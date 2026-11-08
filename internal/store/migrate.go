package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var embedded embed.FS

var migrationFiles fs.FS = mustSub(embedded, "migrations")

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads NNNN_name.sql files and checks they are numbered
// 1, 2, 3... with no gaps or duplicates.
func loadMigrations(files fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	var ms []migration
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || path.Ext(name) != ".sql" {
			continue
		}
		num, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v < 1 {
			return nil, fmt.Errorf("store: migration %q: name must be NNNN_description.sql", name)
		}
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return nil, fmt.Errorf("store: read migration %q: %w", name, err)
		}
		ms = append(ms, migration{version: v, name: name, sql: string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("store: migration %q: expected version %d (no gaps or duplicates)", m.name, i+1)
		}
	}
	return ms, nil
}

// migrate brings the database at dbPath up to the newest migration. db must be
// the single-connection writer pool. Each migration runs in its own
// transaction together with the user_version bump, so a failure leaves the
// database at the previous version.
func migrate(ctx context.Context, db *sql.DB, dbPath string, files fs.FS, now func() time.Time) error {
	ms, err := loadMigrations(files)
	if err != nil {
		return err
	}
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	latest := len(ms)
	switch {
	case current > latest:
		return fmt.Errorf("%w: database is version %d, this binary knows up to %d (%s)", ErrSchemaTooNew, current, latest, dbPath)
	case current == latest:
		return nil
	}

	if current > 0 {
		// Forward-only migrations: this snapshot is the rollback.
		backup := fmt.Sprintf("%s.bak-v%d-%s", dbPath, current, now().UTC().Format("20060102T150405Z"))
		if _, err := db.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
			return fmt.Errorf("store: snapshot before migrating to %s: %w", backup, err)
		}
	}

	for _, m := range ms[current:] {
		// A migration that rebuilds a table (SQLite's 12-step ALTER) starts
		// with this line: foreign keys are off while it runs (so dropping the
		// old table doesn't fire ON DELETE actions) and checked before commit.
		fkOff := strings.HasPrefix(m.sql, fkOffDirective)
		if fkOff {
			if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
				return fmt.Errorf("store: migration %s: %w", m.name, err)
			}
		}
		err := applyMigration(ctx, db, m, fkOff)
		if fkOff {
			if _, ferr := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); ferr != nil && err == nil {
				err = fmt.Errorf("store: migration %s: %w", m.name, ferr)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// fkOffDirective, as a migration's first line, runs it with foreign keys off.
const fkOffDirective = "-- netdeploy: foreign_keys=off"

func applyMigration(ctx context.Context, db *sql.DB, m migration, fkCheck bool) error {
	{
		t, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: migration %s: begin: %w", m.name, err)
		}
		if _, err := t.ExecContext(ctx, m.sql); err != nil {
			_ = t.Rollback()
			return fmt.Errorf("store: migration %s: %w", m.name, err)
		}
		if fkCheck {
			var table, parent string
			var rowid sql.NullInt64
			var fkid int
			err := t.QueryRowContext(ctx, "PRAGMA foreign_key_check").Scan(&table, &rowid, &parent, &fkid)
			if err == nil {
				_ = t.Rollback()
				return fmt.Errorf("store: migration %s: foreign key violation in %s (row %v) referencing %s", m.name, table, rowid.Int64, parent)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				_ = t.Rollback()
				return fmt.Errorf("store: migration %s: foreign key check: %w", m.name, err)
			}
		}
		// PRAGMA can't take bound parameters; m.version is an int we parsed.
		if _, err := t.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			_ = t.Rollback()
			return fmt.Errorf("store: migration %s: set version: %w", m.name, err)
		}
		if err := t.Commit(); err != nil {
			return fmt.Errorf("store: migration %s: commit: %w", m.name, err)
		}
	}
	return nil
}
