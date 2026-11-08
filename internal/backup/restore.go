package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// RestoreOptions control Restore.
type RestoreOptions struct {
	DataDir    string
	ContentDir string
	Snapshot   string // empty: the newest
	// Force replaces an existing database (moved aside as
	// netdeploy.db.pre-restore-<time>).
	Force bool
	// Log reports progress lines.
	Log func(format string, args ...any)
}

// ErrNotEmpty: the data directory already has a database.
var ErrNotEmpty = errors.New("backup: the data directory already has a database (use -force to replace it)")

// Restore copies a snapshot into a data and content directory (ADR 0020).
// The server must not be running (the caller holds the data lock).
func Restore(ctx context.Context, src Dest, o RestoreOptions) (Manifest, error) {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	id := o.Snapshot
	if id == "" {
		ids, err := snapshotIDs(src)
		if err != nil {
			return Manifest{}, err
		}
		if len(ids) == 0 {
			return Manifest{}, fmt.Errorf("no backups at %s", src)
		}
		id = ids[len(ids)-1]
	}
	m, err := ReadManifest(src, id)
	if err != nil {
		return Manifest{}, err
	}
	if m.SchemaVersion > store.SchemaVersion() {
		return m, fmt.Errorf("backup %s is from a newer server (schema %d, this one knows %d): restore it with that version or newer",
			id, m.SchemaVersion, store.SchemaVersion())
	}
	blobs, err := readBlobList(src, id)
	if err != nil {
		return m, err
	}
	o.Log("Restoring backup %s (%s, server %s): %d files, %s", id, m.CreatedAt.Local().Format(time.DateTime), m.ServerVersion, len(blobs), human(m.BlobBytes))

	db := filepath.Join(o.DataDir, store.FileName)
	if _, err := os.Stat(db); err == nil {
		if !o.Force {
			return m, ErrNotEmpty
		}
		aside := fmt.Sprintf("%s.pre-restore-%s", db, time.Now().UTC().Format(idFormat))
		if err := os.Rename(db, aside); err != nil {
			return m, err
		}
		o.Log("Moved the existing database to %s", aside)
	}
	for _, sfx := range []string{"-wal", "-shm"} {
		os.Remove(db + sfx)
	}
	if err := os.MkdirAll(o.DataDir, 0o700); err != nil {
		return m, err
	}

	// Content first, so a restore interrupted here leaves no database that
	// references missing files.
	buf := make([]byte, 1<<20)
	copied, skipped := 0, 0
	for i, b := range blobs {
		if err := ctx.Err(); err != nil {
			return m, err
		}
		dst := filepath.Join(o.ContentDir, "sha256", b.SHA256[:2], b.SHA256)
		if fi, err := os.Stat(dst); err == nil && fi.Size() == b.Size {
			skipped++
			continue
		}
		if err := fetchBlob(src, b, dst, buf); err != nil {
			return m, err
		}
		copied++
		if (i+1)%500 == 0 {
			o.Log("  %d of %d files", i+1, len(blobs))
		}
	}
	o.Log("Content: %d files copied, %d already present", copied, skipped)

	for _, f := range dataFiles {
		err := getFile(src, path.Join(SnapshotsDir, id, f), filepath.Join(o.DataDir, f))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return m, err
		}
	}
	if err := getFile(src, path.Join(SnapshotsDir, id, store.FileName), db+partial); err != nil {
		return m, err
	}
	if err := os.Rename(db+partial, db); err != nil {
		return m, err
	}
	o.Log("Database restored")
	return m, nil
}

func fetchBlob(src Dest, b store.BlobRef, dst string, buf []byte) error {
	r, err := src.Open(path.Join(BlobsDir, b.SHA256[:2], b.SHA256))
	if err != nil {
		return fmt.Errorf("backup file %s: %w", b.SHA256, err)
	}
	defer r.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + partial
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(f, h), r, buf)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && (n != b.Size || hex.EncodeToString(h.Sum(nil)) != b.SHA256) {
		err = fmt.Errorf("backup file %s is damaged (%d bytes, expected %d)", b.SHA256, n, b.Size)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func getFile(src Dest, from, to string) error {
	r, err := src.Open(from)
	if err != nil {
		return err
	}
	defer r.Close()
	f, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
