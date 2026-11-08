package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUploadLifecycle(t *testing.T) {
	ctx := context.Background()
	s, now := testStore(t)
	name := "Win11_24H2.iso"
	u, err := s.CreateUpload(ctx, NewUpload{Length: 100, Filename: &name, HashState: []byte("h0"), ExpiresAt: now.Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if u.Offset != 0 || u.Complete() || u.Imported || u.InUse || u.ExpiresAt == nil || *u.Filename != name {
		t.Fatalf("created: %+v", u)
	}
	other, _ := s.CreateUpload(ctx, NewUpload{Length: 50, HashState: []byte("h0"), ExpiresAt: now.Add(time.Hour)})

	if err := s.CheckpointUpload(ctx, u.ID, UploadCheckpoint{Offset: 60, HashState: []byte("h60"), ExpiresAt: now.Add(25 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckpointUpload(ctx, u.ID, UploadCheckpoint{Offset: 40, HashState: []byte("x"), ExpiresAt: *now}); !errors.Is(err, ErrBadTransition) {
		t.Errorf("going backwards: %v", err)
	}
	if held, pending, _ := s.UploadBytes(ctx, other.ID); held != 60 || pending != 40 {
		t.Errorf("bytes excluding other: held %d pending %d", held, pending)
	}
	// Claiming an incomplete upload fails.
	if _, err := s.CreateJobForUpload(ctx, u.ID, NewJob{Kind: "image.import"}); !errors.Is(err, ErrUploadIncomplete) {
		t.Errorf("claim incomplete: %v", err)
	}
	// A complete upload must have its hash, and vice versa.
	if err := s.CheckpointUpload(ctx, u.ID, UploadCheckpoint{Offset: 100, HashState: []byte("h"), ExpiresAt: *now}); err == nil {
		t.Error("complete without sha256 accepted")
	}
	sum := strings.Repeat("a", 64)
	if err := s.CheckpointUpload(ctx, u.ID, UploadCheckpoint{Offset: 100, HashState: []byte("h"), SHA256: &sum, ExpiresAt: now.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckpointUpload(ctx, u.ID, UploadCheckpoint{Offset: 100, HashState: []byte("h"), SHA256: &sum, ExpiresAt: *now}); !errors.Is(err, ErrBadTransition) {
		t.Errorf("checkpoint after complete: %v", err)
	}

	// Claim; a second claim and a delete are refused while the job is active.
	job, err := s.CreateJobForUpload(ctx, u.ID, NewJob{Kind: "image.import"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateJobForUpload(ctx, u.ID, NewJob{Kind: "image.import"}); !errors.Is(err, ErrUploadInUse) {
		t.Errorf("second claim: %v", err)
	}
	if err := s.DeleteUpload(ctx, u.ID); !errors.Is(err, ErrUploadInUse) {
		t.Errorf("delete in use: %v", err)
	}
	// In use: not expired even when past its expiry.
	*now = now.Add(48 * time.Hour)
	ids, err := s.ExpireUploads(ctx, *now)
	if err != nil || len(ids) != 1 || ids[0] != other.ID {
		t.Fatalf("expire: %v %v (want only %s)", ids, err, other.ID)
	}

	// The import fails: the upload is released with a new expiry.
	s.StartJob(ctx, job.ID)
	s.FinishJob(ctx, job.ID, JobOutcome{Status: JobFailed, Error: problem})
	retry := now.Add(24 * time.Hour)
	if u, err = s.ReleaseUpload(ctx, u.ID, job.ID, false, retry); err != nil || u.InUse || u.Imported || !u.ExpiresAt.Equal(retry) {
		t.Fatalf("released after failure: %+v %v", u, err)
	}

	// Retry succeeds: kept until deleted, never expires.
	job2, err := s.CreateJobForUpload(ctx, u.ID, NewJob{Kind: "image.import"})
	if err != nil {
		t.Fatal(err)
	}
	s.StartJob(ctx, job2.ID)
	s.FinishJob(ctx, job2.ID, JobOutcome{Status: JobSucceeded})
	if u, err = s.ReleaseUpload(ctx, u.ID, job2.ID, true, time.Time{}); err != nil || !u.Imported || u.ExpiresAt != nil || *u.JobID != job2.ID {
		t.Fatalf("released after success: %+v %v", u, err)
	}
	// A later failed import doesn't make it expire again.
	job3, _ := s.CreateJobForUpload(ctx, u.ID, NewJob{Kind: "image.import"})
	s.StartJob(ctx, job3.ID)
	s.FinishJob(ctx, job3.ID, JobOutcome{Status: JobFailed, Error: problem})
	if u, _ = s.ReleaseUpload(ctx, u.ID, job3.ID, false, retry); !u.Imported || u.ExpiresAt != nil {
		t.Errorf("imported upload got an expiry: %+v", u)
	}
	*now = now.Add(1000 * time.Hour)
	if ids, _ := s.ExpireUploads(ctx, *now); len(ids) != 0 {
		t.Errorf("imported upload expired: %v", ids)
	}

	if list, next, err := s.ListUploads(ctx, 0, ""); err != nil || len(list) != 1 || next != "" {
		t.Errorf("list: %d %q %v", len(list), next, err)
	}
	if err := s.DeleteUpload(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upload(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}
