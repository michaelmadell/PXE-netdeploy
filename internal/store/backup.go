package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion is the newest schema this binary knows (the number of
// embedded migrations).
func SchemaVersion() int {
	ms, err := loadMigrations(migrationFiles)
	if err != nil {
		return 0
	}
	return len(ms)
}

// Snapshot writes a consistent copy of the database to path (VACUUM INTO;
// ADR 0020). path must not exist.
func (s *Store) Snapshot(ctx context.Context, path string) error {
	if _, err := s.w.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("store: snapshot: %w", err)
	}
	return nil
}

// BlobRef is a stored blob.
type BlobRef struct {
	SHA256 string
	Size   int64
}

// SnapshotContents reads a database file's schema version and blob list
// without migrating or changing it.
func SnapshotContents(ctx context.Context, path string) (schema int, blobs []BlobRef, err error) {
	if _, err := os.Stat(path); err != nil {
		return 0, nil, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return 0, nil, err
	}
	defer db.Close()
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schema); err != nil {
		return 0, nil, fmt.Errorf("store: read snapshot: %w", err)
	}
	if schema < 2 { // no content store yet
		return schema, nil, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT sha256, size FROM blobs ORDER BY sha256`)
	if err != nil {
		return 0, nil, fmt.Errorf("store: read snapshot blobs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var b BlobRef
		if err := rows.Scan(&b.SHA256, &b.Size); err != nil {
			return 0, nil, err
		}
		blobs = append(blobs, b)
	}
	return schema, blobs, rows.Err()
}

// ActiveJob returns the queued or running job for a resource. ErrNotFound.
func (s *Store) ActiveJob(ctx context.Context, resourceID string) (Job, error) {
	var id string
	err := s.r.QueryRowContext(ctx, `SELECT id FROM jobs WHERE resource_id = ? AND status IN ('queued', 'running')`, resourceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("store: active job: %w", err)
	}
	return s.Job(ctx, id)
}

// ClearUploads deletes every upload row (restore: their files aren't in a
// backup).
func (s *Store) ClearUploads(ctx context.Context) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM uploads`)
	return err
}

// PrepareSnapshot edits a snapshot file before it is stored (ADR 0020):
// jobs of kind that are queued or running (the backup taking it) become
// succeeded with message, and each state entry is written. Without this a
// restored server would find its backup "interrupted".
func PrepareSnapshot(ctx context.Context, path, kind, message string, at time.Time, state map[string]any) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	t, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer t.Rollback()
	if _, err := t.ExecContext(ctx, `UPDATE jobs SET status = 'succeeded', percent = 100, message = ?,
		started_at = coalesce(started_at, ?), finished_at = ? WHERE kind = ? AND status IN ('queued', 'running')`,
		message, ms(at), ms(at), kind); err != nil {
		return fmt.Errorf("store: prepare snapshot: %w", err)
	}
	for k, v := range state {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := t.ExecContext(ctx, `INSERT INTO state (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, k, string(b), ms(at)); err != nil {
			return fmt.Errorf("store: prepare snapshot: %w", err)
		}
	}
	return t.Commit()
}
