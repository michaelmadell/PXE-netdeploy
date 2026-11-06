package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// IdempotentResponse is a stored response to a POST with Idempotency-Key.
type IdempotentResponse struct {
	Scope       string // "<METHOD> <route template>", e.g. "POST /clients"
	Key         string
	RequestHash string // the replay must carry the same body
	Status      int
	ContentType string
	Body        []byte
	CreatedAt   time.Time
}

// IdempotentResponse returns the stored response for scope and key, or
// ErrNotFound.
func (s *Store) IdempotentResponse(ctx context.Context, scope, key string) (IdempotentResponse, error) {
	r := IdempotentResponse{Scope: scope, Key: key}
	var created int64
	err := s.r.QueryRowContext(ctx, `SELECT request_hash, status, content_type, body, created_at
		FROM idempotency_keys WHERE scope = ? AND key = ?`, scope, key).
		Scan(&r.RequestHash, &r.Status, &r.ContentType, &r.Body, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return IdempotentResponse{}, ErrNotFound
	}
	if err != nil {
		return IdempotentResponse{}, fmt.Errorf("store: read idempotency key: %w", err)
	}
	r.CreatedAt = fromMS(created)
	return r, nil
}

// PutIdempotentResponse stores r. ErrDuplicate if the key is already stored
// (a concurrent request with the same key won); the caller should then serve
// the stored response.
func (s *Store) PutIdempotentResponse(ctx context.Context, r IdempotentResponse) error {
	_, err := s.w.ExecContext(ctx, `INSERT INTO idempotency_keys
		(scope, key, request_hash, status, content_type, body, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Scope, r.Key, r.RequestHash, r.Status, r.ContentType, r.Body, ms(s.now()))
	if uniqueViolation(err, "") {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("store: put idempotency key: %w", err)
	}
	return nil
}

// PurgeIdempotentResponses deletes responses stored before `before` (the
// server keeps them 24 h) and returns how many.
func (s *Store) PurgeIdempotentResponses(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE created_at < ?`, ms(before))
	if err != nil {
		return 0, fmt.Errorf("store: purge idempotency keys: %w", err)
	}
	return res.RowsAffected()
}
