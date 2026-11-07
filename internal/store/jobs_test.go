package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

var problem = json.RawMessage(`{"type":"about:blank","title":"Failed","status":500,"code":"internal_error"}`)

func TestJobLifecycle(t *testing.T) {
	ctx := context.Background()
	s, now := testStore(t)
	res := "img1"
	j, err := s.CreateJob(ctx, NewJob{Kind: "image.import", ResourceID: &res, Params: map[string]string{"uploadId": "u1"}})
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != JobQueued || string(j.Params) != `{"uploadId":"u1"}` || j.StartedAt != nil {
		t.Fatalf("created: %+v", j)
	}
	// One active job per resource.
	if _, err := s.CreateJob(ctx, NewJob{Kind: "image.import", ResourceID: &res}); !errors.Is(err, ErrResourceBusy) {
		t.Fatalf("second job: %v, want ErrResourceBusy", err)
	}
	if err := s.UpdateJobProgress(ctx, j.ID, 10, "x"); !errors.Is(err, ErrBadTransition) {
		t.Errorf("progress while queued: %v", err)
	}
	if j, err = s.StartJob(ctx, j.ID); err != nil || j.Status != JobRunning || j.StartedAt == nil {
		t.Fatalf("start: %+v %v", j, err)
	}
	if _, err := s.StartJob(ctx, j.ID); !errors.Is(err, ErrBadTransition) {
		t.Errorf("double start: %v", err)
	}
	if err := s.UpdateJobProgress(ctx, j.ID, 150, "copying"); err != nil {
		t.Fatal(err)
	}
	if j, _ = s.Job(ctx, j.ID); j.Percent != 100 || j.Message != "copying" {
		t.Errorf("progress: %d %q", j.Percent, j.Message)
	}
	// A failed job must carry a Problem with a code.
	if _, err := s.FinishJob(ctx, j.ID, JobOutcome{Status: JobFailed}); err == nil {
		t.Error("failed without error accepted")
	}
	other := "img0"
	*now = now.Add(time.Minute)
	if j, err = s.FinishJob(ctx, j.ID, JobOutcome{Status: JobSucceeded, ResourceID: &other}); err != nil {
		t.Fatal(err)
	}
	if j.Status != JobSucceeded || *j.ResourceID != "img0" || j.Percent != 100 || j.FinishedAt == nil {
		t.Errorf("finished: %+v", j)
	}
	// The resource is free again.
	if _, err := s.CreateJob(ctx, NewJob{Kind: "image.import", ResourceID: &res}); err != nil {
		t.Errorf("job after finish: %v", err)
	}
}

func TestJobCancelAndInterrupt(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	queued, _ := s.CreateJob(ctx, NewJob{Kind: "bootImage.build"})
	running, _ := s.CreateJob(ctx, NewJob{Kind: "bootImage.build"})
	s.StartJob(ctx, running.ID)

	j, prev, err := s.RequestJobCancel(ctx, queued.ID)
	if err != nil || prev != JobQueued || j.Status != JobCancelled || j.FinishedAt == nil {
		t.Fatalf("cancel queued: %+v %s %v", j, prev, err)
	}
	if _, err := s.StartJob(ctx, queued.ID); !errors.Is(err, ErrBadTransition) {
		t.Errorf("start after cancel: %v", err)
	}
	j, prev, err = s.RequestJobCancel(ctx, running.ID)
	if err != nil || prev != JobRunning || j.Status != JobRunning || !j.CancelRequested {
		t.Fatalf("cancel running: %+v %s %v", j, prev, err)
	}
	if _, _, err := s.RequestJobCancel(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel missing: %v", err)
	}

	got, err := s.InterruptRunningJobs(ctx, problem)
	if err != nil || len(got) != 1 || got[0].ID != running.ID || got[0].Status != JobFailed || string(got[0].Error) != string(problem) {
		t.Fatalf("interrupt: %+v %v", got, err)
	}
}

func TestQueuedAndListJobs(t *testing.T) {
	ctx := context.Background()
	s, now := testStore(t)
	var ids []string
	for _, k := range []string{"image.import", "bootImage.build", "driverPackage.import", "image.import"} {
		j, err := s.CreateJob(ctx, NewJob{Kind: k})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	q, err := s.QueuedJobs(ctx, []string{"image.import", "driverPackage.import"}, 2)
	if err != nil || len(q) != 2 || q[0].ID != ids[0] || q[1].ID != ids[2] {
		t.Fatalf("queued: %+v %v", q, err)
	}
	page, next, err := s.ListJobs(ctx, JobFilter{Kind: "image.import", Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != ids[0] || next != ids[0] {
		t.Fatalf("page 1: %+v %q %v", page, next, err)
	}
	page, next, _ = s.ListJobs(ctx, JobFilter{Kind: "image.import", Limit: 1, After: next})
	if len(page) != 1 || page[0].ID != ids[3] || next != "" {
		t.Fatalf("page 2: %+v %q", page, next)
	}

	// Purge removes only jobs finished before the cutoff.
	s.StartJob(ctx, ids[1])
	s.FinishJob(ctx, ids[1], JobOutcome{Status: JobSucceeded})
	s.RequestJobCancel(ctx, ids[2])
	*now = now.Add(31 * 24 * time.Hour)
	if n, err := s.PurgeJobs(ctx, now.Add(-30*24*time.Hour)); err != nil || n != 2 {
		t.Errorf("purge = %d, %v; want 2", n, err)
	}
	if all, _, _ := s.ListJobs(ctx, JobFilter{}); len(all) != 2 {
		t.Errorf("%d jobs left, want the 2 queued", len(all))
	}
}
