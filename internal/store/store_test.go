package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

// testStore opens a fresh database in a temp dir with a controllable clock.
func testStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s, err := Open(context.Background(), t.TempDir(), Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, &now
}

func ptr[T any](v T) *T { return &v }

func TestOpenAppliesMigrationsOnce(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var v int
	if err := s.r.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	all, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if v != len(all) {
		t.Fatalf("user_version = %d, want %d", v, len(all))
	}
	var mode string
	if err := s.r.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	s.Close()

	// Reopening an up-to-date database changes nothing and writes no snapshot.
	s, err = Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if baks, _ := filepath.Glob(filepath.Join(dir, FileName+".bak-*")); len(baks) != 0 {
		t.Errorf("unexpected snapshots: %v", baks)
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.ExecContext(ctx, "PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(ctx, dir, Options{}); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("Open = %v, want ErrSchemaTooNew", err)
	}
}

func TestMigrateSnapshotsBeforeUpgrade(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	db, err := sql.Open("sqlite", filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	now := func() time.Time { return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC) }

	v1 := fstest.MapFS{"0001_a.sql": {Data: []byte("CREATE TABLE a (x INTEGER) STRICT;")}}
	if err := migrate(ctx, db, path, v1, now); err != nil {
		t.Fatal(err)
	}
	if baks, _ := filepath.Glob(path + ".bak-*"); len(baks) != 0 {
		t.Fatalf("snapshot of an empty database: %v", baks)
	}

	v2 := fstest.MapFS{
		"0001_a.sql": v1["0001_a.sql"],
		"0002_b.sql": {Data: []byte("CREATE TABLE b (y INTEGER) STRICT;")},
	}
	if err := migrate(ctx, db, path, v2, now); err != nil {
		t.Fatal(err)
	}
	want := path + ".bak-v1-20261106T120000Z"
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("snapshot %s: %v", want, err)
	}
}

func TestMigrateFailureKeepsPreviousVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), FileName)
	db, err := sql.Open("sqlite", filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	files := fstest.MapFS{
		"0001_a.sql":   {Data: []byte("CREATE TABLE a (x INTEGER) STRICT;")},
		"0002_bad.sql": {Data: []byte("CREATE TABLE b (y INTEGER) STRICT; CREATE TABLE a (x INTEGER);")},
	}
	if err := migrate(ctx, db, path, files, time.Now); err == nil {
		t.Fatal("migrate succeeded, want error from 0002")
	}
	var v int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Errorf("user_version = %d, want 1", v)
	}
	var n int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name = 'b'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("table b from the failed migration exists")
	}
}

func TestLoadMigrationsRejectsGaps(t *testing.T) {
	files := fstest.MapFS{
		"0001_a.sql": {Data: []byte("SELECT 1;")},
		"0003_c.sql": {Data: []byte("SELECT 1;")},
	}
	if _, err := loadMigrations(files); err == nil {
		t.Fatal("want an error for the gap at 0002")
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)

	if _, err := s.Settings(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Settings before init = %v, want ErrNotFound", err)
	}
	if ok, err := s.InitSettings(ctx, json.RawMessage(`{"interfaces":[]}`)); err != nil || !ok {
		t.Fatalf("InitSettings = %v, %v; want true, nil", ok, err)
	}
	if ok, err := s.InitSettings(ctx, json.RawMessage(`{"other":1}`)); err != nil || ok {
		t.Fatalf("second InitSettings = %v, %v; want false, nil", ok, err)
	}
	got, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || string(got.Doc) != `{"interfaces":[]}` {
		t.Fatalf("Settings = %d %s", got.Version, got.Doc)
	}

	if _, err := s.ReplaceSettings(ctx, json.RawMessage(`{"a":1}`), 7); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("stale ReplaceSettings = %v, want ErrVersionMismatch", err)
	}
	v, err := s.ReplaceSettings(ctx, json.RawMessage(`{"a":1}`), 1)
	if err != nil || v != 2 {
		t.Fatalf("ReplaceSettings = %d, %v; want 2, nil", v, err)
	}
	// The schema rejects anything but a JSON object.
	if _, err := s.ReplaceSettings(ctx, json.RawMessage(`[1]`), 2); err == nil {
		t.Fatal("ReplaceSettings accepted a JSON array")
	}
}

func TestClientsCRUD(t *testing.T) {
	ctx := context.Background()
	s, now := testStore(t)

	c, err := s.CreateClient(ctx, ClientInput{MAC: "00-1B-21-AA-BB-CC", Variables: map[string]string{"site": "lab"}})
	if err != nil {
		t.Fatal(err)
	}
	if c.MAC != "00:1b:21:aa:bb:cc" || c.Source != SourceManual || c.Version != 1 || c.Variables["site"] != "lab" {
		t.Fatalf("created %+v", c)
	}
	if _, err := s.CreateClient(ctx, ClientInput{MAC: "00:1b:21:aa:bb:cc"}); !errors.Is(err, ErrMACTaken) {
		t.Fatalf("duplicate MAC = %v, want ErrMACTaken", err)
	}
	if _, err := s.CreateClient(ctx, ClientInput{MAC: "not-a-mac"}); err == nil {
		t.Fatal("accepted an invalid MAC")
	}

	got, err := s.ClientByMAC(ctx, "00:1B:21:AA:BB:CC")
	if err != nil || got.ID != c.ID {
		t.Fatalf("ClientByMAC = %v, %v", got.ID, err)
	}

	*now = now.Add(time.Minute)
	in := c.ClientInput
	in.Hostname, in.ReservedIP = ptr("lab-01"), ptr("10.77.0.50")
	if _, err := s.ReplaceClient(ctx, c.ID, in, 99); !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("stale ReplaceClient = %v, want ErrVersionMismatch", err)
	}
	r, err := s.ReplaceClient(ctx, c.ID, in, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != 2 || *r.Hostname != "lab-01" || !r.UpdatedAt.Equal(*now) {
		t.Fatalf("replaced %+v", r)
	}

	other, err := s.CreateClient(ctx, ClientInput{MAC: "00:1b:21:00:00:01"})
	if err != nil {
		t.Fatal(err)
	}
	in2 := other.ClientInput
	in2.ReservedIP = ptr("10.77.0.50")
	if _, err := s.ReplaceClient(ctx, other.ID, in2, 1); !errors.Is(err, ErrReservedIPTaken) {
		t.Fatalf("duplicate reserved IP = %v, want ErrReservedIPTaken", err)
	}

	if err := s.DeleteClient(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteClient(ctx, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

func TestRecordSighting(t *testing.T) {
	ctx := context.Background()
	s, now := testStore(t)

	c, created, err := s.RecordSighting(ctx, Sighting{MAC: "00:1b:21:aa:bb:cc", Arch: ptr("uefi-x64"), SMBIOSUUID: ptr("4c4c4544-0000-1000-8000-000000000001")})
	if err != nil || !created {
		t.Fatalf("first sighting = %v, %v", created, err)
	}
	if c.Source != SourceDiscovered || c.LastIP != nil || *c.LastArch != "uefi-x64" || c.Version != 1 {
		t.Fatalf("discovered %+v", c)
	}

	*now = now.Add(time.Minute)
	c2, created, err := s.RecordSighting(ctx, Sighting{MAC: "00:1B:21:AA:BB:CC", IP: ptr("10.77.0.100"), SMBIOSUUID: ptr("ignored-because-known")})
	if err != nil || created {
		t.Fatalf("second sighting = %v, %v", created, err)
	}
	if c2.ID != c.ID || *c2.LastIP != "10.77.0.100" || *c2.LastArch != "uefi-x64" || !c2.LastSeenAt.Equal(*now) {
		t.Fatalf("updated %+v", c2)
	}
	if *c2.SMBIOSUUID != *c.SMBIOSUUID {
		t.Errorf("sighting overwrote a known SMBIOS UUID: %s", *c2.SMBIOSUUID)
	}
	// A sighting must not invalidate an admin's If-Match.
	if c2.Version != 1 || !c2.UpdatedAt.Equal(c.UpdatedAt) {
		t.Errorf("sighting changed version/updatedAt: %d %v", c2.Version, c2.UpdatedAt)
	}
}

func TestListClientsPaginatesAndFilters(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	macs := []string{"00:1b:21:00:00:01", "00:1b:21:00:00:02", "00:1b:21:00:00:03"}
	for _, m := range macs {
		if _, err := s.CreateClient(ctx, ClientInput{MAC: m}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.RecordSighting(ctx, Sighting{MAC: "00:1b:21:00:00:04"}); err != nil {
		t.Fatal(err)
	}

	page, next, err := s.ListClients(ctx, ClientFilter{Limit: 2})
	if err != nil || len(page) != 2 || next == "" {
		t.Fatalf("page 1: %d items, next %q, %v", len(page), next, err)
	}
	page2, next2, err := s.ListClients(ctx, ClientFilter{Limit: 2, After: next})
	if err != nil || len(page2) != 2 || next2 != "" {
		t.Fatalf("page 2: %d items, next %q, %v", len(page2), next2, err)
	}
	if page2[0].ID <= page[1].ID {
		t.Error("pages overlap or are out of order")
	}

	disc, _, err := s.ListClients(ctx, ClientFilter{Source: SourceDiscovered})
	if err != nil || len(disc) != 1 || disc[0].MAC != "00:1b:21:00:00:04" {
		t.Fatalf("source filter: %+v, %v", disc, err)
	}
}

func TestOneActiveDeploymentPerClient(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	c, err := s.CreateClient(ctx, ClientInput{MAC: "00:1b:21:aa:bb:cc"})
	if err != nil {
		t.Fatal(err)
	}
	nd := NewDeployment{ClientID: c.ID, ProfileID: "prof1", ProfileSnapshot: json.RawMessage(`{}`), Origin: OriginScheduled}

	d, err := s.CreateDeployment(ctx, nd)
	if err != nil || d.Status != StatusPending {
		t.Fatalf("CreateDeployment = %+v, %v", d, err)
	}
	if _, err := s.CreateDeployment(ctx, nd); !errors.Is(err, ErrDeploymentActive) {
		t.Fatalf("second pending = %v, want ErrDeploymentActive", err)
	}

	// The armed deployment is consumed exactly once.
	if d, err = s.StartDeployment(ctx, d.ID); err != nil || d.Status != StatusRunning || d.StartedAt == nil {
		t.Fatalf("StartDeployment = %+v, %v", d, err)
	}
	if _, err := s.StartDeployment(ctx, d.ID); !errors.Is(err, ErrBadTransition) {
		t.Fatalf("second StartDeployment = %v, want ErrBadTransition", err)
	}
	if _, err := s.CreateDeployment(ctx, nd); !errors.Is(err, ErrDeploymentActive) {
		t.Fatalf("pending while running = %v, want ErrDeploymentActive", err)
	}
	if err := s.DeleteClient(ctx, c.ID); !errors.Is(err, ErrDeploymentActive) {
		t.Fatalf("DeleteClient while running = %v, want ErrDeploymentActive", err)
	}

	if d, err = s.FinishDeployment(ctx, d.ID, StatusSucceeded, "done"); err != nil || d.FinishedAt == nil {
		t.Fatalf("FinishDeployment = %+v, %v", d, err)
	}
	if again, err := s.FinishDeployment(ctx, d.ID, StatusSucceeded, "again"); err != nil || again.Message != "done" {
		t.Fatalf("repeat finish = %+v, %v; want unchanged", again, err)
	}
	if _, err := s.FinishDeployment(ctx, d.ID, StatusFailed, ""); !errors.Is(err, ErrBadTransition) {
		t.Fatalf("fail after success = %v, want ErrBadTransition", err)
	}
	if _, err := s.ActiveDeployment(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ActiveDeployment after finish = %v, want ErrNotFound", err)
	}

	// Re-test: arm again, then delete the client after cancelling.
	d2, err := s.CreateDeployment(ctx, nd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishDeployment(ctx, d2.ID, StatusCancelled, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteClient(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Deployment(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("history after client delete = %v, want ErrNotFound (cascade)", err)
	}
}

func TestDeploymentNeedsClient(t *testing.T) {
	s, _ := testStore(t)
	_, err := s.CreateDeployment(context.Background(), NewDeployment{
		ClientID: "nope", ProfileID: "p", ProfileSnapshot: json.RawMessage(`{}`), Origin: OriginBootPrompt, Running: true,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateDeployment for a missing client = %v, want ErrNotFound", err)
	}
}

func TestLeases(t *testing.T) {
	ctx := context.Background()
	s, now := testStore(t)
	ip := netip.MustParseAddr
	mac := "00:1b:21:aa:bb:cc"
	exp := now.Add(time.Hour)

	if err := s.PutLease(ctx, Lease{Interface: "lab0", IP: ip("10.77.0.100"), MAC: mac, ExpiresAt: exp}); err != nil {
		t.Fatal(err)
	}
	// Renewing at another address releases the old one.
	if err := s.PutLease(ctx, Lease{Interface: "lab0", IP: ip("10.77.0.9"), MAC: mac, ExpiresAt: exp}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LeaseByIP(ctx, "lab0", ip("10.77.0.100")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old address still leased: %v", err)
	}
	l, err := s.LeaseByIP(ctx, "lab0", ip("10.77.0.9"))
	if err != nil || l.MAC != mac || l.State != LeaseBound {
		t.Fatalf("LeaseByIP = %+v, %v", l, err)
	}

	if err := s.DeclineLease(ctx, "lab0", ip("10.77.0.10"), "00:1b:21:00:00:01", now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	all, err := s.Leases(ctx, "lab0")
	if err != nil || len(all) != 2 {
		t.Fatalf("Leases = %+v, %v", all, err)
	}
	if all[0].IP != ip("10.77.0.9") || all[1].IP != ip("10.77.0.10") {
		t.Errorf("not in numeric order: %v, %v", all[0].IP, all[1].IP)
	}

	*now = now.Add(2 * time.Hour)
	if _, err := s.LeaseByIP(ctx, "lab0", ip("10.77.0.9")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired lease returned: %v", err)
	}
	if n, err := s.PurgeExpiredLeases(ctx, *now); err != nil || n != 2 {
		t.Fatalf("PurgeExpiredLeases = %d, %v; want 2", n, err)
	}
}

func TestIdempotentResponses(t *testing.T) {
	ctx := context.Background()
	s, now := testStore(t)
	r := IdempotentResponse{Scope: "POST /clients", Key: "k1", RequestHash: "h", Status: 201, ContentType: "application/json", Body: []byte(`{}`)}

	if _, err := s.IdempotentResponse(ctx, r.Scope, r.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("before put = %v, want ErrNotFound", err)
	}
	if err := s.PutIdempotentResponse(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIdempotentResponse(ctx, r); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second put = %v, want ErrDuplicate", err)
	}
	got, err := s.IdempotentResponse(ctx, r.Scope, r.Key)
	if err != nil || got.Status != 201 || string(got.Body) != `{}` {
		t.Fatalf("IdempotentResponse = %+v, %v", got, err)
	}
	// Same key, different operation: independent.
	if _, err := s.IdempotentResponse(ctx, "POST /deployments", r.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other scope = %v, want ErrNotFound", err)
	}
	*now = now.Add(25 * time.Hour)
	if n, err := s.PurgeIdempotentResponses(ctx, now.Add(-24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("purge = %d, %v; want 1", n, err)
	}
}
