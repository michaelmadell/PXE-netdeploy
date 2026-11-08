// Package jobs runs background work (ADR 0016): imports and boot image
// builds. The jobs table is the queue; this runner takes queued jobs per
// lane, oldest first, and records progress and outcomes.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Lanes and their default sizes.
const (
	LaneBuild  = "build"  // boot image builds: one at a time (DISM mounts)
	LaneImport = "import" // ISO and driver imports
)

// DefaultLanes are the concurrency limits per lane.
var DefaultLanes = map[string]int{LaneBuild: 1, LaneImport: 2}

// Retention is how long finished jobs are kept.
const Retention = 30 * 24 * time.Hour

// Kind is one kind of job.
type Kind struct {
	Name string // api JobKind, e.g. "image.import"
	Lane string
	// Run does the work. ctx is cancelled on cancel or shutdown. Before
	// returning an error, Run leaves its resource in a final state (e.g.
	// status failed) and discards its staging files.
	Run func(ctx context.Context, r *Run) error
	// Abandon fixes up the resource of a job whose Run never cleaned up: a
	// queued job that was cancelled, or one interrupted by a restart.
	// reason is "cancelled", "interrupted" or "failed" (Run panicked). It
	// runs before the job's final status is recorded (for "cancelled", just
	// after: the job was never started). May be nil. Must be idempotent.
	Abandon func(ctx context.Context, j store.Job, reason string) error
}

// Options configure a Runner.
type Options struct {
	Store  *store.Store
	Events *events.Bus // nil: no events
	Logger *slog.Logger
	Lanes  map[string]int // nil: DefaultLanes
	// ProgressInterval is the minimum time between progress writes and
	// events per job. Zero means 500 ms.
	ProgressInterval time.Duration
	// PollInterval re-reads the queue in case a wake-up was missed. Zero
	// means 30 s.
	PollInterval time.Duration
}

// Runner runs queued jobs.
type Runner struct {
	opts  Options
	log   *slog.Logger
	kinds map[string]Kind
	wake  chan struct{}

	mu      sync.Mutex
	ctx     context.Context
	busy    map[string]int                // lane -> running jobs
	running map[string]context.CancelFunc // job id -> cancel
	wg      sync.WaitGroup
	done    chan struct{}
}

// New returns a Runner. Register kinds, then Start.
func New(opts Options) *Runner {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Lanes == nil {
		opts.Lanes = DefaultLanes
	}
	if opts.ProgressInterval <= 0 {
		opts.ProgressInterval = 500 * time.Millisecond
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 30 * time.Second
	}
	return &Runner{
		opts: opts, log: opts.Logger.With("svc", "jobs"), kinds: map[string]Kind{},
		wake: make(chan struct{}, 1), busy: map[string]int{}, running: map[string]context.CancelFunc{},
		done: make(chan struct{}),
	}
}

// Register adds a kind. Call before Start.
func (r *Runner) Register(k Kind) {
	if _, ok := r.opts.Lanes[k.Lane]; !ok {
		panic(fmt.Sprintf("jobs: kind %s: unknown lane %q", k.Name, k.Lane))
	}
	r.kinds[k.Name] = k
}

// Start fails jobs left running by a previous process (interrupted), then
// runs queued jobs until ctx ends. Done closes once every job has stopped.
func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	r.ctx = ctx
	r.mu.Unlock()
	if err := r.interrupt(ctx); err != nil {
		return err
	}
	go r.loop(ctx)
	return nil
}

// Done is closed after Start's ctx ends and every running job has finished.
func (r *Runner) Done() <-chan struct{} { return r.done }

// Wake makes the runner look at the queue now (after inserting a job).
func (r *Runner) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// interrupt handles jobs a previous process left running: each kind's
// Abandon fixes up the resource first, then the jobs are failed.
func (r *Runner) interrupt(ctx context.Context) error {
	running, _, err := r.opts.Store.ListJobs(ctx, store.JobFilter{Status: store.JobRunning, Limit: 200})
	if err != nil {
		return err
	}
	for _, j := range running {
		r.abandon(ctx, j, "interrupted")
	}
	jobs, err := r.opts.Store.InterruptRunningJobs(ctx, interrupted)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		r.log.Warn("job interrupted by a restart", "job_id", j.ID, "kind", j.Kind)
		r.publish(j)
	}
	return nil
}

func (r *Runner) loop(ctx context.Context) {
	defer close(r.done)
	poll := time.NewTicker(r.opts.PollInterval)
	defer poll.Stop()
	purge := time.NewTicker(time.Hour)
	defer purge.Stop()
	r.dispatch(ctx)
	r.purge(ctx)
	for {
		select {
		case <-ctx.Done():
			r.wg.Wait()
			return
		case <-r.wake:
		case <-poll.C:
		case <-purge.C:
			r.purge(ctx)
		}
		r.dispatch(ctx)
	}
}

// dispatch starts queued jobs while their lanes have room.
func (r *Runner) dispatch(ctx context.Context) {
	for lane, size := range r.opts.Lanes {
		r.mu.Lock()
		free := size - r.busy[lane]
		r.mu.Unlock()
		if free <= 0 {
			continue
		}
		var kinds []string
		for name, k := range r.kinds {
			if k.Lane == lane {
				kinds = append(kinds, name)
			}
		}
		jobs, err := r.opts.Store.QueuedJobs(ctx, kinds, free)
		if err != nil {
			if ctx.Err() == nil {
				r.log.Error("read job queue", "lane", lane, "err", err)
			}
			continue
		}
		for _, j := range jobs {
			r.start(ctx, j)
		}
	}
	r.failUnknown(ctx)
}

// failUnknown fails queued jobs no registered kind can run, so they don't
// wait forever.
func (r *Runner) failUnknown(ctx context.Context) {
	jobs, _, err := r.opts.Store.ListJobs(ctx, store.JobFilter{Status: store.JobQueued, Limit: 200})
	if err != nil {
		return
	}
	for _, j := range jobs {
		if _, ok := r.kinds[j.Kind]; ok {
			continue
		}
		if _, err := r.opts.Store.StartJob(ctx, j.ID); err != nil {
			continue
		}
		f, err := r.opts.Store.FinishJob(ctx, j.ID, store.JobOutcome{Status: store.JobFailed,
			Error: problem(fmt.Errorf("no handler for job kind %s in this server version", j.Kind))})
		if err == nil {
			r.log.Error("job kind not supported", "job_id", j.ID, "kind", j.Kind)
			r.publish(f)
		}
	}
}

func (r *Runner) start(ctx context.Context, j store.Job) {
	k := r.kinds[j.Kind]
	j, err := r.opts.Store.StartJob(ctx, j.ID)
	if err != nil {
		if !errors.Is(err, store.ErrBadTransition) && ctx.Err() == nil {
			r.log.Error("start job", "job_id", j.ID, "err", err)
		}
		return
	}
	jctx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	r.busy[k.Lane]++
	r.running[j.ID] = cancel
	r.mu.Unlock()
	r.publish(j)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.execute(jctx, k, j)
		cancel()
		r.mu.Lock()
		r.busy[k.Lane]--
		delete(r.running, j.ID)
		r.mu.Unlock()
		r.Wake()
	}()
}

func (r *Runner) execute(ctx context.Context, k Kind, j store.Job) {
	log := r.log.With("job_id", j.ID, "kind", j.Kind)
	if j.ResourceID != nil {
		log = log.With("resource_id", *j.ResourceID)
	}
	log.Info("job started")
	run := &Run{Job: j, runner: r, log: log}
	stop := run.startFlusher()
	err, panicked := safeRun(ctx, k.Run, run)
	stop()

	// Shutdown (not a cancel request): fail as interrupted now rather than
	// leaving the row for the next start.
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	out := store.JobOutcome{ResourceID: run.resource()}
	out.Percent, out.Message = run.progress()
	switch {
	case err == nil:
		out.Status = store.JobSucceeded
	case run.cancelRequested():
		out.Status = store.JobCancelled
	case isCancel(err) && r.stopping():
		out.Status, out.Error = store.JobFailed, interrupted
	default:
		out.Status, out.Error = store.JobFailed, problem(err)
	}
	// Fix up the resource before the job reads as finished, so nobody sees
	// a failed job next to a resource still marked in progress.
	switch {
	case panicked:
		r.abandon(bg, j, "failed")
	case out.Error != nil && string(out.Error) == string(interrupted):
		r.abandon(bg, j, "interrupted")
	}
	f, ferr := r.opts.Store.FinishJob(bg, j.ID, out)
	if ferr != nil {
		log.Error("record job outcome", "status", out.Status, "err", ferr)
		return
	}
	dur := time.Since(*f.StartedAt).Round(time.Millisecond)
	switch out.Status {
	case store.JobFailed:
		log.Warn("job failed", "duration", dur.String(), "error", string(out.Error))
	default:
		log.Info("job "+out.Status, "duration", dur.String())
	}
	r.publish(f)
}

func (r *Runner) stopping() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ctx != nil && r.ctx.Err() != nil
}

// safeRun runs fn, turning a panic into an error (the job fails; the
// server keeps running).
func safeRun(ctx context.Context, fn func(context.Context, *Run) error, run *Run) (err error, panicked bool) {
	defer func() {
		if p := recover(); p != nil {
			run.log.Error("job panicked", "panic", p, "stack", string(debug.Stack()))
			err, panicked = fmt.Errorf("job panicked: %v", p), true
		}
	}()
	return fn(ctx, run), false
}

// Cancel cancels a queued job at once or asks a running one to stop.
// Finished jobs are returned unchanged. store.ErrNotFound.
func (r *Runner) Cancel(ctx context.Context, id string) (store.Job, error) {
	j, prev, err := r.opts.Store.RequestJobCancel(ctx, id)
	if err != nil {
		return store.Job{}, err
	}
	switch prev {
	case store.JobQueued:
		r.log.Info("job cancelled while queued", "job_id", id, "kind", j.Kind)
		r.abandon(ctx, j, "cancelled")
		r.publish(j)
	case store.JobRunning:
		r.mu.Lock()
		cancel := r.running[id]
		r.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		r.log.Info("job cancel requested", "job_id", id, "kind", j.Kind)
		r.publish(j)
	}
	return j, nil
}

func (r *Runner) abandon(ctx context.Context, j store.Job, reason string) {
	k, ok := r.kinds[j.Kind]
	if !ok || k.Abandon == nil {
		return
	}
	if err := k.Abandon(ctx, j, reason); err != nil {
		r.log.Error("abandon job resource", "job_id", j.ID, "kind", j.Kind, "reason", reason, "err", err)
	}
}

func (r *Runner) purge(ctx context.Context) {
	n, err := r.opts.Store.PurgeJobs(ctx, time.Now().Add(-Retention))
	if err != nil {
		if ctx.Err() == nil {
			r.log.Warn("job purge failed", "err", err)
		}
		return
	}
	if n > 0 {
		r.log.Debug("finished jobs purged", "count", n)
	}
}

// publish sends job.updated.
func (r *Runner) publish(j store.Job) {
	r.opts.Events.Publish(events.Event{Type: events.JobUpdated, ResourceID: j.ID, Data: map[string]any{
		"kind": j.Kind, "status": j.Status, "percent": j.Percent, "resourceId": j.ResourceID,
	}})
}

// Run is a running job, as seen by its Kind.Run.
type Run struct {
	Job    store.Job
	runner *Runner
	log    *slog.Logger

	mu      sync.Mutex
	percent int
	message string
	dirty   bool
	res     *string
}

// Logger logs with the job's id and kind.
func (r *Run) Logger() *slog.Logger { return r.log }

// Params unmarshals the job's parameters into v.
func (r *Run) Params(v any) error { return json.Unmarshal(r.Job.Params, v) }

// Progress reports progress (0-100) and what the job is doing. Cheap: it is
// written and published at most every ProgressInterval.
func (r *Run) Progress(percent int, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.percent, r.message, r.dirty = max(0, min(100, percent)), message, true
}

// SetResource points the job at a different resource when it finishes
// (an import that found an identical existing item).
func (r *Run) SetResource(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.res = &id
}

func (r *Run) resource() *string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.res
}

func (r *Run) progress() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.percent, r.message
}

// CancelRequested reports whether the admin cancelled the job (as opposed
// to the server shutting down, which also cancels its context). A job uses
// it to record a cancelled outcome on its resource; an interrupted one is
// left to Kind.Abandon.
func (r *Run) CancelRequested() bool { return r.cancelRequested() }

// cancelRequested re-reads the row: a cancel request sets it before
// cancelling the context.
func (r *Run) cancelRequested() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	j, err := r.runner.opts.Store.Job(ctx, r.Job.ID)
	return err == nil && j.CancelRequested
}

// startFlusher writes progress every ProgressInterval while it changes.
func (r *Run) startFlusher() (stop func()) {
	t := time.NewTicker(r.runner.opts.ProgressInterval)
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				r.flush()
			}
		}
	}()
	return func() { t.Stop(); close(quit); <-done }
}

func (r *Run) flush() {
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return
	}
	r.dirty = false
	p, m := r.percent, r.message
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.runner.opts.Store.UpdateJobProgress(ctx, r.Job.ID, p, m); err != nil {
		if !errors.Is(err, store.ErrBadTransition) {
			r.log.Warn("record job progress", "err", err)
		}
		return
	}
	j := r.Job
	j.Percent, j.Message = p, m
	r.runner.publish(j)
}
