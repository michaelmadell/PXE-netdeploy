package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

type harness struct {
	st     *store.Store
	bus    *events.Bus
	r      *Runner
	cancel context.CancelFunc

	mu        sync.Mutex
	abandoned map[string]string // job id -> reason
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &harness{st: st, bus: events.New(), abandoned: map[string]string{}}
}

// start builds and starts a runner with the kinds; Abandon is recorded.
func (h *harness) start(t *testing.T, kinds ...Kind) {
	t.Helper()
	h.r = New(Options{Store: h.st, Events: h.bus, ProgressInterval: 20 * time.Millisecond, PollInterval: time.Hour,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for _, k := range kinds {
		if k.Abandon == nil {
			k.Abandon = func(_ context.Context, j store.Job, reason string) error {
				h.mu.Lock()
				defer h.mu.Unlock()
				h.abandoned[j.ID] = reason
				return nil
			}
		}
		h.r.Register(k)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	if err := h.r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); <-h.r.Done() })
}

func (h *harness) queue(t *testing.T, kind string) store.Job {
	t.Helper()
	j, err := h.st.CreateJob(context.Background(), store.NewJob{Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	if h.r != nil {
		h.r.Wake()
	}
	return j
}

func (h *harness) wait(t *testing.T, id string, status string) store.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		j, err := h.st.Job(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status == status {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s is %s, want %s", id, j.Status, status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) reason(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.abandoned[id]
}

func code(t *testing.T, j store.Job) string {
	t.Helper()
	var p struct{ Code string }
	if err := json.Unmarshal(j.Error, &p); err != nil {
		t.Fatalf("error %q: %v", j.Error, err)
	}
	return p.Code
}

// Builds run one at a time, in the order they were queued.
func TestBuildLaneRunsOneAtATimeInOrder(t *testing.T) {
	h := newHarness(t)
	var mu sync.Mutex
	active, maxActive := 0, 0
	var order []string
	release := make(chan struct{})
	h.start(t, Kind{Name: "bootImage.build", Lane: LaneBuild, Run: func(ctx context.Context, r *Run) error {
		mu.Lock()
		active++
		maxActive = max(maxActive, active)
		order = append(order, r.Job.ID)
		mu.Unlock()
		<-release
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	}})
	var ids []string
	for range 4 {
		ids = append(ids, h.queue(t, "bootImage.build").ID)
	}
	h.wait(t, ids[0], store.JobRunning)
	for _, id := range ids[1:] {
		if j, _ := h.st.Job(context.Background(), id); j.Status != store.JobQueued {
			t.Errorf("%s is %s while the first build runs", id, j.Status)
		}
	}
	close(release)
	for _, id := range ids {
		h.wait(t, id, store.JobSucceeded)
	}
	if maxActive != 1 || fmt.Sprint(order) != fmt.Sprint(ids) {
		t.Errorf("max concurrent %d (want 1), order %v (want %v)", maxActive, order, ids)
	}
}

func TestImportLaneRunsTwo(t *testing.T) {
	h := newHarness(t)
	started := make(chan string, 3)
	release := make(chan struct{})
	h.start(t, Kind{Name: "image.import", Lane: LaneImport, Run: func(ctx context.Context, r *Run) error {
		started <- r.Job.ID
		<-release
		return nil
	}})
	a, b, c := h.queue(t, "image.import"), h.queue(t, "image.import"), h.queue(t, "image.import")
	h.wait(t, a.ID, store.JobRunning)
	h.wait(t, b.ID, store.JobRunning)
	time.Sleep(50 * time.Millisecond)
	if j, _ := h.st.Job(context.Background(), c.ID); j.Status != store.JobQueued {
		t.Errorf("third import is %s", j.Status)
	}
	close(release)
	h.wait(t, c.ID, store.JobSucceeded)
}

func TestProgressIsThrottledAndPublished(t *testing.T) {
	h := newHarness(t)
	sub := h.bus.Subscribe()
	defer sub.Close()
	h.start(t, Kind{Name: "image.import", Lane: LaneImport, Run: func(ctx context.Context, r *Run) error {
		for i := range 1000 {
			r.Progress(i/10, "copying")
			if i%100 == 0 {
				time.Sleep(10 * time.Millisecond)
			}
		}
		r.SetResource("existing-image")
		return nil
	}})
	j := h.queue(t, "image.import")
	f := h.wait(t, j.ID, store.JobSucceeded)
	if f.Percent != 100 || *f.ResourceID != "existing-image" {
		t.Errorf("finished: %d%% resource %v", f.Percent, f.ResourceID)
	}
	n := 0
	timeout := time.After(time.Second)
	for done := false; !done; {
		select {
		case e := <-sub.C:
			if e.Type != events.JobUpdated || e.ResourceID != j.ID {
				t.Fatalf("event %+v", e)
			}
			n++
			if e.Data.(map[string]any)["status"] == store.JobSucceeded {
				done = true
			}
		case <-timeout:
			t.Fatal("no succeeded event")
		}
	}
	// running + a handful of progress updates + succeeded, not 1000.
	if n < 2 || n > 20 {
		t.Errorf("%d events for 1000 progress calls", n)
	}
}

func TestCancel(t *testing.T) {
	h := newHarness(t)
	running := make(chan struct{})
	h.start(t, Kind{Name: "bootImage.build", Lane: LaneBuild, Run: func(ctx context.Context, r *Run) error {
		close(running)
		<-ctx.Done()
		return ctx.Err()
	}})
	first, second := h.queue(t, "bootImage.build"), h.queue(t, "bootImage.build")
	<-running

	j, err := h.r.Cancel(context.Background(), second.ID)
	if err != nil || j.Status != store.JobCancelled || h.reason(second.ID) != "cancelled" {
		t.Fatalf("cancel queued: %+v %v, abandon %q", j, err, h.reason(second.ID))
	}
	j, err = h.r.Cancel(context.Background(), first.ID)
	if err != nil || !j.CancelRequested {
		t.Fatalf("cancel running: %+v %v", j, err)
	}
	f := h.wait(t, first.ID, store.JobCancelled)
	if f.Error != nil || h.reason(first.ID) != "" {
		t.Errorf("cancelled running job: error %s, abandon %q", f.Error, h.reason(first.ID))
	}
	// Finished jobs are returned unchanged.
	if j, err := h.r.Cancel(context.Background(), first.ID); err != nil || j.Status != store.JobCancelled {
		t.Errorf("cancel finished: %+v %v", j, err)
	}
	if _, err := h.r.Cancel(context.Background(), "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cancel missing: %v", err)
	}
}

func TestFailureCodes(t *testing.T) {
	h := newHarness(t)
	errs := map[string]error{}
	h.start(t, Kind{Name: "image.import", Lane: LaneImport, Run: func(ctx context.Context, r *Run) error {
		var p struct{ Err string }
		r.Params(&p)
		switch p.Err {
		case "panic":
			panic("boom")
		default:
			return errs[p.Err]
		}
	}})
	errs["storage"] = fmt.Errorf("writing: %w", cas.ErrInsufficientStorage)
	errs["typed"] = Fail(http.StatusUnprocessableEntity, "not_windows_media", "Not Windows media", "no sources/install.wim")
	errs["plain"] = errors.New("disk on fire")
	want := map[string]string{"storage": "insufficient_storage", "typed": "not_windows_media", "plain": "internal_error", "panic": "internal_error"}
	for name, c := range want {
		j, err := h.st.CreateJob(context.Background(), store.NewJob{Kind: "image.import", Params: map[string]string{"err": name}})
		if err != nil {
			t.Fatal(err)
		}
		h.r.Wake()
		f := h.wait(t, j.ID, store.JobFailed)
		if got := code(t, f); got != c {
			t.Errorf("%s: code %q, want %q", name, got, c)
		}
		if name == "panic" && h.reason(j.ID) != "failed" {
			t.Errorf("panicked job not abandoned: %q", h.reason(j.ID))
		}
	}
}

// A job left running by a crashed process fails as interrupted at startup.
func TestStartInterruptsLeftoverJobs(t *testing.T) {
	h := newHarness(t)
	j := h.queue(t, "image.import")
	if _, err := h.st.StartJob(context.Background(), j.ID); err != nil {
		t.Fatal(err)
	}
	queued := h.queue(t, "image.import")
	ran := make(chan string, 1)
	h.start(t, Kind{Name: "image.import", Lane: LaneImport, Run: func(ctx context.Context, r *Run) error {
		ran <- r.Job.ID
		return nil
	}})
	f := h.wait(t, j.ID, store.JobFailed)
	if code(t, f) != "interrupted" || h.reason(j.ID) != "interrupted" {
		t.Errorf("leftover: %s, abandon %q", f.Error, h.reason(j.ID))
	}
	// Jobs still queued run after the restart.
	if id := <-ran; id != queued.ID {
		t.Errorf("ran %s, want the queued job", id)
	}
}

// Shutting down fails a running job as interrupted and waits for it.
func TestShutdownInterruptsRunningJob(t *testing.T) {
	h := newHarness(t)
	running := make(chan struct{})
	h.start(t, Kind{Name: "bootImage.build", Lane: LaneBuild, Run: func(ctx context.Context, r *Run) error {
		close(running)
		<-ctx.Done()
		return fmt.Errorf("copy: %w", ctx.Err())
	}})
	j := h.queue(t, "bootImage.build")
	<-running
	h.cancel()
	<-h.r.Done()
	f, _ := h.st.Job(context.Background(), j.ID)
	if f.Status != store.JobFailed || code(t, f) != "interrupted" || h.reason(j.ID) != "interrupted" {
		t.Errorf("after shutdown: %s %s, abandon %q", f.Status, f.Error, h.reason(j.ID))
	}
}

func TestUnknownKindFails(t *testing.T) {
	h := newHarness(t)
	h.start(t, Kind{Name: "image.import", Lane: LaneImport, Run: func(context.Context, *Run) error { return nil }})
	j := h.queue(t, "bootImage.build") // no kind registered for it
	if f := h.wait(t, j.ID, store.JobFailed); code(t, f) != "internal_error" {
		t.Errorf("unknown kind: %s", f.Error)
	}
}
