package cas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	_ "modernc.org/sqlite"
)

const plenty = int64(1) << 50

type harness struct {
	db   *store.Store
	cas  *Store
	raw  *sql.DB // for test owner tables
	root string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	raw, err := sql.Open("sqlite", filepath.ToSlash(db.Path())+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	for _, q := range []string{
		`CREATE TABLE test_owners (tree_id TEXT NOT NULL REFERENCES trees (id) ON DELETE RESTRICT) STRICT`,
		`DROP VIEW tree_refs`,
		`CREATE VIEW tree_refs (tree_id) AS SELECT tree_id FROM test_owners`,
	} {
		if _, err := raw.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	h := &harness{db: db, raw: raw, root: filepath.Join(dir, "content")}
	h.cas = h.open(t)
	return h
}

func (h *harness) open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), Options{Root: h.root, Store: h.db, Reserve: func() int64 { return 0 }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (h *harness) own(t *testing.T) func(context.Context, string) error {
	return func(ctx context.Context, id string) error {
		_, err := h.raw.ExecContext(ctx, `INSERT INTO test_owners VALUES (?)`, id)
		return err
	}
}

func (h *harness) disown(t *testing.T, id string) {
	t.Helper()
	if _, err := h.raw.Exec(`DELETE FROM test_owners WHERE tree_id = ?`, id); err != nil {
		t.Fatal(err)
	}
}

func sum(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }

func (h *harness) commit(t *testing.T, name string, files map[string]string, attach func(context.Context, string) error) string {
	t.Helper()
	st, err := h.cas.NewStaging(name)
	if err != nil {
		t.Fatal(err)
	}
	for p, body := range files {
		if _, _, err := st.Add(p, strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	id, err := st.Commit(context.Background(), store.TreeBootImage, attach)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func readFile(t *testing.T, fsys fs.FS, name string) (string, fs.FileInfo) {
	t.Helper()
	f, err := fsys.Open(name)
	if err != nil {
		t.Fatalf("open %q: %v", name, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), fi
}

func TestCommitAndServeTree(t *testing.T) {
	h := newHarness(t)
	id := h.commit(t, "job1", map[string]string{
		`Boot\BCD`:                               "bcd",
		"Boot/boot.sdi":                          "sdi",
		"EFI/Microsoft/Boot/Fonts/wgl4_boot.ttf": "font",
		"bootmgfw.efi":                           "sdi", // same bytes as boot.sdi
	}, h.own(t))
	tree := h.cas.Tree(id)

	body, fi := readFile(t, tree, "boot/bcd")
	if body != "bcd" || fi.Name() != "BCD" || fi.Size() != 3 {
		t.Errorf("boot/bcd = %q, name %q, size %d", body, fi.Name(), fi.Size())
	}
	if body, _ := readFile(t, tree, "EFI/MICROSOFT/Boot/fonts/WGL4_BOOT.TTF"); body != "font" {
		t.Errorf("font = %q", body)
	}
	f, err := tree.Open("Boot/boot.sdi")
	if err != nil {
		t.Fatal(err)
	}
	cf := f.(*File)
	if cf.SHA256() != sum("sdi") {
		t.Errorf("sha = %s", cf.SHA256())
	}
	buf := make([]byte, 2)
	if _, err := cf.ReadAt(buf, 1); err != nil || string(buf) != "di" {
		t.Errorf("ReadAt = %q, %v", buf, err)
	}
	f.Close()

	for _, name := range []string{".", "Boot", "missing", "../x", "/Boot/BCD"} {
		if _, err := tree.Open(name); err == nil {
			t.Errorf("open %q succeeded", name)
		}
	}

	// Deduplicated on disk: three distinct blobs, staging emptied.
	var blobs int
	filepath.WalkDir(filepath.Join(h.root, blobDir), func(_ string, d fs.DirEntry, _ error) error {
		if !d.IsDir() {
			blobs++
		}
		return nil
	})
	if blobs != 3 {
		t.Errorf("%d blob files, want 3", blobs)
	}
	if es, _ := os.ReadDir(filepath.Join(h.root, stagingDir)); len(es) != 0 {
		t.Errorf("staging not emptied: %v", es)
	}
}

func TestDuplicatePathInStaging(t *testing.T) {
	h := newHarness(t)
	st, err := h.cas.NewStaging("job")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Discard()
	if h, n, err := st.Add("Sources/Boot.wim", strings.NewReader("a")); err != nil || h != sum("a") || n != 1 {
		t.Fatal(err)
	}
	if _, _, err := st.Add(`sources\boot.WIM`, strings.NewReader("b")); !errors.Is(err, store.ErrDuplicatePath) {
		t.Errorf("err = %v, want ErrDuplicatePath", err)
	}
	if _, _, err := st.Add("a/../b", strings.NewReader("c")); !errors.Is(err, ErrBadPath) {
		t.Errorf("err = %v, want ErrBadPath", err)
	}
}

func TestSweep(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	kept := h.commit(t, "a", map[string]string{"shared": "shared", "only-kept": "kept"}, h.own(t))
	gone := h.commit(t, "b", map[string]string{"shared": "shared", "only-gone": "gone"}, h.own(t))
	// A commit whose owner never got recorded leaves an unowned tree.
	failed := errors.New("attach failed")
	st, _ := h.cas.NewStaging("c")
	st.Add("only-failed", strings.NewReader("failed"))
	if _, err := st.Commit(ctx, store.TreeImage, func(context.Context, string) error { return failed }); !errors.Is(err, failed) {
		t.Fatalf("commit err = %v", err)
	}
	h.disown(t, gone)

	// An orphan file with no row (crash between rename and insert).
	orphan := h.cas.blobPath(sum("orphan"))
	os.MkdirAll(filepath.Dir(orphan), 0o700)
	os.WriteFile(orphan, []byte("orphan"), 0o600)

	if err := h.cas.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	for body, want := range map[string]bool{"shared": true, "kept": true, "gone": false, "failed": false, "orphan": false} {
		_, err := os.Stat(h.cas.blobPath(sum(body)))
		if got := err == nil; got != want {
			t.Errorf("blob %q exists = %v, want %v", body, got, want)
		}
	}
	if body, _ := readFile(t, h.cas.Tree(kept), "shared"); body != "shared" {
		t.Errorf("kept tree: %q", body)
	}
	if _, err := h.cas.Tree(gone).Open("shared"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("swept tree open: %v", err)
	}
}

func TestOpenEmptiesStagingAndChecksBlobs(t *testing.T) {
	h := newHarness(t)
	id := h.commit(t, "a", map[string]string{"x": "xxxx", "y": "yy"}, h.own(t))
	_ = id
	interrupted := filepath.Join(h.root, stagingDir, "crashed-job")
	os.MkdirAll(interrupted, 0o700)
	os.WriteFile(filepath.Join(interrupted, "0"), []byte("partial"), 0o600)
	os.Remove(h.cas.blobPath(sum("xxxx")))
	os.WriteFile(h.cas.blobPath(sum("yy")), []byte("y"), 0o600) // truncated

	s := h.open(t)
	if _, err := os.Stat(interrupted); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("interrupted staging kept: %v", err)
	}
	ok, detail := s.Health()
	if ok || !strings.Contains(detail, "2 stored files") || !strings.Contains(detail, sum("xxxx")+" missing") {
		t.Errorf("health = %v, %q", ok, detail)
	}
	if ok, detail := h.cas.Health(); !ok {
		t.Errorf("fresh store health = %v, %q", ok, detail)
	}
}

func TestInsufficientStorage(t *testing.T) {
	h := newHarness(t)
	free := plenty
	h.cas.diskSpace = func(string) (int64, int64, error) { return plenty, free, nil }
	h.cas.reserve = func() int64 { return 1000 }
	h.cas.checkEvery = 64

	if err := h.cas.CheckSpace(plenty); !errors.Is(err, ErrInsufficientStorage) {
		t.Errorf("CheckSpace(huge) = %v", err)
	}
	st, _ := h.cas.NewStaging("job")
	defer st.Discard()
	// Free space drops below the reserve partway through a write.
	r := io.MultiReader(bytes.NewReader(make([]byte, 100)), readerFunc(func(p []byte) (int, error) {
		free = 500
		return copy(p, make([]byte, 100)), io.EOF
	}))
	if _, _, err := st.Add("big", r); !errors.Is(err, ErrInsufficientStorage) {
		t.Errorf("Add = %v, want ErrInsufficientStorage", err)
	}
	sp, err := h.cas.Space(context.Background())
	if err != nil || !sp.Low() || sp.Reserve != 1000 {
		t.Errorf("space = %+v, %v", sp, err)
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestDirFS(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "Boot", "Fonts"), 0o700)
	os.WriteFile(filepath.Join(dir, "Boot", "BCD"), []byte("bcd"), 0o600)
	os.WriteFile(filepath.Join(dir, "Boot", "Fonts", "a.ttf"), []byte("font"), 0o600)
	fsys := DirFS(dir)
	if body, _ := readFile(t, fsys, "boot/bcd"); body != "bcd" {
		t.Errorf("boot/bcd = %q", body)
	}
	if body, _ := readFile(t, fsys, "BOOT/fonts/A.TTF"); body != "font" {
		t.Errorf("font = %q", body)
	}
	for _, name := range []string{"../x", "missing", "Boot/missing"} {
		if _, err := fsys.Open(name); err == nil {
			t.Errorf("open %q succeeded", name)
		}
	}
}

func TestCleanPath(t *testing.T) {
	good := map[string]string{
		`\Boot\BCD`: "Boot/BCD", "/a//b/./c": "a/b/c", "x": "x",
	}
	for in, want := range good {
		if got, err := CleanPath(in); err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "/", "a/../b", "C:/x", "a\x00b", "file.txt:stream"} {
		if _, err := CleanPath(in); err == nil {
			t.Errorf("CleanPath(%q) accepted", in)
		}
	}
	if Fold("Boot/BCD-É") != "boot/bcd-É" {
		t.Errorf("Fold folds non-ASCII")
	}
}

func TestBlobsOutsideTheTree(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// An existing file to link in, as a kept upload would be.
	src := filepath.Join(t.TempDir(), "upload")
	os.WriteFile(src, []byte("the whole iso"), 0o600)

	st, _ := h.cas.NewStaging("job")
	st.Add("efi/boot/bootx64.efi", strings.NewReader("boot"))
	bh, bn, err := st.AddBlob(strings.NewReader("from inside a wim"))
	if err != nil || bh != sum("from inside a wim") || bn != 17 {
		t.Fatalf("AddBlob: %s %d %v", bh, bn, err)
	}
	if err := st.LinkBlob(src, sum("the whole iso"), 13); err != nil {
		t.Fatal(err)
	}
	var tree string
	if _, err := st.Commit(ctx, store.TreeImage, func(ctx context.Context, id string) error {
		tree = id
		return h.own(t)(ctx, id)
	}); err != nil {
		t.Fatal(err)
	}
	// The tree holds only its entry; both blobs are stored.
	if es, _ := h.db.TreeEntries(ctx, tree); len(es) != 1 {
		t.Errorf("tree entries: %v", es)
	}
	for _, body := range []string{"from inside a wim", "the whole iso"} {
		if b, err := os.ReadFile(h.cas.blobPath(sum(body))); err != nil || string(b) != body {
			t.Errorf("blob %q: %q %v", body, b, err)
		}
	}
	// The linked blob outlives the source.
	os.Remove(src)
	if _, err := os.Stat(h.cas.blobPath(sum("the whole iso"))); err != nil {
		t.Errorf("linked blob gone with its source: %v", err)
	}
	// Unreferenced extra blobs are swept.
	if err := h.cas.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.cas.blobPath(sum("from inside a wim"))); err == nil {
		t.Error("unreferenced extra blob survived the sweep")
	}
}
