// Package cas is the content-addressed store (ADR 0015): imported and built
// files kept once each under <content>/sha256/, named by their SHA-256, with
// trees (an extracted ISO, a driver package, a boot image) described by
// manifests in the database and served as a virtual fs.FS.
//
// Layout under the content root:
//
//	sha256/ab/ab12…   blobs, never modified
//	staging/<name>/   files a running job is writing; emptied at startup
//	uploads/          partial tus uploads
//
// All three are on one volume, so publishing a file is an atomic rename.
package cas

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Subdirectories of the content root.
const (
	blobDir    = "sha256"
	stagingDir = "staging"
	uploadDir  = "uploads"
)

// DefaultReserveBytes is the default storage.reserveBytes (5 GiB).
const DefaultReserveBytes int64 = 5 << 30

// spaceCheckEvery is how many bytes a job writes between free-space checks.
const spaceCheckEvery int64 = 256 << 20

// ErrInsufficientStorage: writing would leave less free space than the
// reserve. The API reports it as 507 / insufficient_storage.
var ErrInsufficientStorage = errors.New("cas: insufficient storage")

// Options configure Open.
type Options struct {
	Root  string       // content root; created if missing
	Store *store.Store // manifest rows
	// Reserve returns storage.reserveBytes. Nil: DefaultReserveBytes.
	Reserve func() int64
	Logger  *slog.Logger
}

// Store is the content store. Safe for concurrent use.
type Store struct {
	root    string
	db      *store.Store
	reserve func() int64
	log     *slog.Logger

	// Commits hold mu for reading while they rename blobs into place and
	// insert their rows; Sweep holds it for writing, so it never deletes a
	// blob a commit has just decided to reuse, nor a file renamed in but not
	// yet recorded.
	mu sync.RWMutex

	// Overridable in tests.
	diskSpace  func(path string) (total, free int64, err error)
	checkEvery int64

	healthMu sync.Mutex
	problems []string // from the startup check, plus damage found later

	// Backups hold the sweep (HoldSweep) while copying blobs; a sweep asked
	// for meanwhile runs when the last hold is released.
	sweepMu      sync.Mutex
	sweepHolds   int
	sweepPending bool
}

// Open prepares the content root, empties staging, checks every stored blob
// is present with the right size, and sweeps unreferenced content.
func Open(ctx context.Context, opts Options) (*Store, error) {
	if opts.Root == "" {
		return nil, errors.New("cas: content root is required")
	}
	if opts.Store == nil {
		return nil, errors.New("cas: store is required")
	}
	if opts.Reserve == nil {
		opts.Reserve = func() int64 { return DefaultReserveBytes }
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("cas: content root: %w", err)
	}
	s := &Store{
		root: root, db: opts.Store, reserve: opts.Reserve,
		log:       opts.Logger.With("svc", "content-store"),
		diskSpace: diskSpace, checkEvery: spaceCheckEvery,
	}
	if err := s.prepare(); err != nil {
		return nil, err
	}
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	if err := s.Sweep(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// prepare creates the layout, empties staging and checks staging and blobs
// share a volume (a rename between them must work).
func (s *Store) prepare() error {
	for _, d := range []string{blobDir, stagingDir, uploadDir} {
		if err := os.MkdirAll(filepath.Join(s.root, d), 0o700); err != nil {
			return fmt.Errorf("cas: create %s: %w", d, err)
		}
	}
	staging := filepath.Join(s.root, stagingDir)
	entries, err := os.ReadDir(staging)
	if err != nil {
		return fmt.Errorf("cas: read staging: %w", err)
	}
	for _, e := range entries {
		// Left by jobs that were running when the server stopped. A
		// leftover that can't be removed (e.g. a DISM mount the server
		// couldn't discard unelevated, ADR 0022) is logged, not fatal.
		if err := os.RemoveAll(filepath.Join(staging, e.Name())); err != nil {
			s.log.Warn("couldn't remove interrupted staging files", "name", e.Name(), "err", err)
			continue
		}
		s.log.Info("removed interrupted staging files", "name", e.Name())
	}
	probe := filepath.Join(staging, ".volume-probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return fmt.Errorf("cas: write to staging: %w", err)
	}
	dst := filepath.Join(s.root, blobDir, ".volume-probe")
	if err := os.Rename(probe, dst); err != nil {
		os.Remove(probe)
		return fmt.Errorf("cas: %s and %s must be on one volume: %w", staging, filepath.Join(s.root, blobDir), err)
	}
	return os.Remove(dst)
}

// check stats every blob row; missing or wrong-sized files are reported by
// Health. Hashes were verified when written and aren't re-read here.
func (s *Store) check(ctx context.Context) error {
	var problems []string
	n := 0
	err := s.db.Blobs(ctx, func(h string, size int64) error {
		n++
		fi, err := os.Stat(s.blobPath(h))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			problems = append(problems, h+" missing")
		case err != nil:
			problems = append(problems, h+": "+err.Error())
		case fi.Size() != size:
			problems = append(problems, fmt.Sprintf("%s is %d bytes, expected %d", h, fi.Size(), size))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		s.log.Error("stored files missing or damaged", "count", len(problems), "first", problems[0])
	}
	s.log.Info("content store opened", "root", s.root, "blobs", n, "problems", len(problems))
	s.healthMu.Lock()
	s.problems = problems
	s.healthMu.Unlock()
	return nil
}

// Root is the content root directory.
func (s *Store) Root() string { return s.root }

// UploadDir is where partial uploads live (same volume as the blobs).
func (s *Store) UploadDir() string { return filepath.Join(s.root, uploadDir) }

// BlobPath is the file holding the blob with hash h (64 lower-case hex).
// Callers must treat it as read-only.
func (s *Store) BlobPath(h string) (string, error) {
	if !validHash(h) {
		return "", fmt.Errorf("cas: invalid sha256 %q", h)
	}
	return s.blobPath(h), nil
}

func (s *Store) blobPath(h string) string {
	return filepath.Join(s.root, blobDir, h[:2], h)
}

func validHash(h string) bool {
	if len(h) != 64 || strings.ToLower(h) != h {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

// Health is the storage service's state: an error when the volume can't be
// read or stored files are missing or damaged. detail is empty when ok (the
// health endpoint is unauthenticated, so it doesn't name the directory).
func (s *Store) Health() (ok bool, detail string) {
	if _, _, err := s.diskSpace(s.root); err != nil {
		return false, "content store unavailable: " + err.Error()
	}
	s.healthMu.Lock()
	p := s.problems
	s.healthMu.Unlock()
	if len(p) == 0 {
		return true, ""
	}
	const show = 3
	detail = fmt.Sprintf("%d stored files missing or damaged: %s", len(p), strings.Join(p[:min(show, len(p))], "; "))
	if len(p) > show {
		detail += "; …"
	}
	return false, detail
}
