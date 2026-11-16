package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Deployment statuses (api DeploymentStatus) and origins.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"

	OriginScheduled  = "scheduled"
	OriginBootPrompt = "boot-prompt"
)

// Deployment is a stored deployment. Phase 2 creates and reads them; the
// agent endpoints that drive stage and percent arrive in Phase 4.
type Deployment struct {
	ID              string
	ClientID        string
	ProfileID       string
	ProfileSnapshot json.RawMessage
	Origin          string
	Status          string
	Stage           *string
	Percent         int
	Message         string
	StartedAt       *time.Time
	FinishedAt      *time.Time
	ComputerName    *string
	LastActivityAt  *time.Time // the last agent request (ADR 0026)
	Version         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewDeployment is what CreateDeployment needs.
type NewDeployment struct {
	ClientID        string
	ProfileID       string
	ProfileSnapshot json.RawMessage
	Origin          string
	// Running starts it immediately (boot prompt); otherwise it is armed
	// (pending) for the next network boot.
	Running      bool
	ComputerName *string
	// Secrets are copied into the deployment (ADR 0026).
	Secrets []string
}

const deploymentCols = `id, client_id, profile_id, profile_snapshot, origin, status, stage, percent,
	message, started_at, finished_at, version, created_at, updated_at, computer_name, last_activity_at`

func scanDeployment(r rowScanner) (Deployment, error) {
	var (
		d                Deployment
		snapshot         string
		stage, name      sql.NullString
		started, done    sql.NullInt64
		activity         sql.NullInt64
		created, updated int64
	)
	if err := r.Scan(&d.ID, &d.ClientID, &d.ProfileID, &snapshot, &d.Origin, &d.Status, &stage, &d.Percent,
		&d.Message, &started, &done, &d.Version, &created, &updated, &name, &activity); err != nil {
		return Deployment{}, err
	}
	d.ProfileSnapshot = json.RawMessage(snapshot)
	d.Stage = fromNullStr(stage)
	d.StartedAt, d.FinishedAt = fromNullMS(started), fromNullMS(done)
	d.ComputerName, d.LastActivityAt = fromNullStr(name), fromNullMS(activity)
	d.CreatedAt, d.UpdatedAt = fromMS(created), fromMS(updated)
	return d, nil
}

// CreateDeployment stores a pending (armed) or running deployment.
// ErrNotFound if the client doesn't exist; ErrDeploymentActive if it already
// has a pending or running one (enforced by the deployments_one_active index).
func (s *Store) CreateDeployment(ctx context.Context, in NewDeployment) (Deployment, error) {
	status, started := StatusPending, sql.NullInt64{}
	now := ms(s.now())
	if in.Running {
		status, started = StatusRunning, sql.NullInt64{Int64: now, Valid: true}
	}
	id := newID()
	err := s.tx(ctx, func(t *sql.Tx) error {
		var exists int
		if err := t.QueryRowContext(ctx, `SELECT count(*) FROM clients WHERE id = ?`, in.ClientID).Scan(&exists); err != nil {
			return fmt.Errorf("store: create deployment: %w", err)
		}
		if exists == 0 {
			return ErrNotFound
		}
		_, err := t.ExecContext(ctx, `INSERT INTO deployments
			(id, client_id, profile_id, profile_snapshot, origin, status, started_at, created_at, updated_at,
			 computer_name, last_activity_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.ClientID, in.ProfileID, string(in.ProfileSnapshot), in.Origin, status, started, now, now,
			nullStr(in.ComputerName), started)
		if uniqueViolation(err, "deployments.client_id") {
			return ErrDeploymentActive
		}
		if err != nil {
			return fmt.Errorf("store: create deployment: %w", err)
		}
		for _, sid := range in.Secrets {
			res, err := t.ExecContext(ctx, `INSERT OR IGNORE INTO deployment_secrets
				(deployment_id, secret_id, kind, value_version, key_fingerprint, value)
				SELECT ?, id, kind, value_version, key_fingerprint, value FROM secrets WHERE id = ?`, id, sid)
			if err != nil {
				return fmt.Errorf("store: copy secret %s: %w", sid, err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				var dup int
				t.QueryRowContext(ctx, `SELECT count(*) FROM deployment_secrets WHERE deployment_id = ? AND secret_id = ?`, id, sid).Scan(&dup)
				if dup == 0 {
					return fmt.Errorf("%w: secret %s", ErrNotFound, sid)
				}
			}
		}
		return nil
	})
	if err != nil {
		return Deployment{}, err
	}
	return s.Deployment(ctx, id)
}

// Deployment returns the deployment with id, or ErrNotFound.
func (s *Store) Deployment(ctx context.Context, id string) (Deployment, error) {
	return s.deploymentWhere(ctx, s.r, "id = ?", id)
}

// ActiveDeployment returns the client's pending or running deployment, or
// ErrNotFound. This is the "is it armed?" check on every network boot.
func (s *Store) ActiveDeployment(ctx context.Context, clientID string) (Deployment, error) {
	return s.deploymentWhere(ctx, s.r, "client_id = ? AND status IN ('pending', 'running')", clientID)
}

func (s *Store) deploymentWhere(ctx context.Context, q querier, where string, arg any) (Deployment, error) {
	d, err := scanDeployment(q.QueryRowContext(ctx, `SELECT `+deploymentCols+` FROM deployments WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	if err != nil {
		return Deployment{}, fmt.Errorf("store: read deployment: %w", err)
	}
	return d, nil
}

// StartDeployment moves a pending (armed) deployment to running. This is the
// consume step at agent check-in: only one caller can win it, so an armed
// deployment runs once. ErrBadTransition if it isn't pending any more.
func (s *Store) StartDeployment(ctx context.Context, id string) (Deployment, error) {
	now := ms(s.now())
	return s.transition(ctx, id, []string{StatusPending}, nil,
		`status = 'running', started_at = ?, last_activity_at = ?, updated_at = ?, version = version + 1`, now, now, now)
}

// FinishDeployment moves a deployment to succeeded, failed or cancelled.
// Cancelling and failing work from pending or running; succeeding only from
// running. Finishing an already-finished deployment returns it unchanged if
// the status matches (idempotent), ErrBadTransition otherwise.
func (s *Store) FinishDeployment(ctx context.Context, id, status, message string) (Deployment, error) {
	var from []string
	switch status {
	case StatusSucceeded:
		from = []string{StatusRunning}
	case StatusFailed, StatusCancelled:
		from = []string{StatusPending, StatusRunning}
	default:
		return Deployment{}, fmt.Errorf("store: finish deployment: %q is not a final status", status)
	}
	now := ms(s.now())
	// The secret copies go with the token (ADR 0026).
	dropSecrets := func(t *sql.Tx) error {
		_, err := t.ExecContext(ctx, `DELETE FROM deployment_secrets WHERE deployment_id = ?`, id)
		return err
	}
	d, err := s.transition(ctx, id, from, dropSecrets,
		`status = ?, message = ?, finished_at = ?, updated_at = ?, version = version + 1`, status, message, now, now)
	if errors.Is(err, ErrBadTransition) {
		if cur, gerr := s.Deployment(ctx, id); gerr == nil && cur.Status == status {
			return cur, nil
		}
	}
	return d, err
}

// transition applies set to deployment id if its status is one of from,
// then after (if not nil) in the same transaction.
func (s *Store) transition(ctx context.Context, id string, from []string, after func(*sql.Tx) error, set string, args ...any) (Deployment, error) {
	var d Deployment
	err := s.tx(ctx, func(t *sql.Tx) error {
		cur, err := s.deploymentWhere(ctx, t, "id = ?", id)
		if err != nil {
			return err
		}
		ok := false
		for _, f := range from {
			ok = ok || cur.Status == f
		}
		if !ok {
			return fmt.Errorf("%w: %s is %s", ErrBadTransition, id, cur.Status)
		}
		if _, err := t.ExecContext(ctx, `UPDATE deployments SET `+set+` WHERE id = ?`, append(args, id)...); err != nil {
			return fmt.Errorf("store: update deployment: %w", err)
		}
		if after != nil {
			if err := after(t); err != nil {
				return fmt.Errorf("store: update deployment: %w", err)
			}
		}
		d, err = s.deploymentWhere(ctx, t, "id = ?", id)
		return err
	})
	return d, err
}

// TouchDeployment records an agent request for a running deployment (the
// inactivity timeout, ADR 0026).
func (s *Store) TouchDeployment(ctx context.Context, id string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE deployments SET last_activity_at = ? WHERE id = ? AND status = 'running'`, ms(s.now()), id)
	return err
}

// DueDeployments returns the pending deployments created before
// armedBefore and the running ones whose last agent request (or start) was
// before idleBefore.
func (s *Store) DueDeployments(ctx context.Context, armedBefore, idleBefore time.Time) (expired, idle []string, err error) {
	rows, err := s.r.QueryContext(ctx, `SELECT id, status FROM deployments WHERE
		(status = 'pending' AND created_at < ?) OR
		(status = 'running' AND coalesce(last_activity_at, started_at) < ?) ORDER BY id`, ms(armedBefore), ms(idleBefore))
	if err != nil {
		return nil, nil, fmt.Errorf("store: due deployments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, nil, err
		}
		if status == StatusPending {
			expired = append(expired, id)
		} else {
			idle = append(idle, id)
		}
	}
	return expired, idle, rows.Err()
}

// DeploymentSecret is a secret's sealed value as copied into a deployment.
type DeploymentSecret struct {
	SecretID       string
	Kind           string
	ValueVersion   int64
	KeyFingerprint string
	Value          []byte
}

// DeploymentSecrets returns a deployment's secret copies (none once it has
// finished).
func (s *Store) DeploymentSecrets(ctx context.Context, id string) ([]DeploymentSecret, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT secret_id, kind, value_version, key_fingerprint, value
		FROM deployment_secrets WHERE deployment_id = ? ORDER BY secret_id`, id)
	if err != nil {
		return nil, fmt.Errorf("store: deployment secrets: %w", err)
	}
	defer rows.Close()
	var out []DeploymentSecret
	for rows.Next() {
		var x DeploymentSecret
		if err := rows.Scan(&x.SecretID, &x.Kind, &x.ValueVersion, &x.KeyFingerprint, &x.Value); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
