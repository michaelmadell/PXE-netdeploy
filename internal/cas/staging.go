package cas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Staging collects the files of one tree while a job writes them. Nothing
// is visible until Commit; Discard (or a restart) throws it away.
type Staging struct {
	s   *Store
	dir string

	mu      sync.Mutex
	files   []staged
	folds   map[string]bool
	written int64 // since the last space check
	closed  bool
}

type staged struct {
	path, fold, sha string // path "" : a blob outside the tree
	size            int64
	tmp             string
}

// NewStaging starts a tree. name identifies the job (e.g. its id) and must
// be unique among running jobs.
func (s *Store) NewStaging(name string) (*Staging, error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return nil, fmt.Errorf("cas: invalid staging name %q", name)
	}
	dir := filepath.Join(s.root, stagingDir, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cas: create staging: %w", err)
	}
	return &Staging{s: s, dir: dir, folds: map[string]bool{}}, nil
}

// Add streams r into staging as the file at path p, hashing it, and
// returns its SHA-256 and size. It checks free space before writing and
// every 256 MiB, failing with ErrInsufficientStorage before the reserve is
// used.
func (st *Staging) Add(p string, r io.Reader) (string, int64, error) {
	clean, err := CleanPath(p)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %q", err, p)
	}
	idx, tmp, err := st.reserve(staged{path: clean, fold: Fold(clean)})
	if err != nil {
		return "", 0, err
	}
	return st.write(idx, tmp, clean, r)
}

// AddBlob stores r as a blob outside the tree (see store.NewTree.Blobs)
// and returns its SHA-256 and size.
func (st *Staging) AddBlob(r io.Reader) (string, int64, error) {
	idx, tmp, err := st.reserve(staged{})
	if err != nil {
		return "", 0, err
	}
	return st.write(idx, tmp, "blob", r)
}

// LinkBlob adds an existing, never-modified file (a complete upload) as a
// blob outside the tree, without copying it where the volume supports hard
// links. sha256 and size must be the file's (the caller verified them).
func (st *Staging) LinkBlob(path, sha256 string, size int64) error {
	if !validHash(sha256) {
		return fmt.Errorf("cas: invalid sha256 %q", sha256)
	}
	idx, tmp, err := st.reserve(staged{})
	if err != nil {
		return err
	}
	if err := os.Link(path, tmp); err != nil {
		// No hard links (e.g. FAT, some network shares): copy.
		in, oerr := os.Open(path)
		if oerr != nil {
			return fmt.Errorf("cas: link %s: %w", path, err)
		}
		defer in.Close()
		got, n, werr := st.write(idx, tmp, "linked blob", in)
		if werr != nil {
			return werr
		}
		if got != sha256 || n != size {
			return fmt.Errorf("cas: %s changed: sha256 %s, %d bytes", path, got, n)
		}
		return nil
	}
	st.mu.Lock()
	st.files[idx].sha, st.files[idx].size = sha256, size
	st.mu.Unlock()
	return nil
}

// reserve registers a staged file and returns its index and temp path.
func (st *Staging) reserve(f staged) (int, string, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return 0, "", errors.New("cas: staging already committed or discarded")
	}
	if f.path != "" {
		if st.folds[f.fold] {
			return 0, "", fmt.Errorf("%w: %q", store.ErrDuplicatePath, f.path)
		}
		st.folds[f.fold] = true
	}
	f.tmp = filepath.Join(st.dir, strconv.Itoa(len(st.files)))
	st.files = append(st.files, f)
	return len(st.files) - 1, f.tmp, nil
}

func (st *Staging) write(idx int, tmp, what string, r io.Reader) (string, int64, error) {
	clean := what
	if err := st.s.CheckSpace(0); err != nil {
		return "", 0, err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("cas: stage %q: %w", clean, err)
	}
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(f, h), &spaceChecked{r: r, st: st}, make([]byte, 1<<20))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if errors.Is(err, ErrInsufficientStorage) {
			return "", 0, err
		}
		return "", 0, fmt.Errorf("cas: stage %q: %w", clean, err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	st.mu.Lock()
	st.files[idx].sha = sum
	st.files[idx].size = n
	st.mu.Unlock()
	return sum, n, nil
}

// spaceChecked checks free space every Store.checkEvery bytes read.
type spaceChecked struct {
	r  io.Reader
	st *Staging
}

func (c *spaceChecked) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.st.mu.Lock()
	c.st.written += int64(n)
	due := c.st.written >= c.st.s.checkEvery
	if due {
		c.st.written = 0
	}
	c.st.mu.Unlock()
	if due {
		if serr := c.st.s.CheckSpace(0); serr != nil {
			return n, serr
		}
	}
	return n, err
}

// Commit publishes the staged files and records the tree. attach, if not
// nil, runs before the store's lock is released and must record an owner
// for the tree (e.g. set images.tree_id): an unowned tree is garbage. If
// attach fails the tree is left for the next sweep. The staging directory
// is removed either way.
func (st *Staging) Commit(ctx context.Context, kind store.TreeKind, attach func(ctx context.Context, treeID string) error) (string, error) {
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return "", errors.New("cas: staging already committed or discarded")
	}
	st.closed = true
	files := st.files
	st.mu.Unlock()
	defer os.RemoveAll(st.dir)

	s := st.s
	s.mu.RLock()
	defer s.mu.RUnlock()

	t := store.NewTree{Kind: kind, Entries: make([]store.NewTreeEntry, 0, len(files))}
	for _, f := range files {
		if f.sha == "" {
			return "", fmt.Errorf("cas: %q was not fully written", f.path)
		}
		if err := s.publish(f.tmp, f.sha); err != nil {
			return "", err
		}
		if f.path == "" {
			t.Blobs = append(t.Blobs, store.NewBlob{SHA256: f.sha, Size: f.size})
			continue
		}
		t.Entries = append(t.Entries, store.NewTreeEntry{Path: f.path, Fold: f.fold, SHA256: f.sha, Size: f.size})
	}
	id, err := s.db.InsertTree(ctx, t)
	if err != nil {
		return "", err
	}
	if attach != nil {
		if err := attach(ctx, id); err != nil {
			return "", err
		}
	}
	return id, nil
}

// publish moves a staged file into sha256/ unless that blob is already
// there (from an earlier import, or a crash before its row was written:
// same hash, same bytes), in which case the staged copy is dropped.
// Caller holds s.mu.
func (s *Store) publish(tmp, h string) error {
	dst := s.blobPath(h)
	if _, err := os.Lstat(dst); err == nil {
		return os.Remove(tmp)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("cas: publish %s: %w", h, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		// Another commit published it first (Windows refuses to replace a
		// file that is open).
		if _, serr := os.Lstat(dst); serr == nil {
			return os.Remove(tmp)
		}
		return fmt.Errorf("cas: publish %s: %w", h, err)
	}
	return nil
}

// Dir is the staging directory; scratch files may be created in it (they
// go when the staging is committed or discarded).
func (st *Staging) Dir() string { return st.dir }

// Paths lists the staged tree files in the order they were added.
func (st *Staging) Paths() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []string
	for _, f := range st.files {
		if f.path != "" && f.sha != "" {
			out = append(out, f.path)
		}
	}
	return out
}

// Open opens a staged tree file by path (case-insensitive) before commit.
func (st *Staging) Open(p string) (*os.File, error) {
	clean, err := CleanPath(p)
	if err != nil {
		return nil, err
	}
	fold := Fold(clean)
	st.mu.Lock()
	tmp := ""
	for _, f := range st.files {
		if f.path != "" && f.fold == fold && f.sha != "" {
			tmp = f.tmp
			break
		}
	}
	st.mu.Unlock()
	if tmp == "" {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	return os.Open(tmp)
}

// Discard throws the staged files away.
func (st *Staging) Discard() error {
	st.mu.Lock()
	st.closed = true
	st.mu.Unlock()
	if err := os.RemoveAll(st.dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cas: discard staging: %w", err)
	}
	return nil
}
