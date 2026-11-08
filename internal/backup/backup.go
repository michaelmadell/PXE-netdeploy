package backup

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Layout at a destination (ADR 0020).
const (
	SnapshotsDir = "snapshots"
	BlobsDir     = "sha256"
	ManifestName = "manifest.json"
	BlobListName = "blobs.txt.gz"
	partial      = ".partial"
	idFormat     = "20060102T150405Z"
)

// Files copied from the data directory with the database.
var dataFiles = []string{"admin-cert.pem", "admin-key.pem"}

// Triggers.
const (
	TriggerScheduled = "scheduled"
	TriggerManual    = "manual"
	TriggerOffline   = "offline"
)

// Manifest describes one snapshot.
type Manifest struct {
	Format                string    `json:"format"`
	Version               int       `json:"version"`
	ID                    string    `json:"id"`
	CreatedAt             time.Time `json:"createdAt"`
	Trigger               string    `json:"trigger"`
	ServerVersion         string    `json:"serverVersion"`
	SchemaVersion         int       `json:"schemaVersion"`
	BlobCount             int       `json:"blobCount"`
	BlobBytes             int64     `json:"blobBytes"`
	DatabaseBytes         int64     `json:"databaseBytes"`
	SecretsKeyFingerprint string    `json:"secretsKeyFingerprint,omitempty"`
}

const manifestFormat = "netdeploy-backup"

// Source is what gets backed up.
type Source struct {
	Store          *store.Store
	Content        *cas.Store
	DataDir        string
	ServerVersion  string
	KeyFingerprint string // "" when no secrets key exists
}

// Options control one run.
type Options struct {
	Trigger   string
	Retention int // snapshots to keep; 0 keeps all
	Progress  func(percent int, message string)
	Now       func() time.Time
	Logger    *slog.Logger
}

// Result summarises a run.
type Result struct {
	Manifest        Manifest
	CopiedBlobs     int
	CopiedBytes     int64
	PrunedSnapshots []string
	PrunedBlobs     int
}

// ErrDamaged: a stored blob didn't match its hash while being copied.
var ErrDamaged = errors.New("backup: stored file is damaged")

// Run makes one backup at dest.
func Run(ctx context.Context, src Source, dest Dest, o Options) (Result, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Progress == nil {
		o.Progress = func(int, string) {}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	var res Result

	// Hold the sweep before the snapshot, so every blob it references stays
	// until copied.
	release := src.Content.HoldSweep()
	defer release()

	start := o.Now().UTC()
	id := start.Format(idFormat)
	o.Progress(0, "Snapshotting the database")
	tmp, err := os.MkdirTemp(src.DataDir, "backup-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(tmp)
	dbPath := filepath.Join(tmp, store.FileName)
	if err := src.Store.Snapshot(ctx, dbPath); err != nil {
		return res, err
	}
	schema, blobs, err := store.SnapshotContents(ctx, dbPath)
	if err != nil {
		return res, err
	}
	// In the copy, this backup has succeeded and is the last success.
	if schema >= 7 {
		st := Status{LastSuccessAt: &start, LastSnapshot: id}
		if err := store.PrepareSnapshot(ctx, dbPath, KindBackup, "Backed up (this snapshot)", start, map[string]any{stateStatus: st}); err != nil {
			return res, err
		}
	}
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		return res, err
	}

	o.Progress(2, "Checking the destination")
	for _, d := range []string{SnapshotsDir, BlobsDir} {
		if err := dest.MkdirAll(d); err != nil {
			return res, fmt.Errorf("create %s at %s: %w", d, dest, err)
		}
	}
	if _, err := dest.ReadDir(path.Join(SnapshotsDir, id)); err == nil {
		return res, fmt.Errorf("a snapshot %s already exists at %s", id, dest)
	}
	cleanPartials(dest, o.Logger)
	have, err := listBlobs(dest, prefixesOf(blobs))
	if err != nil {
		return res, err
	}
	var todo []store.BlobRef
	var todoBytes, total int64
	for _, b := range blobs {
		total += b.Size
		if have[b.SHA256] != b.Size {
			todo = append(todo, b)
			todoBytes += b.Size
		}
	}
	o.Logger.Info("backup started", "destination", dest.String(), "snapshot", id, "blobs", len(blobs), "to_copy", len(todo), "bytes_to_copy", todoBytes)

	// Copy missing blobs (5-90%).
	buf := make([]byte, 1<<20)
	var done int64
	for i, b := range todo {
		msg := fmt.Sprintf("Copying files (%d of %d)", i+1, len(todo))
		err := copyBlob(ctx, src.Content, dest, b, buf, func(n int64) {
			done += n
			o.Progress(5+int(85*done/max(todoBytes, 1)), msg)
		})
		if err != nil {
			return res, err
		}
		res.CopiedBlobs++
		res.CopiedBytes += b.Size
	}

	// The snapshot directory, published by renaming when complete.
	o.Progress(91, "Writing the snapshot")
	snap := path.Join(SnapshotsDir, id)
	work := snap + partial
	if err := dest.MkdirAll(work); err != nil {
		return res, err
	}
	if err := putFile(dest, path.Join(work, store.FileName), dbPath); err != nil {
		return res, err
	}
	for _, f := range dataFiles {
		if err := putFile(dest, path.Join(work, f), filepath.Join(src.DataDir, f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return res, err
		}
	}
	if err := putBlobList(dest, path.Join(work, BlobListName), blobs); err != nil {
		return res, err
	}
	res.Manifest = Manifest{Format: manifestFormat, Version: 1, ID: id, CreatedAt: start, Trigger: o.Trigger,
		ServerVersion: src.ServerVersion, SchemaVersion: schema, BlobCount: len(blobs), BlobBytes: total,
		DatabaseBytes: dbInfo.Size(), SecretsKeyFingerprint: src.KeyFingerprint}
	mb, _ := json.MarshalIndent(res.Manifest, "", "  ")
	if err := putBytes(dest, path.Join(work, ManifestName), mb); err != nil {
		return res, err
	}
	if err := dest.Rename(work, snap); err != nil {
		return res, fmt.Errorf("publish snapshot %s: %w", id, err)
	}

	if o.Retention > 0 {
		o.Progress(95, "Removing old backups")
		res.PrunedSnapshots, res.PrunedBlobs, err = prune(dest, o.Retention, o.Logger)
		if err != nil {
			// The new backup is complete; pruning is retried next time.
			o.Logger.Warn("backup retention", "err", err)
		}
	}
	o.Progress(100, fmt.Sprintf("Backed up %d files (%s new)", len(blobs), human(res.CopiedBytes)))
	o.Logger.Info("backup finished", "snapshot", id, "copied", res.CopiedBlobs, "copied_bytes", res.CopiedBytes,
		"pruned_snapshots", len(res.PrunedSnapshots), "pruned_blobs", res.PrunedBlobs)
	return res, nil
}

func prefixesOf(blobs []store.BlobRef) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range blobs {
		if p := b.SHA256[:2]; !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// listBlobs maps hash -> size for blobs at dest under the given prefixes,
// removing leftover partial files.
func listBlobs(dest Dest, prefixes []string) (map[string]int64, error) {
	have := map[string]int64{}
	for _, p := range prefixes {
		es, err := dest.ReadDir(path.Join(BlobsDir, p))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list %s/%s at %s: %w", BlobsDir, p, dest, err)
		}
		for _, e := range es {
			if strings.HasSuffix(e.Name, partial) {
				dest.Remove(path.Join(BlobsDir, p, e.Name))
				continue
			}
			if !e.Dir {
				have[e.Name] = e.Size
			}
		}
	}
	return have, nil
}

func allPrefixes() []string {
	out := make([]string, 256)
	for i := range out {
		out[i] = fmt.Sprintf("%02x", i)
	}
	return out
}

func cleanPartials(dest Dest, log *slog.Logger) {
	es, err := dest.ReadDir(SnapshotsDir)
	if err != nil {
		return
	}
	for _, e := range es {
		if strings.HasSuffix(e.Name, partial) {
			if err := dest.RemoveAll(path.Join(SnapshotsDir, e.Name)); err != nil {
				log.Warn("remove incomplete snapshot", "name", e.Name, "err", err)
			}
		}
	}
}

type progressWriter struct {
	w  io.Writer
	fn func(int64)
}

func (p progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.fn(int64(n))
	return n, err
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(b []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(b)
}

// copyBlob copies one blob, re-hashing it; a mismatch is reported to the
// content store and fails the backup.
func copyBlob(ctx context.Context, content *cas.Store, dest Dest, b store.BlobRef, buf []byte, progress func(int64)) error {
	src, err := content.BlobPath(b.SHA256)
	if err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		content.ReportDamaged(b.SHA256, "unreadable: "+err.Error())
		return fmt.Errorf("%w: %s: %v", ErrDamaged, b.SHA256, err)
	}
	defer f.Close()
	dir := path.Join(BlobsDir, b.SHA256[:2])
	if err := dest.MkdirAll(dir); err != nil {
		return err
	}
	tmp := path.Join(dir, b.SHA256+partial)
	w, err := dest.Create(tmp)
	if err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	h := sha256.New()
	n, err := io.CopyBuffer(progressWriter{io.MultiWriter(w, h), progress}, ctxReader{ctx, f}, buf)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		dest.Remove(tmp)
		return fmt.Errorf("copy %s: %w", b.SHA256, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != b.SHA256 || n != b.Size {
		dest.Remove(tmp)
		detail := fmt.Sprintf("reads as %s (%d bytes, expected %d)", got[:12], n, b.Size)
		content.ReportDamaged(b.SHA256, detail)
		return fmt.Errorf("%w: %s %s", ErrDamaged, b.SHA256, detail)
	}
	if err := dest.Rename(tmp, path.Join(dir, b.SHA256)); err != nil {
		return fmt.Errorf("publish %s: %w", b.SHA256, err)
	}
	return nil
}

func putFile(dest Dest, to, from string) error {
	f, err := os.Open(from)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := dest.Create(to)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

func putBytes(dest Dest, to string, b []byte) error {
	w, err := dest.Create(to)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

func putBlobList(dest Dest, to string, blobs []store.BlobRef) error {
	w, err := dest.Create(to)
	if err != nil {
		return err
	}
	z := gzip.NewWriter(w)
	bw := bufio.NewWriter(z)
	for _, b := range blobs {
		fmt.Fprintf(bw, "%s %d\n", b.SHA256, b.Size)
	}
	err = bw.Flush()
	if e := z.Close(); err == nil {
		err = e
	}
	if e := w.Close(); err == nil {
		err = e
	}
	return err
}

// readBlobList reads a snapshot's blob list.
func readBlobList(dest Dest, snap string) ([]store.BlobRef, error) {
	r, err := dest.Open(path.Join(SnapshotsDir, snap, BlobListName))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	z, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	var out []store.BlobRef
	sc := bufio.NewScanner(z)
	for sc.Scan() {
		h, n, ok := strings.Cut(sc.Text(), " ")
		size, err := strconv.ParseInt(n, 10, 64)
		if !ok || err != nil || len(h) != 64 {
			return nil, fmt.Errorf("%s: bad line %q", snap, sc.Text())
		}
		out = append(out, store.BlobRef{SHA256: h, Size: size})
	}
	return out, sc.Err()
}

// snapshotIDs lists complete snapshots, oldest first.
func snapshotIDs(dest Dest) ([]string, error) {
	es, err := dest.ReadDir(SnapshotsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range es {
		if e.Dir && !strings.HasSuffix(e.Name, partial) {
			if _, err := time.Parse(idFormat, e.Name); err == nil {
				ids = append(ids, e.Name)
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// prune keeps the newest keep snapshots, then deletes blobs none of the
// remaining snapshots list. If any list can't be read, no blob is deleted.
func prune(dest Dest, keep int, log *slog.Logger) ([]string, int, error) {
	ids, err := snapshotIDs(dest)
	if err != nil {
		return nil, 0, err
	}
	var pruned []string
	for len(ids) > keep {
		if err := dest.RemoveAll(path.Join(SnapshotsDir, ids[0])); err != nil {
			return pruned, 0, fmt.Errorf("remove snapshot %s: %w", ids[0], err)
		}
		pruned = append(pruned, ids[0])
		ids = ids[1:]
	}
	need := map[string]bool{}
	for _, id := range ids {
		list, err := readBlobList(dest, id)
		if err != nil {
			return pruned, 0, fmt.Errorf("read %s's file list (no files removed): %w", id, err)
		}
		for _, b := range list {
			need[b.SHA256] = true
		}
	}
	removed := 0
	for _, p := range allPrefixes() {
		es, err := dest.ReadDir(path.Join(BlobsDir, p))
		if err != nil {
			continue
		}
		for _, e := range es {
			if e.Dir || need[e.Name] {
				continue
			}
			if err := dest.Remove(path.Join(BlobsDir, p, e.Name)); err != nil {
				log.Warn("remove backup file", "name", e.Name, "err", err)
				continue
			}
			removed++
		}
	}
	return pruned, removed, nil
}

// List returns the manifests of the complete snapshots at dest, newest
// first. A snapshot whose manifest can't be read is skipped.
func List(dest Dest) ([]Manifest, error) {
	ids, err := snapshotIDs(dest)
	if err != nil {
		return nil, err
	}
	out := make([]Manifest, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		m, err := ReadManifest(dest, ids[i])
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// ReadManifest reads one snapshot's manifest.
func ReadManifest(dest Dest, id string) (Manifest, error) {
	r, err := dest.Open(path.Join(SnapshotsDir, id, ManifestName))
	if err != nil {
		return Manifest{}, err
	}
	defer r.Close()
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&m); err != nil || m.Format != manifestFormat {
		return Manifest{}, fmt.Errorf("snapshot %s: not a netdeploy backup manifest", id)
	}
	return m, nil
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
