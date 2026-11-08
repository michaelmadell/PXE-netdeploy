package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func fakeSeal(v string) SealFunc {
	return func(id string, vv int64) ([]byte, error) { return []byte(id + "|" + v), nil }
}

const fp = "0000000000000000000000000000000000000000000000000000000000000000"

func TestSecrets(t *testing.T) {
	s, now := testStore(t)
	ctx := context.Background()
	a, err := s.CreateSecret(ctx, NewSecret{Name: "NAS", Kind: "share-credential", Hint: ptr("svc-backup"), KeyFingerprint: fp, Seal: fakeSeal("v1")})
	if err != nil {
		t.Fatal(err)
	}
	if a.ValueVersion != 1 || a.Version != 1 || *a.Hint != "svc-backup" {
		t.Fatalf("created: %+v", a)
	}
	_, sealed, err := s.SecretValue(ctx, a.ID)
	if err != nil || string(sealed) != a.ID+"|v1" {
		t.Fatalf("value: %q %v", sealed, err)
	}
	s.CreateSecret(ctx, NewSecret{Name: "Key", Kind: "product-key", KeyFingerprint: fp, Seal: fakeSeal("k")})
	if list, _, _ := s.ListSecrets(ctx, "share-credential", 0, ""); len(list) != 1 {
		t.Errorf("list by kind: %d", len(list))
	}
	if list, next, _ := s.ListSecrets(ctx, "", 1, ""); len(list) != 1 || next == "" {
		t.Errorf("page: %d %q", len(list), next)
	}

	if _, err := s.UpdateSecretMetadata(ctx, a.ID, ptr("NAS 2"), nil, 9); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale: %v", err)
	}
	if _, err := s.UpdateSecretMetadata(ctx, "nope", ptr("x"), nil, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	a, _ = s.UpdateSecretMetadata(ctx, a.ID, ptr("NAS 2"), ptr("backups"), 1)
	if a.Name != "NAS 2" || a.Description != "backups" || a.Version != 2 || a.ValueVersion != 1 {
		t.Errorf("updated: %+v", a)
	}
	*now = now.Add(time.Hour)
	var sawVV int64
	a, err = s.ReplaceSecretValue(ctx, a.ID, ptr("svc2"), fp, func(id string, vv int64) ([]byte, error) { sawVV = vv; return []byte("v2"), nil }, 2)
	if err != nil || sawVV != 2 || a.ValueVersion != 2 || a.Version != 3 || *a.Hint != "svc2" || !a.ValueUpdatedAt.Equal(*now) {
		t.Fatalf("replace: %+v %v", a, err)
	}

	// In use by the backup destination.
	s.w.ExecContext(ctx, `INSERT INTO settings (id, version, doc, updated_at) VALUES (1, 1, json_object('backup', json_object('destination', json_object('credentialSecretId', ?))), 0)
		ON CONFLICT (id) DO UPDATE SET doc = excluded.doc`, a.ID)
	if err := s.DeleteSecret(ctx, a.ID); !errors.Is(err, ErrSecretInUse) {
		t.Fatalf("delete in use: %v", err)
	}
	s.w.ExecContext(ctx, `UPDATE settings SET doc = '{}'`)
	if err := s.DeleteSecret(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSecret(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
}

func TestState(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	var v struct{ N int }
	if ok, err := s.State(ctx, "x", &v); ok || err != nil {
		t.Fatalf("unset: %v %v", ok, err)
	}
	s.SetState(ctx, "x", map[string]int{"N": 1})
	s.SetState(ctx, "x", map[string]int{"N": 2})
	if ok, _ := s.State(ctx, "x", &v); !ok || v.N != 2 {
		t.Errorf("state: %v %+v", ok, v)
	}
}

func fkDB(t *testing.T) (*sql.DB, string) {
	path := filepath.Join(t.TempDir(), FileName)
	db, err := sql.Open("sqlite", filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	return db, path
}

// A table rebuild with foreign keys off keeps child rows' references (no
// ON DELETE action fires), and foreign keys are on again afterwards.
func TestMigrateForeignKeysOff(t *testing.T) {
	ctx := context.Background()
	db, path := fkDB(t)
	v1 := fstest.MapFS{"0001_a.sql": {Data: []byte(`CREATE TABLE p (id TEXT PRIMARY KEY, k TEXT CHECK (k IN ('a'))) STRICT;
		CREATE TABLE c (id INTEGER PRIMARY KEY, pid TEXT REFERENCES p (id) ON DELETE SET NULL) STRICT;
		INSERT INTO p VALUES ('x', 'a'); INSERT INTO c VALUES (1, 'x');`)}}
	if err := migrate(ctx, db, path, v1, time.Now); err != nil {
		t.Fatal(err)
	}
	v2 := fstest.MapFS{"0001_a.sql": v1["0001_a.sql"], "0002_b.sql": {Data: []byte(fkOffDirective + `
		CREATE TABLE p_new (id TEXT PRIMARY KEY, k TEXT CHECK (k IN ('a', 'b'))) STRICT;
		INSERT INTO p_new SELECT * FROM p; DROP TABLE p; ALTER TABLE p_new RENAME TO p;`)}}
	if err := migrate(ctx, db, path, v2, time.Now); err != nil {
		t.Fatal(err)
	}
	var pid sql.NullString
	db.QueryRowContext(ctx, `SELECT pid FROM c WHERE id = 1`).Scan(&pid)
	if pid.String != "x" {
		t.Errorf("child reference lost: %v", pid)
	}
	var on int
	db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&on)
	if on != 1 {
		t.Error("foreign keys left off")
	}
	// A rebuild that breaks a reference fails and rolls back.
	v3 := fstest.MapFS{"0001_a.sql": v1["0001_a.sql"], "0002_b.sql": v2["0002_b.sql"], "0003_c.sql": {Data: []byte(fkOffDirective + `
		DELETE FROM p;`)}}
	if err := migrate(ctx, db, path, v3, time.Now); err == nil {
		t.Fatal("dangling reference accepted")
	}
	var n int
	db.QueryRowContext(ctx, `SELECT count(*) FROM p`).Scan(&n)
	if n != 1 {
		t.Error("failed migration not rolled back")
	}
}

// Migration 0007 rebuilds jobs; uploads keep their job_id.
func TestMigration0007KeepsUploadJobs(t *testing.T) {
	ctx := context.Background()
	db, path := fkDB(t)
	upTo := func(n int) fs.FS {
		m := fstest.MapFS{}
		entries, _ := fs.ReadDir(migrationFiles, ".")
		for i, e := range entries {
			if i < n {
				b, _ := fs.ReadFile(migrationFiles, e.Name())
				m[e.Name()] = &fstest.MapFile{Data: b}
			}
		}
		return m
	}
	if err := migrate(ctx, db, path, upTo(6), time.Now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO jobs (id, kind, status, created_at) VALUES ('j1', 'image.import', 'queued', 0);
		INSERT INTO uploads (id, length, hash_state, job_id, created_at, updated_at, expires_at) VALUES ('u1', 1, x'00', 'j1', 0, 0, 1);`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, path, upTo(7), time.Now); err != nil {
		t.Fatal(err)
	}
	var job sql.NullString
	db.QueryRowContext(ctx, `SELECT job_id FROM uploads WHERE id = 'u1'`).Scan(&job)
	if job.String != "j1" {
		t.Errorf("upload lost its job: %v", job)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO jobs (id, kind, status, created_at) VALUES ('j2', 'system.backup', 'queued', 0)`); err != nil {
		t.Errorf("system.backup job: %v", err)
	}
}
