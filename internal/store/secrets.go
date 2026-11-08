package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrSecretInUse: something references the secret (ADR 0021).
var ErrSecretInUse = errors.New("store: secret is in use")

// Secret is a secrets row without its value.
type Secret struct {
	ID             string
	Name           string
	Description    string
	Kind           string
	Hint           *string
	ValueVersion   int64
	ValueUpdatedAt time.Time
	KeyFingerprint string
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// SealFunc encrypts a value for a secret id and value version (the
// associated data binds them, so the store supplies them).
type SealFunc func(id string, valueVersion int64) (sealed []byte, err error)

// NewSecret is a secret to create.
type NewSecret struct {
	Name, Description, Kind string
	Hint                    *string
	KeyFingerprint          string
	Seal                    SealFunc
}

const secretCols = `id, name, description, kind, hint, value_version, value_updated_at, key_fingerprint, version, created_at, updated_at`

func scanSecret(r rowScanner, extra ...any) (Secret, error) {
	var x Secret
	var hint sql.NullString
	var vu, c, u int64
	dst := append([]any{&x.ID, &x.Name, &x.Description, &x.Kind, &hint, &x.ValueVersion, &vu, &x.KeyFingerprint, &x.Version, &c, &u}, extra...)
	if err := r.Scan(dst...); err != nil {
		return Secret{}, err
	}
	x.Hint, x.ValueUpdatedAt, x.CreatedAt, x.UpdatedAt = fromNullStr(hint), fromMS(vu), fromMS(c), fromMS(u)
	return x, nil
}

// CreateSecret stores a new secret, sealing its value with n.Seal.
func (s *Store) CreateSecret(ctx context.Context, n NewSecret) (Secret, error) {
	id := newID()
	sealed, err := n.Seal(id, 1)
	if err != nil {
		return Secret{}, err
	}
	now := ms(s.now())
	if _, err := s.w.ExecContext(ctx, `INSERT INTO secrets (id, name, description, kind, hint, value, value_version, value_updated_at, key_fingerprint, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`, id, n.Name, n.Description, n.Kind, nullStr(n.Hint), sealed, now, n.KeyFingerprint, now, now); err != nil {
		return Secret{}, fmt.Errorf("store: create secret: %w", err)
	}
	return s.Secret(ctx, id)
}

// Secret reads a secret's metadata. ErrNotFound.
func (s *Store) Secret(ctx context.Context, id string) (Secret, error) {
	x, err := scanSecret(s.r.QueryRowContext(ctx, `SELECT `+secretCols+` FROM secrets WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, ErrNotFound
	}
	if err != nil {
		return Secret{}, fmt.Errorf("store: read secret: %w", err)
	}
	return x, nil
}

// SecretValue reads a secret with its sealed value. ErrNotFound.
func (s *Store) SecretValue(ctx context.Context, id string) (Secret, []byte, error) {
	var sealed []byte
	x, err := scanSecret(s.r.QueryRowContext(ctx, `SELECT `+secretCols+`, value FROM secrets WHERE id = ?`, id), &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return Secret{}, nil, ErrNotFound
	}
	if err != nil {
		return Secret{}, nil, fmt.Errorf("store: read secret: %w", err)
	}
	return x, sealed, nil
}

// ListSecrets returns secrets (optionally of one kind) oldest first and the
// next cursor.
func (s *Store) ListSecrets(ctx context.Context, kind string, limit int, after string) ([]Secret, string, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	rows, err := s.r.QueryContext(ctx, `SELECT `+secretCols+` FROM secrets WHERE id > ? AND (? = '' OR kind = ?) ORDER BY id LIMIT ?`,
		after, kind, kind, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("store: list secrets: %w", err)
	}
	defer rows.Close()
	var out []Secret
	for rows.Next() {
		x, err := scanSecret(rows)
		if err != nil {
			return nil, "", fmt.Errorf("store: list secrets: %w", err)
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("store: list secrets: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// UpdateSecretMetadata changes name and/or description if the version
// matches. ErrNotFound, ErrVersionMismatch.
func (s *Store) UpdateSecretMetadata(ctx context.Context, id string, name, description *string, ifVersion int64) (Secret, error) {
	res, err := s.w.ExecContext(ctx, `UPDATE secrets SET name = coalesce(?, name), description = coalesce(?, description), version = version + 1, updated_at = ?
		WHERE id = ? AND version = ?`, nullStr(name), nullStr(description), ms(s.now()), id, ifVersion)
	if err != nil {
		return Secret{}, fmt.Errorf("store: update secret: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Secret(ctx, id); err != nil {
			return Secret{}, err
		}
		return Secret{}, ErrVersionMismatch
	}
	return s.Secret(ctx, id)
}

// ReplaceSecretValue stores a new value (value_version + 1) if the version
// matches. ErrNotFound, ErrVersionMismatch.
func (s *Store) ReplaceSecretValue(ctx context.Context, id string, hint *string, keyFingerprint string, seal SealFunc, ifVersion int64) (Secret, error) {
	err := s.tx(ctx, func(t *sql.Tx) error {
		var v, vv int64
		if err := t.QueryRowContext(ctx, `SELECT version, value_version FROM secrets WHERE id = ?`, id).Scan(&v, &vv); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("store: replace secret value: %w", err)
		}
		if v != ifVersion {
			return ErrVersionMismatch
		}
		sealed, err := seal(id, vv+1)
		if err != nil {
			return err
		}
		now := ms(s.now())
		_, err = t.ExecContext(ctx, `UPDATE secrets SET value = ?, value_version = ?, value_updated_at = ?, hint = ?, key_fingerprint = ?,
			version = version + 1, updated_at = ? WHERE id = ?`, sealed, vv+1, now, nullStr(hint), keyFingerprint, now, id)
		return err
	})
	if err != nil {
		return Secret{}, err
	}
	return s.Secret(ctx, id)
}

// DeleteSecret deletes a secret nothing references. ErrNotFound,
// ErrSecretInUse (with what uses it).
func (s *Store) DeleteSecret(ctx context.Context, id string) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		var one int
		if err := t.QueryRowContext(ctx, `SELECT 1 FROM secrets WHERE id = ?`, id).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("store: delete secret: %w", err)
		}
		var n int
		t.QueryRowContext(ctx, `SELECT count(*) FROM settings WHERE json_extract(doc, '$.backup.destination.credentialSecretId') = ?`, id).Scan(&n)
		if n > 0 {
			return fmt.Errorf("%w: the backup destination uses it", ErrSecretInUse)
		}
		_, err := t.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, id)
		return err
	})
}

// CountSecrets returns how many secrets exist.
func (s *Store) CountSecrets(ctx context.Context) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM secrets`).Scan(&n)
	return n, err
}

// State reads a state value into v; false if it isn't set.
func (s *Store) State(ctx context.Context, key string, v any) (bool, error) {
	var b string
	err := s.r.QueryRowContext(ctx, `SELECT value FROM state WHERE key = ?`, key).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: read state %s: %w", key, err)
	}
	return true, json.Unmarshal([]byte(b), v)
}

// SetState stores a state value as JSON.
func (s *Store) SetState(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.w.ExecContext(ctx, `INSERT INTO state (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, string(b), ms(s.now()))
	if err != nil {
		return fmt.Errorf("store: write state %s: %w", key, err)
	}
	return nil
}
