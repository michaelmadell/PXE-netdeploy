package cas

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Sweep deletes unreferenced trees and blobs (rows, then files), then
// removes any blob file without a row: left by a crash between publishing
// and recording, or a delete Windows refused because the file was open.
// It holds the store's lock, so no commit runs meanwhile.
func (s *Store) Sweep(ctx context.Context) error {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	if s.sweepHolds > 0 {
		s.sweepPending = true
		s.log.Info("sweep deferred until the running backup finishes")
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	trees, hashes, err := s.db.SweepContent(ctx)
	if err != nil {
		return err
	}
	removed, failed := 0, 0
	for _, h := range hashes {
		if err := os.Remove(s.blobPath(h)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failed++
			s.log.Warn("blob delete failed; retried by the next sweep", "sha256", h, "err", err)
			continue
		}
		removed++
	}

	known := map[string]bool{}
	if err := s.db.Blobs(ctx, func(h string, _ int64) error { known[h] = true; return nil }); err != nil {
		return err
	}
	orphans := 0
	base := filepath.Join(s.root, blobDir)
	fanout, err := os.ReadDir(base)
	if err != nil {
		return err
	}
	for _, d := range fanout {
		if !d.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(base, d.Name()))
		if err != nil {
			return err
		}
		for _, f := range files {
			if known[f.Name()] {
				continue
			}
			p := filepath.Join(base, d.Name(), f.Name())
			if err := os.RemoveAll(p); err != nil {
				s.log.Warn("orphan blob delete failed", "path", p, "err", err)
				continue
			}
			orphans++
		}
	}
	if trees > 0 || len(hashes) > 0 || orphans > 0 {
		s.log.Info("content swept", "trees", trees, "blobs", removed, "blob_delete_failures", failed, "orphan_files", orphans)
	}
	return nil
}

// HoldSweep stops sweeps from deleting anything until release is called
// (ADR 0020: a backup copies the blobs its database snapshot references).
// It waits for a sweep in progress. A sweep requested while held runs, in
// the background, when the last hold is released.
func (s *Store) HoldSweep() (release func()) {
	s.sweepMu.Lock()
	s.sweepHolds++
	s.sweepMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.sweepMu.Lock()
			s.sweepHolds--
			run := s.sweepHolds == 0 && s.sweepPending
			if run {
				s.sweepPending = false
			}
			s.sweepMu.Unlock()
			if run {
				go func() {
					if err := s.Sweep(context.Background()); err != nil {
						s.log.Warn("deferred sweep", "err", err)
					}
				}()
			}
		})
	}
}

// ReportDamaged records a stored blob found damaged (e.g. by a backup
// re-hashing it); storage health becomes error until the next start.
func (s *Store) ReportDamaged(h, detail string) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.problems = append(s.problems, h+" "+detail)
	s.log.Error("stored file damaged", "sha256", h, "detail", detail)
}
