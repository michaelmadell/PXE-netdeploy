package cas

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Sweep deletes unreferenced trees and blobs (rows, then files), then
// removes any blob file without a row: left by a crash between publishing
// and recording, or a delete Windows refused because the file was open.
// It holds the store's lock, so no commit runs meanwhile.
func (s *Store) Sweep(ctx context.Context) error {
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
