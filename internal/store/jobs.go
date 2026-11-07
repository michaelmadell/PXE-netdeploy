package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Job statuses (api JobStatus).
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// ErrResourceBusy: the resource already has a queued or running job.
var ErrResourceBusy = errors.New("store: resource already has an active job")

// Job is a jobs row (ADR 0016).
type Job struct {
	ID              string
	Kind            string
	Status          string
	ResourceID      *string
	Params          json.RawMessage
	Percent         int
	Message         string
	Error           json.RawMessage // Problem JSON; nil unless failed
	CancelRequested bool
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

// Finished reports a terminal status.
func (j Job) Finished() bool {
	return j.Status == JobSucceeded || j.Status == JobFailed || j.Status == JobCancelled
}

// NewJob is a job to queue.
type NewJob struct {
	Kind       string
	ResourceID *string
	Params     any // marshalled to a JSON object; nil means {}
}

const jobCols = `id, kind, status, resource_id, params, percent, message, error, cancel_requested, created_at, started_at, finished_at`

func scanJob(r rowScanner) (Job, error) {
	var j Job
	var res, errDoc sql.NullString
	var params string
	var created int64
	var started, finished sql.NullInt64
	var cancel int
	if err := r.Scan(&j.ID, &j.Kind, &j.Status, &res, &params, &j.Percent, &j.Message, &errDoc, &cancel, &created, &started, &finished); err != nil {
		return Job{}, err
	}
	j.ResourceID = fromNullStr(res)
	j.Params = json.RawMessage(params)
	if errDoc.Valid {
		j.Error = json.RawMessage(errDoc.String)
	}
	j.CancelRequested = cancel == 1
	j.CreatedAt = fromMS(created)
	j.StartedAt = fromNullMS(started)
	j.FinishedAt = fromNullMS(finished)
	return j, nil
}

// CreateJob queues a job on its own. Resource-creating operations insert
// their job in the same transaction with insertJob instead.
func (s *Store) CreateJob(ctx context.Context, n NewJob) (Job, error) {
	var id string
	err := s.tx(ctx, func(t *sql.Tx) error {
		var err error
		id, err = s.insertJob(ctx, t, n)
		return err
	})
	if err != nil {
		return Job{}, err
	}
	return s.Job(ctx, id)
}

func (s *Store) insertJob(ctx context.Context, t *sql.Tx, n NewJob) (string, error) {
	params := []byte("{}")
	if n.Params != nil {
		var err error
		if params, err = json.Marshal(n.Params); err != nil {
			return "", fmt.Errorf("store: job params: %w", err)
		}
	}
	id := newID()
	_, err := t.ExecContext(ctx, `INSERT INTO jobs (id, kind, status, resource_id, params, created_at) VALUES (?, ?, 'queued', ?, ?, ?)`,
		id, n.Kind, nullStr(n.ResourceID), string(params), ms(s.now()))
	if uniqueViolation(err, "jobs.resource_id") {
		return "", ErrResourceBusy
	}
	if err != nil {
		return "", fmt.Errorf("store: insert job: %w", err)
	}
	return id, nil
}

// Job reads one job. ErrNotFound.
func (s *Store) Job(ctx context.Context, id string) (Job, error) {
	j, err := scanJob(s.r.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("store: read job: %w", err)
	}
	return j, nil
}

// JobFilter narrows ListJobs. Zero values don't filter.
type JobFilter struct {
	Status     string
	Kind       string
	ResourceID string
	Limit      int    // 1..200; zero means 50
	After      string // cursor: the last ID of the previous page
}

// ListJobs returns jobs oldest first and the next cursor (empty on the last
// page).
func (s *Store) ListJobs(ctx context.Context, f JobFilter) ([]Job, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	where, args := []string{"id > ?"}, []any{f.After}
	if f.Status != "" {
		where, args = append(where, "status = ?"), append(args, f.Status)
	}
	if f.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, f.Kind)
	}
	if f.ResourceID != "" {
		where, args = append(where, "resource_id = ?"), append(args, f.ResourceID)
	}
	args = append(args, limit+1)
	out, err := s.jobs(ctx, `SELECT `+jobCols+` FROM jobs WHERE `+strings.Join(where, " AND ")+` ORDER BY id LIMIT ?`, args...)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// QueuedJobs returns up to limit queued jobs of the given kinds, oldest
// first (the runner's queue).
func (s *Store) QueuedJobs(ctx context.Context, kinds []string, limit int) ([]Job, error) {
	if len(kinds) == 0 || limit <= 0 {
		return nil, nil
	}
	args := make([]any, 0, len(kinds)+1)
	for _, k := range kinds {
		args = append(args, k)
	}
	args = append(args, limit)
	return s.jobs(ctx, `SELECT `+jobCols+` FROM jobs WHERE status = 'queued' AND kind IN (?`+
		strings.Repeat(", ?", len(kinds)-1)+`) ORDER BY id LIMIT ?`, args...)
}

func (s *Store) jobs(ctx context.Context, q string, args ...any) ([]Job, error) {
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list jobs: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// StartJob moves a queued job to running. ErrBadTransition if it isn't
// queued any more (e.g. cancelled meanwhile).
func (s *Store) StartJob(ctx context.Context, id string) (Job, error) {
	return s.jobUpdate(ctx, id, `UPDATE jobs SET status = 'running', started_at = ? WHERE id = ? AND status = 'queued'`, ms(s.now()), id)
}

// UpdateJobProgress records a running job's progress. ErrBadTransition if
// it isn't running.
func (s *Store) UpdateJobProgress(ctx context.Context, id string, percent int, message string) error {
	percent = max(0, min(100, percent))
	_, err := s.jobUpdate(ctx, id, `UPDATE jobs SET percent = ?, message = ? WHERE id = ? AND status = 'running'`, percent, message, id)
	return err
}

// JobOutcome is how a running job ended.
type JobOutcome struct {
	Status     string          // JobSucceeded, JobFailed or JobCancelled
	ResourceID *string         // replaces the resource id if set (deduplicated imports)
	Error      json.RawMessage // Problem JSON, required for JobFailed
	Percent    int
	Message    string
}

// FinishJob ends a running job. ErrBadTransition if it isn't running.
func (s *Store) FinishJob(ctx context.Context, id string, o JobOutcome) (Job, error) {
	var errDoc sql.NullString
	if o.Error != nil {
		errDoc = sql.NullString{String: string(o.Error), Valid: true}
	}
	if o.Status == JobSucceeded {
		o.Percent = 100
	}
	return s.jobUpdate(ctx, id, `UPDATE jobs SET status = ?, resource_id = coalesce(?, resource_id), error = ?,
		percent = ?, message = ?, finished_at = ? WHERE id = ? AND status = 'running'`,
		o.Status, nullStr(o.ResourceID), errDoc, max(0, min(100, o.Percent)), o.Message, ms(s.now()), id)
}

// RequestJobCancel cancels a queued job at once, or flags a running one
// (cancel_requested). Finished jobs are returned unchanged. prev is the
// status before the call. ErrNotFound.
func (s *Store) RequestJobCancel(ctx context.Context, id string) (j Job, prev string, err error) {
	err = s.tx(ctx, func(t *sql.Tx) error {
		if err := t.QueryRowContext(ctx, `SELECT status FROM jobs WHERE id = ?`, id).Scan(&prev); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("store: cancel job: %w", err)
		}
		var q string
		var args []any
		switch prev {
		case JobQueued:
			q, args = `UPDATE jobs SET status = 'cancelled', finished_at = ? WHERE id = ?`, []any{ms(s.now()), id}
		case JobRunning:
			q, args = `UPDATE jobs SET cancel_requested = 1 WHERE id = ?`, []any{id}
		default:
			return nil
		}
		if _, err := t.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("store: cancel job: %w", err)
		}
		return nil
	})
	if err != nil {
		return Job{}, "", err
	}
	j, err = s.Job(ctx, id)
	return j, prev, err
}

// InterruptRunningJobs fails every running job with errDoc (startup after a
// crash, or shutdown) and returns them.
func (s *Store) InterruptRunningJobs(ctx context.Context, errDoc json.RawMessage) ([]Job, error) {
	var ids []string
	err := s.tx(ctx, func(t *sql.Tx) error {
		rows, err := t.QueryContext(ctx, `UPDATE jobs SET status = 'failed', error = ?, finished_at = ?
			WHERE status = 'running' RETURNING id`, string(errDoc), ms(s.now()))
		if err != nil {
			return fmt.Errorf("store: interrupt jobs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("store: interrupt jobs: %w", err)
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(ids))
	for _, id := range ids {
		j, err := s.Job(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// PurgeJobs deletes jobs that finished before the cutoff.
func (s *Store) PurgeJobs(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM jobs WHERE finished_at IS NOT NULL AND finished_at < ?`, ms(before))
	if err != nil {
		return 0, fmt.Errorf("store: purge jobs: %w", err)
	}
	return res.RowsAffected()
}

// jobUpdate runs a conditional UPDATE and returns the job. No row changed
// means ErrNotFound (no such job) or ErrBadTransition (wrong status).
func (s *Store) jobUpdate(ctx context.Context, id, q string, args ...any) (Job, error) {
	res, err := s.w.ExecContext(ctx, q, args...)
	if err != nil {
		return Job{}, fmt.Errorf("store: update job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Job(ctx, id); err != nil {
			return Job{}, err
		}
		return Job{}, ErrBadTransition
	}
	return s.Job(ctx, id)
}
