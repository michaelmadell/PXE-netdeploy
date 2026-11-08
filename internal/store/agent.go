package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Agent token kinds (ADR 0025).
const (
	TokenSession    = "session"
	TokenDeployment = "deployment"
)

// ErrTokenInvalid: unknown, expired, already used, or for a deployment that
// isn't running.
var ErrTokenInvalid = errors.New("store: agent token invalid")

// AgentToken is an agent_tokens row.
type AgentToken struct {
	Hash         string
	Kind         string
	ClientID     string
	DeploymentID *string
	ExpiresAt    time.Time
}

// CreateAgentToken stores a token by its hash.
func (s *Store) CreateAgentToken(ctx context.Context, t AgentToken) error {
	_, err := s.w.ExecContext(ctx, `INSERT INTO agent_tokens (hash, kind, client_id, deployment_id, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, t.Hash, t.Kind, t.ClientID, nullStr(t.DeploymentID), ms(t.ExpiresAt), ms(s.now()))
	if err != nil {
		return fmt.Errorf("store: create agent token: %w", err)
	}
	return nil
}

// UseSessionToken consumes a session token (single use). ErrTokenInvalid.
func (s *Store) UseSessionToken(ctx context.Context, hash string) (AgentToken, error) {
	var t AgentToken
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var exp int64
		err := tx.QueryRowContext(ctx, `SELECT client_id, expires_at FROM agent_tokens
			WHERE hash = ? AND kind = 'session' AND used_at IS NULL`, hash).Scan(&t.ClientID, &exp)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && exp <= ms(s.now())) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		t.Hash, t.Kind, t.ExpiresAt = hash, TokenSession, fromMS(exp)
		_, err = tx.ExecContext(ctx, `UPDATE agent_tokens SET used_at = ? WHERE hash = ?`, ms(s.now()), hash)
		return err
	})
	return t, err
}

// DeploymentToken returns a valid deployment token's row: unexpired, and its
// deployment running. ErrTokenInvalid.
func (s *Store) DeploymentToken(ctx context.Context, hash string) (AgentToken, error) {
	var t AgentToken
	var dep string
	var exp int64
	err := s.r.QueryRowContext(ctx, `SELECT t.client_id, t.deployment_id, t.expires_at FROM agent_tokens t
		JOIN deployments d ON d.id = t.deployment_id
		WHERE t.hash = ? AND t.kind = 'deployment' AND d.status = 'running'`, hash).Scan(&t.ClientID, &dep, &exp)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && exp <= ms(s.now())) {
		return AgentToken{}, ErrTokenInvalid
	}
	if err != nil {
		return AgentToken{}, fmt.Errorf("store: read agent token: %w", err)
	}
	t.Hash, t.Kind, t.DeploymentID, t.ExpiresAt = hash, TokenDeployment, &dep, fromMS(exp)
	return t, nil
}

// PurgeAgentTokens deletes tokens that expired before cutoff.
func (s *Store) PurgeAgentTokens(ctx context.Context, cutoff time.Time) error {
	_, err := s.w.ExecContext(ctx, `DELETE FROM agent_tokens WHERE expires_at < ?`, ms(cutoff))
	return err
}

// SetDeploymentPlan records a running deployment's plan and the files it
// may download.
func (s *Store) SetDeploymentPlan(ctx context.Context, id string, plan json.RawMessage, files []string) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx, `UPDATE deployments SET plan = ? WHERE id = ?`, string(plan), id); err != nil {
			return fmt.Errorf("store: set deployment plan: %w", err)
		}
		if _, err := t.ExecContext(ctx, `DELETE FROM deployment_files WHERE deployment_id = ?`, id); err != nil {
			return err
		}
		for _, f := range files {
			if _, err := t.ExecContext(ctx, `INSERT OR IGNORE INTO deployment_files (deployment_id, sha256) VALUES (?, ?)`, id, f); err != nil {
				return fmt.Errorf("store: deployment file: %w", err)
			}
		}
		return nil
	})
}

// DeploymentPlan returns a deployment's plan (nil if none yet).
func (s *Store) DeploymentPlan(ctx context.Context, id string) (json.RawMessage, error) {
	var plan sql.NullString
	err := s.r.QueryRowContext(ctx, `SELECT plan FROM deployments WHERE id = ?`, id).Scan(&plan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil || !plan.Valid {
		return nil, err
	}
	return json.RawMessage(plan.String), nil
}

// DeploymentFileAllowed reports whether a running deployment's plan lists
// the blob.
func (s *Store) DeploymentFileAllowed(ctx context.Context, deploymentID, sha256 string) (bool, error) {
	var one int
	err := s.r.QueryRowContext(ctx, `SELECT 1 FROM deployment_files f JOIN deployments d ON d.id = f.deployment_id
		WHERE f.deployment_id = ? AND f.sha256 = ? AND d.status = 'running'`, deploymentID, sha256).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// stageOrder ranks DeploymentStage values: progress only moves forward.
var stageOrder = map[string]int{"checked-in": 1, "partitioning": 2, "applying-image": 3, "injecting-drivers": 4,
	"configuring": 5, "finalizing": 6, "rebooting": 7}

// ReportDeploymentProgress records a running deployment's stage, percent and
// message. A report for an earlier stage than the current one is ignored.
// ErrBadTransition if the deployment isn't running.
func (s *Store) ReportDeploymentProgress(ctx context.Context, id, stage string, percent int, message string) error {
	rank, ok := stageOrder[stage]
	if !ok {
		return fmt.Errorf("store: unknown stage %q", stage)
	}
	return s.tx(ctx, func(t *sql.Tx) error {
		var status string
		var cur sql.NullString
		if err := t.QueryRowContext(ctx, `SELECT status, stage FROM deployments WHERE id = ?`, id).Scan(&status, &cur); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status != StatusRunning {
			return ErrBadTransition
		}
		if cur.Valid && stageOrder[cur.String] > rank {
			return nil
		}
		_, err := t.ExecContext(ctx, `UPDATE deployments SET stage = ?, percent = ?, message = ?, updated_at = ? WHERE id = ?`,
			stage, max(0, min(100, percent)), message, ms(s.now()), id)
		return err
	})
}

// LogEntry is a deployment log line.
type LogEntry struct {
	At      time.Time
	Level   string
	Message string
	Source  *string
}

// AppendDeploymentLogs stores log entries after the deployment's last.
func (s *Store) AppendDeploymentLogs(ctx context.Context, id string, entries []LogEntry) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		var seq int64
		if err := t.QueryRowContext(ctx, `SELECT coalesce(max(seq), 0) FROM deployment_logs WHERE deployment_id = ?`, id).Scan(&seq); err != nil {
			return err
		}
		for _, e := range entries {
			seq++
			if _, err := t.ExecContext(ctx, `INSERT INTO deployment_logs (deployment_id, seq, at, level, message, source) VALUES (?, ?, ?, ?, ?, ?)`,
				id, seq, ms(e.At), e.Level, e.Message, nullStr(e.Source)); err != nil {
				return fmt.Errorf("store: append deployment log: %w", err)
			}
		}
		return nil
	})
}

// DeploymentLogs returns up to limit entries after seq after, and the last
// seq returned (the next cursor) when there may be more.
func (s *Store) DeploymentLogs(ctx context.Context, id string, after int64, limit int) ([]LogEntry, int64, error) {
	if limit <= 0 {
		limit = 200
	}
	limit = min(limit, 1000)
	rows, err := s.r.QueryContext(ctx, `SELECT seq, at, level, message, source FROM deployment_logs
		WHERE deployment_id = ? AND seq > ? ORDER BY seq LIMIT ?`, id, after, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("store: deployment logs: %w", err)
	}
	defer rows.Close()
	var out []LogEntry
	var last int64
	for rows.Next() {
		var e LogEntry
		var at int64
		var src sql.NullString
		if err := rows.Scan(&last, &at, &e.Level, &e.Message, &src); err != nil {
			return nil, 0, err
		}
		e.At, e.Source = fromMS(at), fromNullStr(src)
		out = append(out, e)
	}
	if len(out) < limit {
		last = 0
	}
	return out, last, rows.Err()
}

// ListDeployments returns deployments newest first, optionally for one
// client, and the next cursor.
func (s *Store) ListDeployments(ctx context.Context, clientID string, limit int, before string) ([]Deployment, string, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	q := `SELECT ` + deploymentCols + ` FROM deployments WHERE (? = '' OR client_id = ?) AND (? = '' OR id < ?) ORDER BY id DESC LIMIT ?`
	rows, err := s.r.QueryContext(ctx, q, clientID, clientID, before, before, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("store: list deployments: %w", err)
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, d)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, rows.Err()
}

// AgentTokenAny returns an unexpired token whatever its deployment's status
// (complete is idempotent). ErrTokenInvalid.
func (s *Store) AgentTokenAny(ctx context.Context, hash string) (AgentToken, error) {
	var t AgentToken
	var dep sql.NullString
	var exp int64
	err := s.r.QueryRowContext(ctx, `SELECT kind, client_id, deployment_id, expires_at FROM agent_tokens WHERE hash = ?`, hash).
		Scan(&t.Kind, &t.ClientID, &dep, &exp)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && exp <= ms(s.now())) {
		return AgentToken{}, ErrTokenInvalid
	}
	if err != nil {
		return AgentToken{}, fmt.Errorf("store: read agent token: %w", err)
	}
	t.Hash, t.DeploymentID, t.ExpiresAt = hash, fromNullStr(dep), fromMS(exp)
	return t, nil
}
