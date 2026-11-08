package backup

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

type env struct {
	st      *store.Store
	content *cas.Store
	data    string
}

func newEnv(t *testing.T) env {
	t.Helper()
	ctx := context.Background()
	data := t.TempDir()
	st, err := store.Open(ctx, data, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	content, err := cas.Open(ctx, cas.Options{Root: filepath.Join(data, "content"), Store: st, Reserve: func() int64 { return 0 }, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(data, "admin-cert.pem"), []byte("CERT"), 0o600)
	os.WriteFile(filepath.Join(data, "admin-key.pem"), []byte("KEY"), 0o600)
	return env{st, content, data}
}

// addFiles commits files as a tree with no owner: they stay until swept,
// which is enough for a backup (it copies what the blobs table lists).
func (e env) addFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	ctx := context.Background()
	st, err := e.content.NewStaging("t" + time.Now().Format("150405.000000000"))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if _, _, err := st.Add(name, strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	var tree string
	if _, err := st.Commit(ctx, store.TreeDriverPackage, func(ctx context.Context, id string) error { tree = id; return nil }); err != nil {
		t.Fatal(err)
	}
	return tree
}

func (e env) src() Source {
	return Source{Store: e.st, Content: e.content, DataDir: e.data, ServerVersion: "test"}
}

func TestBackupIncrementalRetentionRestore(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.addFiles(t, map[string]string{"a.txt": "alpha", "b/c.txt": "charlie"})
	destDir := t.TempDir()
	dest, err := OpenDest(ctx, TypeLocal, destDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 8, 2, 0, 0, 0, time.UTC)
	opts := Options{Trigger: TriggerManual, Retention: 2, Now: func() time.Time { return clock }}

	r1, err := Run(ctx, e.src(), dest, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Unreferenced blobs (no owner row yet) are still in the blobs table
	// until swept, and are backed up: the snapshot is what the store holds.
	if r1.CopiedBlobs != 2 || r1.Manifest.BlobCount != 2 || r1.Manifest.SchemaVersion != store.SchemaVersion() {
		t.Fatalf("first: %+v", r1)
	}
	for _, f := range []string{"snapshots/20261108T020000Z/manifest.json", "snapshots/20261108T020000Z/netdeploy.db", "snapshots/20261108T020000Z/admin-key.pem"} {
		if _, err := os.Stat(filepath.Join(destDir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}

	// Second run: nothing new to copy.
	clock = clock.Add(time.Hour)
	r2, _ := Run(ctx, e.src(), dest, opts)
	if r2.CopiedBlobs != 0 {
		t.Errorf("second copied %d", r2.CopiedBlobs)
	}
	// Third: one new file; retention 2 drops the first snapshot.
	e.addFiles(t, map[string]string{"d.txt": "delta"})
	clock = clock.Add(time.Hour)
	r3, err := Run(ctx, e.src(), dest, opts)
	if err != nil || r3.CopiedBlobs != 1 || len(r3.PrunedSnapshots) != 1 || r3.PrunedSnapshots[0] != "20261108T020000Z" {
		t.Fatalf("third: %+v %v", r3, err)
	}
	list, _ := List(dest)
	if len(list) != 2 || list[0].ID != "20261108T040000Z" {
		t.Errorf("list: %+v", list)
	}

	// A file that no kept snapshot lists is removed from the destination.
	stray := filepath.Join(destDir, "sha256", "ff", strings.Repeat("f", 64))
	os.MkdirAll(filepath.Dir(stray), 0o700)
	os.WriteFile(stray, []byte("x"), 0o600)
	clock = clock.Add(time.Hour)
	r4, _ := Run(ctx, e.src(), dest, opts)
	if r4.PrunedBlobs != 1 {
		t.Errorf("stray not pruned: %+v", r4)
	}

	// Restore the newest into an empty directory.
	data2, content2 := t.TempDir(), filepath.Join(t.TempDir(), "content")
	m, err := Restore(ctx, dest, RestoreOptions{DataDir: data2, ContentDir: content2})
	if err != nil || m.ID != r4.Manifest.ID {
		t.Fatalf("restore: %+v %v", m, err)
	}
	st2, err := store.Open(ctx, data2, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	c2, err := cas.Open(ctx, cas.Options{Root: content2, Store: st2, Reserve: func() int64 { return 0 }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if ok, detail := c2.Health(); !ok {
		t.Errorf("restored content: %s", detail)
	}
	if b, _ := os.ReadFile(filepath.Join(data2, "admin-cert.pem")); string(b) != "CERT" {
		t.Error("certificate not restored")
	}
	// The restored database has this backup as succeeded and as the last
	// success, so the restored server doesn't report it interrupted.
	var bst Status
	if ok, _ := st2.State(ctx, stateStatus, &bst); !ok || bst.LastSnapshot != r4.Manifest.ID {
		t.Errorf("restored backup status: %+v", bst)
	}
	// A second restore refuses without -force, and moves the database aside with it.
	if _, err := Restore(ctx, dest, RestoreOptions{DataDir: data2, ContentDir: content2}); err != ErrNotEmpty {
		t.Errorf("non-empty: %v", err)
	}
}

func TestBackupDetectsDamage(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.addFiles(t, map[string]string{"a.txt": "alpha"})
	var h string
	e.st.Blobs(ctx, func(s string, _ int64) error { h = s; return nil })
	p, _ := e.content.BlobPath(h)
	os.Chmod(p, 0o600)
	os.WriteFile(p, []byte("ALPHA"), 0o600) // same size, different bytes
	dest, _ := OpenDest(ctx, TypeLocal, t.TempDir(), nil)
	_, err := Run(ctx, e.src(), dest, Options{Trigger: TriggerManual})
	if err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("damage: %v", err)
	}
	if ok, _ := e.content.Health(); ok {
		t.Error("storage health still ok")
	}
	if ids, _ := snapshotIDs(dest); len(ids) != 0 {
		t.Error("snapshot written despite damage")
	}
}

func TestSweepHeldDuringBackup(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	release := e.content.HoldSweep()
	e.addFiles(t, map[string]string{"a.txt": "alpha"}) // unreferenced: a sweep would delete it
	if err := e.content.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	n := 0
	e.st.Blobs(ctx, func(string, int64) error { n++; return nil })
	if n != 1 {
		t.Fatal("swept while held")
	}
	release()
	deadline := time.Now().Add(5 * time.Second)
	for n != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		n = 0
		e.st.Blobs(ctx, func(string, int64) error { n++; return nil })
	}
	if n != 0 {
		t.Error("deferred sweep didn't run after release")
	}
}

func TestParseUNC(t *testing.T) {
	for in, want := range map[string]UNC{
		`\\nas01\backups`:             {"nas01", "backups", ""},
		`\\nas01\backups\netdeploy\x`: {"nas01", "backups", "netdeploy/x"},
		`//10.0.0.5/b/c`:              {"10.0.0.5", "b", "c"},
	} {
		if got, err := ParseUNC(in); err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{`C:\x`, `\\nas01`, `\\nas01\\x`, `\\a\b\..\c`} {
		if _, err := ParseUNC(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
