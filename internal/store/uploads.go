package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrUploadIncomplete: the upload hasn't received all its bytes.
	ErrUploadIncomplete = errors.New("store: upload incomplete")
	// ErrUploadInUse: a queued or running job is using the upload.
	ErrUploadInUse = errors.New("store: upload in use by an import")
)

// Upload is an uploads row (ADR 0017).
type Upload struct {
	ID        string
	Length    int64
	Offset    int64
	Filename  *string
	Filetype  *string
	HashState []byte
	SHA256    *string // set once complete
	Imported  bool
	JobID     *string // most recent import job
	InUse     bool    // that job is queued or running
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt *time.Time // nil: kept until deleted (imported)
}

// Complete reports whether every byte has arrived.
func (u Upload) Complete() bool { return u.SHA256 != nil }

// NewUpload is an upload to create.
type NewUpload struct {
	Length    int64
	Filename  *string
	Filetype  *string
	HashState []byte // state of an empty hash
	ExpiresAt time.Time
}

const uploadCols = `u.id, u.length, u.received, u.filename, u.filetype, u.hash_state, u.sha256, u.imported, u.job_id,
	coalesce(j.status IN ('queued', 'running'), 0), u.created_at, u.updated_at, u.expires_at`

const uploadFrom = ` FROM uploads u LEFT JOIN jobs j ON j.id = u.job_id`

func scanUpload(r rowScanner) (Upload, error) {
	var u Upload
	var name, typ, sha, job sql.NullString
	var imported, inUse int
	var created, updated int64
	var expires sql.NullInt64
	if err := r.Scan(&u.ID, &u.Length, &u.Offset, &name, &typ, &u.HashState, &sha, &imported, &job, &inUse, &created, &updated, &expires); err != nil {
		return Upload{}, err
	}
	u.Filename, u.Filetype, u.SHA256, u.JobID = fromNullStr(name), fromNullStr(typ), fromNullStr(sha), fromNullStr(job)
	u.Imported, u.InUse = imported == 1, inUse == 1
	u.CreatedAt, u.UpdatedAt, u.ExpiresAt = fromMS(created), fromMS(updated), fromNullMS(expires)
	return u, nil
}

// CreateUpload records a new, empty upload.
func (s *Store) CreateUpload(ctx context.Context, n NewUpload) (Upload, error) {
	id := newID()
	now := ms(s.now())
	_, err := s.w.ExecContext(ctx, `INSERT INTO uploads (id, length, filename, filetype, hash_state, created_at, updated_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, n.Length, nullStr(n.Filename), nullStr(n.Filetype), n.HashState, now, now, ms(n.ExpiresAt))
	if err != nil {
		return Upload{}, fmt.Errorf("store: create upload: %w", err)
	}
	return s.Upload(ctx, id)
}

// Upload reads one upload. ErrNotFound.
func (s *Store) Upload(ctx context.Context, id string) (Upload, error) {
	return s.uploadWhere(ctx, s.r, id)
}

func (s *Store) uploadWhere(ctx context.Context, q querier, id string) (Upload, error) {
	u, err := scanUpload(q.QueryRowContext(ctx, `SELECT `+uploadCols+uploadFrom+` WHERE u.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, ErrNotFound
	}
	if err != nil {
		return Upload{}, fmt.Errorf("store: read upload: %w", err)
	}
	return u, nil
}

// ListUploads returns uploads oldest first and the next cursor (empty on
// the last page). limit 1..200; zero means 50.
func (s *Store) ListUploads(ctx context.Context, limit int, after string) ([]Upload, string, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	rows, err := s.r.QueryContext(ctx, `SELECT `+uploadCols+uploadFrom+` WHERE u.id > ? ORDER BY u.id LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("store: list uploads: %w", err)
	}
	defer rows.Close()
	var out []Upload
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			return nil, "", fmt.Errorf("store: list uploads: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("store: list uploads: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// UploadCheckpoint is an incomplete upload's progress.
type UploadCheckpoint struct {
	Offset    int64
	HashState []byte
	SHA256    *string // set when Offset reaches the length
	ExpiresAt time.Time
}

// CheckpointUpload records progress on an incomplete upload; the offset
// may not go backwards. ErrNotFound, ErrBadTransition (already complete or
// an older offset).
func (s *Store) CheckpointUpload(ctx context.Context, id string, c UploadCheckpoint) error {
	res, err := s.w.ExecContext(ctx, `UPDATE uploads SET received = ?, hash_state = ?, sha256 = ?, updated_at = ?, expires_at = ?
		WHERE id = ? AND sha256 IS NULL AND received <= ?`,
		c.Offset, c.HashState, nullStr(c.SHA256), ms(s.now()), ms(c.ExpiresAt), id, c.Offset)
	if err != nil {
		return fmt.Errorf("store: checkpoint upload: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Upload(ctx, id); err != nil {
			return err
		}
		return ErrBadTransition
	}
	return nil
}

// DeleteUpload deletes the row. ErrNotFound, ErrUploadInUse.
func (s *Store) DeleteUpload(ctx context.Context, id string) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		u, err := s.uploadWhere(ctx, t, id)
		if err != nil {
			return err
		}
		if u.InUse {
			return ErrUploadInUse
		}
		if _, err := t.ExecContext(ctx, `DELETE FROM uploads WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: delete upload: %w", err)
		}
		return nil
	})
}

// ExpireUploads deletes uploads whose expiry has passed, except those in
// use, and returns their ids so their files can be removed.
func (s *Store) ExpireUploads(ctx context.Context, now time.Time) ([]string, error) {
	var ids []string
	err := s.tx(ctx, func(t *sql.Tx) error {
		rows, err := t.QueryContext(ctx, `DELETE FROM uploads WHERE expires_at < ?
			AND NOT EXISTS (SELECT 1 FROM jobs j WHERE j.id = uploads.job_id AND j.status IN ('queued', 'running'))
			RETURNING id`, ms(now))
		if err != nil {
			return fmt.Errorf("store: expire uploads: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("store: expire uploads: %w", err)
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}

// UploadBytes is the bytes held by uploads (received so far) and the bytes
// still to come for incomplete ones, excluding the upload exceptID.
func (s *Store) UploadBytes(ctx context.Context, exceptID string) (held, pending int64, err error) {
	err = s.r.QueryRowContext(ctx, `SELECT coalesce(sum(received), 0), coalesce(sum(length - received), 0) FROM uploads WHERE id <> ?`,
		exceptID).Scan(&held, &pending)
	if err != nil {
		return 0, 0, fmt.Errorf("store: upload bytes: %w", err)
	}
	return held, pending, nil
}

// claimUpload marks a complete upload as used by jobID, inside the
// transaction that queues the job. ErrNotFound, ErrUploadIncomplete,
// ErrUploadInUse.
func (s *Store) claimUpload(ctx context.Context, t *sql.Tx, uploadID, jobID string) error {
	u, err := s.uploadWhere(ctx, t, uploadID)
	if err != nil {
		return err
	}
	if !u.Complete() {
		return ErrUploadIncomplete
	}
	if u.InUse {
		return ErrUploadInUse
	}
	if _, err := t.ExecContext(ctx, `UPDATE uploads SET job_id = ?, updated_at = ? WHERE id = ?`, jobID, ms(s.now()), uploadID); err != nil {
		return fmt.Errorf("store: claim upload: %w", err)
	}
	return nil
}

// CreateJobForUpload queues a job that uses an upload, claiming it in the
// same transaction. Imports that also create a resource do the same inside
// their own transaction.
func (s *Store) CreateJobForUpload(ctx context.Context, uploadID string, n NewJob) (Job, error) {
	var id string
	err := s.tx(ctx, func(t *sql.Tx) error {
		var err error
		if id, err = s.insertJob(ctx, t, n); err != nil {
			return err
		}
		return s.claimUpload(ctx, t, uploadID, id)
	})
	if err != nil {
		return Job{}, err
	}
	return s.Job(ctx, id)
}

// ReleaseUpload records how jobID's use of the upload ended: success keeps
// it until deleted; failure sets its expiry (unless an earlier import
// already succeeded, which keeps it regardless). No-op if another job has
// claimed it since. ErrNotFound.
func (s *Store) ReleaseUpload(ctx context.Context, uploadID, jobID string, succeeded bool, expiresAt time.Time) (Upload, error) {
	now := ms(s.now())
	var err error
	if succeeded {
		_, err = s.w.ExecContext(ctx, `UPDATE uploads SET imported = 1, expires_at = NULL, updated_at = ? WHERE id = ? AND job_id = ?`,
			now, uploadID, jobID)
	} else {
		_, err = s.w.ExecContext(ctx, `UPDATE uploads SET expires_at = ?, updated_at = ? WHERE id = ? AND job_id = ? AND imported = 0`,
			ms(expiresAt), now, uploadID, jobID)
	}
	if err != nil {
		return Upload{}, fmt.Errorf("store: release upload: %w", err)
	}
	return s.Upload(ctx, uploadID)
}
