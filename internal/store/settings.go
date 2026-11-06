package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Settings is the stored settings document. Doc is the API's Settings object
// as JSON, except that secrets are already replaced by what the caller wants
// persisted (e.g. the boot prompt password hash). The store does not look
// inside it.
type Settings struct {
	Doc       json.RawMessage
	Version   int64 // the ETag
	UpdatedAt time.Time
}

// Settings returns the current document, or ErrNotFound before InitSettings.
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	var (
		out     Settings
		doc     string
		updated int64
	)
	err := s.r.QueryRowContext(ctx, `SELECT doc, version, updated_at FROM settings WHERE id = 1`).
		Scan(&doc, &out.Version, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, ErrNotFound
	}
	if err != nil {
		return Settings{}, fmt.Errorf("store: read settings: %w", err)
	}
	out.Doc = json.RawMessage(doc)
	out.UpdatedAt = fromMS(updated)
	return out, nil
}

// InitSettings stores doc as version 1 if no settings exist yet (first run).
// It reports whether it wrote anything; an existing document is left alone.
func (s *Store) InitSettings(ctx context.Context, doc json.RawMessage) (bool, error) {
	res, err := s.w.ExecContext(ctx,
		`INSERT INTO settings (id, version, doc, updated_at) VALUES (1, 1, ?, ?) ON CONFLICT (id) DO NOTHING`,
		string(doc), ms(s.now()))
	if err != nil {
		return false, fmt.Errorf("store: init settings: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: init settings: %w", err)
	}
	return n == 1, nil
}

// ReplaceSettings writes doc if the stored version is still ifVersion and
// returns the new version. ErrVersionMismatch if it changed meanwhile,
// ErrNotFound before InitSettings.
func (s *Store) ReplaceSettings(ctx context.Context, doc json.RawMessage, ifVersion int64) (int64, error) {
	var next int64
	err := s.tx(ctx, func(t *sql.Tx) error {
		var cur int64
		err := t.QueryRowContext(ctx, `SELECT version FROM settings WHERE id = 1`).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("store: replace settings: %w", err)
		}
		if cur != ifVersion {
			return ErrVersionMismatch
		}
		next = cur + 1
		if _, err := t.ExecContext(ctx, `UPDATE settings SET doc = ?, version = ?, updated_at = ? WHERE id = 1`,
			string(doc), next, ms(s.now())); err != nil {
			return fmt.Errorf("store: replace settings: %w", err)
		}
		return nil
	})
	return next, err
}
