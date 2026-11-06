// Package store persists netdeployd's state in SQLite (ADR 0014).
//
// The schema enforces the invariants (STRICT tables, CHECK constraints,
// unique and partial unique indexes); this package maps constraint failures
// to the errors below. Writes go through a single connection, so a check and
// the write that follows it inside one transaction cannot race.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// FileName is the database file inside the data directory.
const FileName = "netdeploy.db"

var (
	// ErrNotFound: no row with that key.
	ErrNotFound = errors.New("store: not found")
	// ErrVersionMismatch: the caller's version (ETag) is stale.
	ErrVersionMismatch = errors.New("store: version mismatch")
	// ErrMACTaken: another client already has this MAC.
	ErrMACTaken = errors.New("store: mac already registered")
	// ErrReservedIPTaken: another client already has this reserved IP.
	ErrReservedIPTaken = errors.New("store: reserved ip already in use")
	// ErrDeploymentActive: the client has a pending or running deployment.
	ErrDeploymentActive = errors.New("store: client has an active deployment")
	// ErrBadTransition: the deployment is not in a state that allows the change.
	ErrBadTransition = errors.New("store: deployment status does not allow this change")
	// ErrDuplicate: the row already exists (e.g. an idempotency key).
	ErrDuplicate = errors.New("store: already exists")
	// ErrSchemaTooNew: the database was written by a newer netdeployd.
	ErrSchemaTooNew = errors.New("store: database schema is newer than this binary")
)

// Store is the SQLite-backed store. Safe for concurrent use.
type Store struct {
	w    *sql.DB // single connection; all writes
	r    *sql.DB // read-only pool
	path string
	now  func() time.Time
}

// Options configure Open.
type Options struct {
	// ReadConns caps the read pool. Zero means 4.
	ReadConns int
	// Now overrides the clock (tests). Nil means time.Now.
	Now func() time.Time
}

// Open opens (creating if needed) the database in dataDir and applies any
// pending migrations. Before migrating a non-empty database it writes a
// snapshot next to it (see migrate).
func Open(ctx context.Context, dataDir string, opts Options) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("store: create data dir: %w", err)
	}
	path := filepath.Join(dataDir, FileName)
	if opts.ReadConns <= 0 {
		opts.ReadConns = 4
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	// SQLite accepts forward slashes on Windows; modernc strips the _pragma
	// and _txlock parameters before opening the file.
	base := filepath.ToSlash(path) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	w, err := sql.Open("sqlite", base+"&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)

	s := &Store{w: w, path: path, now: opts.Now}
	if err := migrate(ctx, w, path, migrationFiles, s.now); err != nil {
		w.Close()
		return nil, err
	}

	// Open the read pool after migrating, so WAL mode is already set.
	r, err := sql.Open("sqlite", base+"&_pragma=query_only(1)")
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("store: open %s (read pool): %w", path, err)
	}
	r.SetMaxOpenConns(opts.ReadConns)
	s.r = r
	if err := r.PingContext(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("store: open %s (read pool): %w", path, err)
	}
	return s, nil
}

// Close closes both pools.
func (s *Store) Close() error {
	var errs []error
	if s.r != nil {
		errs = append(errs, s.r.Close())
	}
	errs = append(errs, s.w.Close())
	return errors.Join(errs...)
}

// Path is the database file path.
func (s *Store) Path() string { return s.path }

// tx runs fn in a write transaction (BEGIN IMMEDIATE) and commits if fn
// returns nil.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	t, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	if err := fn(t); err != nil {
		_ = t.Rollback()
		return err
	}
	if err := t.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

func newID() string { return ulid.Make().String() }

// Timestamps are stored as Unix milliseconds, UTC.
func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func fromNullMS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

func nullMS(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: ms(*t), Valid: true}
}

func nullStr(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}

func fromNullStr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}

// uniqueViolation reports whether err is a UNIQUE or PRIMARY KEY constraint
// failure on target ("table.column"; empty matches any). SQLite names the
// columns in the message, e.g. "UNIQUE constraint failed: clients.mac".
func uniqueViolation(err error, target string) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		return target == "" || strings.Contains(se.Error(), target)
	}
	return false
}

// Ping checks the database answers a query (health).
func (s *Store) Ping(ctx context.Context) error {
	var one int
	if err := s.r.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("store: ping: %w", err)
	}
	return nil
}
