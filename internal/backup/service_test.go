package backup

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

func TestSchedulerAndHealth(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	cfg := Config{Schedule: Schedule{Frequency: Daily, Time: "02:00"}, Retention: 5,
		Destination: &Destination{Type: TypeLocal, Path: t.TempDir()}}
	clock := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	newSvc := func() *Service {
		return NewService(ServiceOptions{Store: e.st, Content: e.content, DataDir: e.data, Config: func() Config { return cfg },
			Location: time.UTC, Now: func() time.Time { return clock }, Logger: quiet})
	}
	s := newSvc()
	queued := func() int {
		list, _, _ := e.st.ListJobs(ctx, store.JobFilter{Kind: KindBackup, Status: "queued"})
		return len(list)
	}

	// First sight of the schedule: no catch-up for today's 02:00.
	s.tick(ctx)
	if queued() != 0 {
		t.Fatal("backup queued for a slot before the schedule was set")
	}
	if h := s.Health(ctx); h.NextRunAt == nil || !h.NextRunAt.Equal(time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC)) {
		t.Errorf("next run: %v", h.NextRunAt)
	}
	clock = time.Date(2026, 11, 9, 2, 0, 30, 0, time.UTC)
	if wait := s.tick(ctx); wait > tickEvery {
		t.Errorf("wait %v", wait)
	}
	if queued() != 1 {
		t.Fatalf("not queued at the slot: %d", queued())
	}
	s.tick(ctx)
	if queued() != 1 {
		t.Fatal("queued twice")
	}
	// Mark it done, then the server is down for two slots: one catch-up.
	j, _ := e.st.ActiveJob(ctx, ResourceID)
	e.st.StartJob(ctx, j.ID)
	e.st.FinishJob(ctx, j.ID, store.JobOutcome{Status: store.JobSucceeded, Percent: 100})
	clock = time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	s = newSvc()
	s.tick(ctx)
	if queued() != 1 {
		t.Fatalf("catch-up: %d queued", queued())
	}
	// No success since 2026-11-10 02:00 (two slots ago): overdue.
	h := s.Health(ctx)
	if !hasWarning(h, "backup_overdue") {
		t.Errorf("no overdue warning: %+v", h.Warnings)
	}
	s.recordFailure(ctx, jobs.Fail(502, "backup_destination_error", "x", "share unreachable"))
	if h := s.Health(ctx); !hasWarning(h, "backup_failed") || h.LastError != "share unreachable" {
		t.Errorf("failure: %+v", h)
	}
	// A schedule change starts afresh.
	j, _ = e.st.ActiveJob(ctx, ResourceID)
	e.st.RequestJobCancel(ctx, j.ID)
	cfg.Schedule.Time = "03:00"
	s.tick(ctx)
	if queued() != 0 {
		t.Errorf("schedule change queued a catch-up")
	}
	if h := s.Health(ctx); hasWarning(h, "backup_overdue") {
		t.Error("overdue right after a schedule change")
	}
	// Changed shortly before a slot that passes before the next tick: it
	// still runs (counted from the previous tick).
	clock = time.Date(2026, 11, 11, 11, 38, 50, 0, time.UTC)
	s.tick(ctx)
	cfg.Schedule = Schedule{Frequency: Hourly, Minute: 39}
	clock = time.Date(2026, 11, 11, 11, 39, 5, 0, time.UTC)
	s.tick(ctx)
	if queued() != 1 {
		t.Errorf("slot right after a schedule change was skipped: %d queued", queued())
	}
	cfg.Destination = nil
	if _, err := s.Enqueue(ctx, TriggerManual); err != ErrNotConfigured {
		t.Errorf("no destination: %v", err)
	}
}

func hasWarning(h Health, code string) bool {
	for _, w := range h.Warnings {
		if w[0] == code {
			return true
		}
	}
	return false
}

func TestBackupJob(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.addFiles(t, map[string]string{"x.txt": "x"})
	cfg := Config{Retention: 5, Destination: &Destination{Type: TypeLocal, Path: t.TempDir()}}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := jobs.New(jobs.Options{Store: e.st, Logger: quiet, ProgressInterval: 10 * time.Millisecond})
	s := NewService(ServiceOptions{Store: e.st, Content: e.content, Jobs: runner, DataDir: e.data, ServerVersion: "test",
		Config: func() Config { return cfg }, Logger: quiet})
	runner.Register(s.Kind())
	rctx, cancel := context.WithCancel(ctx)
	runner.Start(rctx)
	defer func() { cancel(); <-runner.Done() }()

	j, err := s.Enqueue(ctx, TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if j2, _ := s.Enqueue(ctx, TriggerManual); j2.ID != j.ID && !j2.Finished() {
		t.Errorf("second enqueue made another job while one was active")
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		j, _ = e.st.Job(ctx, j.ID)
		if j.Finished() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("backup job still running")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if j.Status != store.JobSucceeded || !strings.HasPrefix(j.Message, "Backed up 1 files") {
		t.Fatalf("job: %s %q %s", j.Status, j.Message, j.Error)
	}
	if h := s.Health(ctx); h.LastSuccessAt == nil || len(h.Warnings) != 0 {
		t.Errorf("health: %+v", h)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || list[0].Trigger != TriggerManual {
		t.Fatalf("list: %+v %v", list, err)
	}
	// In the snapshot, the backup's own job has succeeded.
	db, _ := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(cfg.Destination.Path, SnapshotsDir, list[0].ID, store.FileName))+"?mode=ro")
	var status string
	db.QueryRowContext(ctx, `SELECT status FROM jobs WHERE id = ?`, j.ID).Scan(&status)
	db.Close()
	if status != store.JobSucceeded {
		t.Errorf("backup job in its own snapshot: %q", status)
	}

	// An SMB destination with a missing secret fails cleanly.
	cfg.Destination = &Destination{Type: TypeSMB, Path: `\\127.0.0.1\nope`, CredentialSecretID: "missing"}
	j, _ = s.Enqueue(ctx, TriggerManual)
	for !j.Finished() {
		time.Sleep(20 * time.Millisecond)
		j, _ = e.st.Job(ctx, j.ID)
	}
	if j.Status != store.JobFailed || !strings.Contains(string(j.Error), "backup_destination_error") {
		t.Errorf("smb without secret: %s %s", j.Status, j.Error)
	}
}
