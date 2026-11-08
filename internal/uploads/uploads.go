// Package uploads is netdeployd's tus 1.0.0 server (ADR 0017): core plus
// the creation, termination and expiration extensions, with files kept in
// the content store's uploads directory and progress in the uploads table.
// The SHA-256 is computed as bytes arrive, so a complete upload's hash is
// known without reading it again.
package uploads

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// MaxSize is the largest upload accepted: 64 GiB, above a 50 GB dual-layer
// Blu-ray image.
const MaxSize int64 = 64 << 30

// DefaultExpiry is storage.uploadExpiryHours' default.
const DefaultExpiry = 24 * time.Hour

const (
	checkpointEvery int64 = 64 << 20  // bytes between fsync + saved offset
	spaceCheckEvery int64 = 256 << 20 // bytes between free-space checks
)

// Options configure a Manager.
type Options struct {
	Store   *store.Store
	Content *cas.Store
	// Expiry returns storage.uploadExpiryHours as a duration. Nil:
	// DefaultExpiry.
	Expiry func() time.Duration
	Logger *slog.Logger
	Now    func() time.Time // nil: time.Now
}

// Manager serves the tus endpoints and owns upload files.
type Manager struct {
	opts Options
	log  *slog.Logger
	dir  string

	checkpointEvery int64
	spaceCheckEvery int64

	mu       sync.Mutex
	sessions map[string]*session // upload id -> request writing (or reading) it
}

// New returns a Manager. Call Start before serving.
func New(opts Options) *Manager {
	if opts.Expiry == nil {
		opts.Expiry = func() time.Duration { return DefaultExpiry }
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Manager{
		opts: opts, log: opts.Logger.With("svc", "uploads"), dir: opts.Content.UploadDir(),
		checkpointEvery: checkpointEvery, spaceCheckEvery: spaceCheckEvery,
		sessions: map[string]*session{},
	}
}

var idPattern = regexp.MustCompile(`^[0-9A-Z]{26}$`) // ULID

// Path is the file holding an upload's bytes. Callers must not write it.
func (m *Manager) Path(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("uploads: invalid id %q", id)
	}
	return filepath.Join(m.dir, id), nil
}

// Start reconciles files with rows (truncating incomplete files to their
// saved offset), expires old uploads, then expires hourly until ctx ends.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.recover(ctx); err != nil {
		return err
	}
	m.expire(ctx)
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.expire(ctx)
			}
		}
	}()
	return nil
}

func (m *Manager) recover(ctx context.Context) error {
	known := map[string]bool{}
	after := ""
	for {
		page, next, err := m.opts.Store.ListUploads(ctx, 200, after)
		if err != nil {
			return err
		}
		for _, u := range page {
			known[u.ID] = true
			m.recoverOne(ctx, u)
		}
		if next == "" {
			break
		}
		after = next
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return fmt.Errorf("uploads: read %s: %w", m.dir, err)
	}
	for _, e := range entries {
		if known[e.Name()] {
			continue
		}
		// A crash between creating the file and its row, or between
		// deleting the row and the file.
		if err := os.RemoveAll(filepath.Join(m.dir, e.Name())); err != nil {
			m.log.Warn("remove orphan upload file", "name", e.Name(), "err", err)
		} else {
			m.log.Info("removed orphan upload file", "name", e.Name())
		}
	}
	return nil
}

func (m *Manager) recoverOne(ctx context.Context, u store.Upload) {
	p, _ := m.Path(u.ID)
	log := m.log.With("upload_id", u.ID)
	fi, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist) || (err == nil && fi.Size() < u.Offset):
		// The bytes the row vouches for are gone: the upload can't be
		// resumed or imported.
		log.Error("upload file missing or short; deleting the upload", "offset", u.Offset)
		if err := m.opts.Store.DeleteUpload(ctx, u.ID); err != nil {
			log.Error("delete broken upload", "err", err)
		}
		os.Remove(p)
	case err != nil:
		log.Error("stat upload file", "err", err)
	case fi.Size() > u.Offset:
		// Bytes after the last checkpoint: the hash state doesn't cover
		// them, so drop them; HEAD tells the client to resend.
		if err := os.Truncate(p, u.Offset); err != nil {
			log.Error("truncate upload to its checkpoint", "err", err)
		} else {
			log.Info("upload truncated to its last checkpoint", "offset", u.Offset, "dropped", fi.Size()-u.Offset)
		}
	}
}

func (m *Manager) expire(ctx context.Context) {
	ids, err := m.opts.Store.ExpireUploads(ctx, m.opts.Now())
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("expire uploads", "err", err)
		}
		return
	}
	for _, id := range ids {
		m.removeFile(id)
		m.log.Info("upload expired", "upload_id", id)
	}
}

func (m *Manager) removeFile(id string) {
	p, err := m.Path(id)
	if err != nil {
		return
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// Retried at the next start (orphan file).
		m.log.Warn("remove upload file", "upload_id", id, "err", err)
	}
}

// Release records how an import job's use of an upload ended (ADR 0017):
// success keeps it until deleted; failure keeps it for a retry until the
// returned upload's ExpiresAt.
func (m *Manager) Release(ctx context.Context, uploadID, jobID string, succeeded bool) (store.Upload, error) {
	return m.opts.Store.ReleaseUpload(ctx, uploadID, jobID, succeeded, m.opts.Now().Add(m.opts.Expiry()))
}

// session is a request that holds an upload: a PATCH writing it, or a
// HEAD/DELETE that stopped one.
type session struct {
	stop func() // asks the holder to finish quickly
	done chan struct{}
}

// acquire stops any request holding the upload, waits for it to save its
// progress, and holds the upload until the returned release is called.
// tus clients send one request at a time, so a holder still running when a
// new request arrives is almost always a dead connection.
func (m *Manager) acquire(id string, stop func()) (release func()) {
	m.mu.Lock()
	for {
		old := m.sessions[id]
		if old == nil {
			break
		}
		m.mu.Unlock()
		old.stop()
		<-old.done
		m.mu.Lock()
	}
	s := &session{stop: stop, done: make(chan struct{})}
	m.sessions[id] = s
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		if m.sessions[id] == s {
			delete(m.sessions, id)
		}
		m.mu.Unlock()
		close(s.done)
	}
}
