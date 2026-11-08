package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/secrets"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// KindBackup is the backup job kind; ResourceID makes at most one backup
// queued or running at a time.
const (
	KindBackup = "system.backup"
	ResourceID = "backup"
)

// State keys.
const (
	stateStatus    = "backup.status"
	stateScheduler = "backup.scheduler"
)

// Config is backup settings as the service needs them.
type Config struct {
	Schedule    Schedule
	Retention   int
	Destination *Destination
}

// Destination is backup.destination.
type Destination struct {
	Type, Path, CredentialSecretID string
}

// ErrNotConfigured: no destination is set.
var ErrNotConfigured = errors.New("backup: no destination is configured")

// Status is the recorded outcome of backups.
type Status struct {
	LastSuccessAt *time.Time `json:"lastSuccessAt,omitempty"`
	LastFailureAt *time.Time `json:"lastFailureAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
	LastSnapshot  string     `json:"lastSnapshot,omitempty"`
}

type schedState struct {
	Key   string    `json:"key"`   // schedule + destination it counts slots for
	Slot  time.Time `json:"slot"`  // last slot acted on
	Since time.Time `json:"since"` // when Key was set
}

// ServiceOptions configure the service.
type ServiceOptions struct {
	Store         *store.Store
	Content       *cas.Store
	Vault         *secrets.Vault
	Jobs          *jobs.Runner // nil in tests that don't queue
	DataDir       string
	ServerVersion string
	Config        func() Config
	Location      *time.Location // nil: time.Local
	Now           func() time.Time
	Logger        *slog.Logger
}

// Service runs scheduled and manual backups as jobs.
type Service struct {
	o        ServiceOptions
	log      *slog.Logger
	lastTick time.Time // scheduler goroutine only
}

// tickEvery bounds the scheduler's sleep, so settings changes are picked up
// promptly.
const tickEvery = 15 * time.Second

// NewService returns the backup service.
func NewService(o ServiceOptions) *Service {
	if o.Location == nil {
		o.Location = time.Local
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Service{o: o, log: o.Logger.With("svc", "backup")}
}

// Kind is the job kind to register.
func (s *Service) Kind() jobs.Kind {
	return jobs.Kind{Name: KindBackup, Lane: jobs.LaneBackup, Run: s.run, Abandon: s.abandon}
}

type params struct {
	Trigger string `json:"trigger"`
}

// Enqueue queues a backup, or returns the one already queued or running.
func (s *Service) Enqueue(ctx context.Context, trigger string) (store.Job, error) {
	if s.o.Config().Destination == nil {
		return store.Job{}, ErrNotConfigured
	}
	res := ResourceID
	j, err := s.o.Store.CreateJob(ctx, store.NewJob{Kind: KindBackup, ResourceID: &res, Params: params{Trigger: trigger}})
	if errors.Is(err, store.ErrResourceBusy) {
		return s.o.Store.ActiveJob(ctx, ResourceID)
	}
	if err != nil {
		return store.Job{}, err
	}
	if s.o.Jobs != nil {
		s.o.Jobs.Wake()
	}
	return j, nil
}

// OpenDest opens the configured destination (decrypting a share
// credential), for a run or a listing.
func (s *Service) OpenDest(ctx context.Context, d *Destination) (Dest, error) {
	if d == nil {
		return nil, ErrNotConfigured
	}
	var cred *Credential
	if d.Type == TypeSMB {
		if s.o.Vault == nil {
			return nil, errors.New("backup: secrets are unavailable")
		}
		c, err := s.o.Vault.Credential(ctx, d.CredentialSecretID, "share-credential")
		if err != nil {
			return nil, fmt.Errorf("backup: share credential: %w", err)
		}
		cred = &Credential{Username: c.Username, Password: c.Password}
	}
	return OpenDest(ctx, d.Type, d.Path, cred)
}

// List lists the snapshots at the configured destination.
func (s *Service) List(ctx context.Context) ([]Manifest, error) {
	d, err := s.OpenDest(ctx, s.o.Config().Destination)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return List(d)
}

func destError(err error) *jobs.Error {
	return jobs.Fail(http.StatusBadGateway, "backup_destination_error", "Backup destination error", err.Error())
}

func (s *Service) run(ctx context.Context, r *jobs.Run) error {
	var p params
	r.Params(&p)
	if p.Trigger == "" {
		p.Trigger = TriggerManual
	}
	cfg := s.o.Config()
	err := s.backup(ctx, r, cfg, p.Trigger)
	if err != nil && ctx.Err() == nil {
		s.recordFailure(context.WithoutCancel(ctx), err)
	}
	return err
}

func (s *Service) backup(ctx context.Context, r *jobs.Run, cfg Config, trigger string) error {
	if cfg.Destination == nil {
		return jobs.Fail(http.StatusConflict, "backup_not_configured", "Backups not configured", "Set backup.destination in the settings.")
	}
	r.Progress(0, "Connecting to "+cfg.Destination.Path)
	dest, err := s.OpenDest(ctx, cfg.Destination)
	if err != nil {
		return destError(err)
	}
	defer dest.Close()
	fp := ""
	if s.o.Vault != nil {
		if ring, err := s.o.Vault.Keys().Ring(false); err == nil {
			fp = ring.Fingerprint()
		}
	}
	res, err := Run(ctx, Source{Store: s.o.Store, Content: s.o.Content, DataDir: s.o.DataDir, ServerVersion: s.o.ServerVersion, KeyFingerprint: fp},
		dest, Options{Trigger: trigger, Retention: cfg.Retention, Progress: r.Progress, Logger: r.Logger()})
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, ErrDamaged):
		return jobs.Fail(http.StatusInternalServerError, "damaged_content", "Damaged stored file", err.Error())
	default:
		return destError(err)
	}
	now := res.Manifest.CreatedAt
	st := s.status(ctx)
	st.LastSuccessAt, st.LastSnapshot = &now, res.Manifest.ID
	if st.LastFailureAt != nil && st.LastFailureAt.Before(now) {
		st.LastError = ""
	}
	return s.o.Store.SetState(context.WithoutCancel(ctx), stateStatus, st)
}

func (s *Service) abandon(ctx context.Context, j store.Job, reason string) error {
	if reason != "cancelled" {
		s.recordFailure(ctx, fmt.Errorf("the backup was %s", reason))
	}
	return nil
}

func (s *Service) recordFailure(ctx context.Context, err error) {
	st := s.status(ctx)
	now := s.o.Now().UTC()
	st.LastFailureAt, st.LastError = &now, jobs.AsError(err).Detail
	if st.LastError == "" {
		st.LastError = err.Error()
	}
	if err := s.o.Store.SetState(ctx, stateStatus, st); err != nil {
		s.log.Error("record backup failure", "err", err)
	}
}

func (s *Service) status(ctx context.Context) Status {
	var st Status
	s.o.Store.State(ctx, stateStatus, &st)
	return st
}

func schedKey(cfg Config) string {
	if cfg.Destination == nil {
		return ""
	}
	return cfg.Schedule.Key() + "|" + cfg.Destination.Type + "|" + cfg.Destination.Path
}

// Start runs the scheduler until ctx ends. A slot missed while the server
// was down runs at once; slots before the schedule (or destination) last
// changed don't.
func (s *Service) Start(ctx context.Context) {
	go func() {
		for {
			wait := s.tick(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
}

// tick queues a due backup and returns how long to sleep.
//
// A changed schedule (or destination) counts slots from the previous tick:
// the change happened somewhere since then, so a slot after that tick runs,
// and none before it does. At startup there is no previous tick, so a
// schedule changed while the server was down starts from now.
func (s *Service) tick(ctx context.Context) time.Duration {
	cfg := s.o.Config()
	now := s.o.Now()
	ref := s.lastTick
	if ref.IsZero() || ref.After(now) {
		ref = now
	}
	s.lastTick = now
	key := schedKey(cfg)
	prev, ok := cfg.Schedule.Prev(now, s.o.Location)
	if key == "" || !ok {
		return tickEvery
	}
	var st schedState
	s.o.Store.State(ctx, stateScheduler, &st)
	if st.Key != key {
		from, _ := cfg.Schedule.Prev(ref, s.o.Location)
		st = schedState{Key: key, Slot: from, Since: ref}
		s.o.Store.SetState(ctx, stateScheduler, st)
	}
	if prev.After(st.Slot) {
		if _, err := s.Enqueue(ctx, TriggerScheduled); err != nil {
			s.log.Error("queue scheduled backup", "err", err)
			return tickEvery
		}
		s.log.Info("scheduled backup queued", "slot", prev)
		st.Slot = prev
		s.o.Store.SetState(ctx, stateScheduler, st)
	}
	wait := tickEvery
	if next, ok := cfg.Schedule.Next(now, s.o.Location); ok && next.Sub(now) < wait {
		wait = max(next.Sub(now), time.Second)
	}
	return wait
}

// Health is what /system/health reports about backups.
type Health struct {
	Status
	NextRunAt *time.Time
	Warnings  [][2]string // code, message
}

// Health reports the last results, the next run and warnings.
func (s *Service) Health(ctx context.Context) Health {
	cfg := s.o.Config()
	h := Health{Status: s.status(ctx)}
	now := s.o.Now()
	if cfg.Destination != nil {
		if n, ok := cfg.Schedule.Next(now, s.o.Location); ok {
			h.NextRunAt = &n
		}
	}
	st := h.Status
	if st.LastFailureAt != nil && (st.LastSuccessAt == nil || st.LastFailureAt.After(*st.LastSuccessAt)) {
		h.Warnings = append(h.Warnings, [2]string{"backup_failed", "The last backup failed: " + st.LastError})
	}
	// Overdue: no success since two slots ago, counting only slots after
	// the schedule was set.
	if p1, ok := cfg.Schedule.Prev(now, s.o.Location); ok && cfg.Destination != nil {
		if p2, ok := cfg.Schedule.Prev(p1.Add(-time.Second), s.o.Location); ok {
			var ss schedState
			s.o.Store.State(ctx, stateScheduler, &ss)
			if ss.Key == schedKey(cfg) && ss.Since.Before(p2) && (st.LastSuccessAt == nil || st.LastSuccessAt.Before(p2)) {
				h.Warnings = append(h.Warnings, [2]string{"backup_overdue", "Scheduled backups haven't succeeded since " + p2.Format("2006-01-02 15:04") + "."})
			}
		}
	}
	return h
}

// CheckDestination validates a destination's form against things outside
// the settings document: a local path mustn't be inside the data or
// content directory; an smb destination's secret must exist and be a
// share-credential. It never connects. Returns the field ("path" or
// "credentialSecretId") and the problem, or "".
func CheckDestination(ctx context.Context, st *store.Store, dataDir, contentDir string, d Destination) (field, msg string) {
	switch d.Type {
	case TypeLocal:
		if !filepath.IsAbs(d.Path) {
			return "path", "must be an absolute path"
		}
		p := filepath.Clean(d.Path)
		for _, dir := range []string{dataDir, contentDir} {
			abs, err := filepath.Abs(dir)
			if err != nil {
				continue
			}
			if rel, err := filepath.Rel(abs, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "path", "must not be inside the data or content directory"
			}
			if rel, err := filepath.Rel(p, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "path", "must not contain the data or content directory"
			}
		}
	case TypeSMB:
		x, err := st.Secret(ctx, d.CredentialSecretID)
		if errors.Is(err, store.ErrNotFound) {
			return "credentialSecretId", "no secret " + d.CredentialSecretID
		}
		if err != nil {
			return "credentialSecretId", err.Error()
		}
		if x.Kind != "share-credential" {
			return "credentialSecretId", fmt.Sprintf("%q is a %s secret, not a share-credential", x.Name, x.Kind)
		}
	}
	return "", ""
}
