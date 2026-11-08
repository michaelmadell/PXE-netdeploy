package imports

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/uploads"
)

type harness struct {
	st      *store.Store
	content *cas.Store
	up      *uploads.Manager
	runner  *jobs.Runner
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	content, err := cas.Open(ctx, cas.Options{Root: filepath.Join(dir, "content"), Store: st, Reserve: func() int64 { return 0 }, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	up := uploads.New(uploads.Options{Store: st, Content: content, Logger: quiet})
	runner := jobs.New(jobs.Options{Store: st, Logger: quiet, ProgressInterval: 10 * time.Millisecond})
	runner.Register(NewImages(ImagesOptions{Store: st, Content: content, Uploads: up, Logger: quiet}).Kind())
	rctx, cancel := context.WithCancel(ctx)
	if err := runner.Start(rctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); <-runner.Done() })
	return &harness{st: st, content: content, up: up, runner: runner}
}

// upload stores b as a complete upload (as the tus server would).
func (h *harness) upload(t *testing.T, name string, b []byte) store.Upload {
	t.Helper()
	ctx := context.Background()
	u, err := h.st.CreateUpload(ctx, store.NewUpload{Length: int64(len(b)), Filename: &name, HashState: []byte("x"), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := h.up.Path(u.ID)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	sha := hex.EncodeToString(sum[:])
	if err := h.st.CheckpointUpload(ctx, u.ID, store.UploadCheckpoint{Offset: int64(len(b)), HashState: []byte("x"), SHA256: &sha, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	u, _ = h.st.Upload(ctx, u.ID)
	return u
}

func (h *harness) importAndWait(t *testing.T, u store.Upload, retain bool, want string) (store.Image, store.Job) {
	t.Helper()
	ctx := context.Background()
	im, job, err := h.st.CreateImageImport(ctx, store.NewImageImport{Name: "test", UploadID: u.ID, RetainISO: retain}, KindImageImport)
	if err != nil {
		t.Fatal(err)
	}
	h.runner.Wake()
	deadline := time.Now().Add(30 * time.Second)
	for {
		j, _ := h.st.Job(ctx, job.ID)
		if j.Finished() {
			if j.Status != want {
				t.Fatalf("job %s: %s, want %s (error %s)", j.ID, j.Status, want, j.Error)
			}
			im, _ = h.st.Image(ctx, im.ID)
			return im, j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job still %s", j.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "media", "udf", "testdata", name+".iso.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, _ := gzip.NewReader(f)
	b, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestImportOtherMedia(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	iso := fixture(t, "udf102")
	u := h.upload(t, "fixture.iso", iso)

	im, job := h.importAndWait(t, u, true, store.JobSucceeded)
	if im.Status != store.ImageReady || *im.Kind != "other" || *im.VolumeLabel != "NDTEST_UDF102" || *im.SourceFilename != "fixture.iso" ||
		im.ISOBlob == nil || *im.ISOBlob != *u.SHA256 || job.Percent != 100 {
		t.Fatalf("image: %+v", im)
	}
	var details api.OtherImageDetails
	json.Unmarshal(im.Details, &details)
	if details.BootFiles == nil || len(*details.BootFiles) != 1 || (*details.BootFiles)[0].SourcePath != "EFI/Boot/bootx64.efi" ||
		(*details.BootFiles)[0].Signer != "unknown" || *(*details.BootFiles)[0].Architecture != "x64" {
		t.Errorf("details: %s", im.Details)
	}
	// Every file is in the tree and readable.
	tree := h.content.Tree(*im.TreeID)
	if b, err := fs.ReadFile(tree, "sources/sxs/file-149.cab"); err != nil || string(b) != "149" {
		t.Errorf("tree file: %q %v", b, err)
	}
	if b, err := fs.ReadFile(tree, "DEEP/A/B/C/D/LEAF.TXT"); err != nil || string(b) != "leaf" {
		t.Errorf("tree file (case-insensitive): %q %v", b, err)
	}
	// The retained ISO is a blob identical to the upload.
	p, _ := h.content.BlobPath(*im.ISOBlob)
	if b, _ := os.ReadFile(p); !bytes.Equal(b, iso) {
		t.Error("retained ISO differs")
	}
	// The upload is kept until deleted.
	if u2, _ := h.st.Upload(ctx, u.ID); !u2.Imported || u2.ExpiresAt != nil || u2.InUse {
		t.Errorf("upload after import: %+v", u2)
	}
	// Deleting the upload doesn't touch the retained ISO.
	h.st.DeleteUpload(ctx, u.ID)
	up, _ := h.up.Path(u.ID)
	os.Remove(up)
	if b, _ := os.ReadFile(p); !bytes.Equal(b, iso) {
		t.Error("retained ISO lost with its upload")
	}
	// Nothing referenced is swept.
	if err := h.content.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(tree, "sources/big.bin"); err != nil {
		t.Errorf("after sweep: %v", err)
	}
}

func TestImportFailureKeepsUploadForRetry(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	junk := bytes.Repeat([]byte("not an iso "), 100_000)
	u := h.upload(t, "junk.iso", junk)
	im, job := h.importAndWait(t, u, false, store.JobFailed)
	var p struct{ Code, Detail string }
	json.Unmarshal(job.Error, &p)
	if p.Code != "unsupported_media" || !strings.Contains(p.Detail, "kept until") {
		t.Errorf("job error: %s", job.Error)
	}
	if im.Status != store.ImageFailed || !strings.Contains(string(im.Error), "kept until") {
		t.Errorf("image: %+v", im)
	}
	u2, _ := h.st.Upload(ctx, u.ID)
	if u2.InUse || u2.Imported || u2.ExpiresAt == nil {
		t.Errorf("upload after failure: %+v", u2)
	}
	// Staging is empty and nothing was stored.
	if n, _ := h.st.ContentBytes(ctx); n != 0 {
		t.Errorf("%d bytes stored by a failed import", n)
	}
}

func TestImportDeduplicates(t *testing.T) {
	h := newHarness(t)
	iso := fixture(t, "udf201")
	first, _ := h.importAndWait(t, h.upload(t, "a.iso", iso), false, store.JobSucceeded)
	_, _, err := h.st.CreateImageImport(context.Background(), store.NewImageImport{Name: "again", UploadID: h.upload(t, "b.iso", iso).ID}, KindImageImport)
	var dup *store.DuplicateImageError
	if !errorsAs(err, &dup) || dup.Image.ID != first.ID {
		t.Fatalf("second import: %v", err)
	}
}

// TestRealISO imports real Windows media when NETDEPLOY_TEST_ISO is set.
// It copies the whole ISO into a temporary content store.
func TestRealISO(t *testing.T) {
	src := os.Getenv("NETDEPLOY_TEST_ISO")
	if src == "" {
		t.Skip("set NETDEPLOY_TEST_ISO to a Windows ISO")
	}
	h := newHarness(t)
	ctx := context.Background()
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := f.Stat()
	// Link (or copy) the ISO in as an upload without reading it into memory.
	u, _ := h.st.CreateUpload(ctx, store.NewUpload{Length: st.Size(), HashState: []byte("x"), ExpiresAt: time.Now().Add(time.Hour)})
	p, _ := h.up.Path(u.ID)
	if err := os.Link(src, p); err != nil {
		out, _ := os.Create(p)
		io.Copy(out, f)
		out.Close()
	}
	f.Seek(0, 0)
	hsh := sha256.New()
	io.Copy(hsh, f)
	f.Close()
	sha := hex.EncodeToString(hsh.Sum(nil))
	h.st.CheckpointUpload(ctx, u.ID, store.UploadCheckpoint{Offset: st.Size(), HashState: []byte("x"), SHA256: &sha, ExpiresAt: time.Now().Add(time.Hour)})
	u, _ = h.st.Upload(ctx, u.ID)

	start := time.Now()
	im, _ := h.importAndWaitLong(t, u)
	t.Logf("imported in %v: kind %s, label %s, warnings %v", time.Since(start), *im.Kind, *im.VolumeLabel, im.Warnings)
	var d api.WindowsImageDetails
	json.Unmarshal(im.Details, &d)
	t.Logf("architecture %s, format %s, %d editions, boot indexes %d", d.Architecture, d.InstallImageFormat, len(d.Editions), len(d.BootImageIndexes))
	for _, b := range d.BootManagers {
		t.Logf("  %s %s %s", b.SourcePath, b.Signer, b.Sha256[:12])
	}
	found2023 := false
	for _, b := range d.BootManagers {
		if b.Signer == "windows-uefi-ca-2023" {
			found2023 = true
		}
	}
	if *im.Kind != "windows" || len(d.Editions) == 0 || !found2023 {
		t.Error("expected Windows media with a 2023-signed boot manager")
	}
}

func (h *harness) importAndWaitLong(t *testing.T, u store.Upload) (store.Image, store.Job) {
	t.Helper()
	ctx := context.Background()
	im, job, err := h.st.CreateImageImport(ctx, store.NewImageImport{Name: "real", UploadID: u.ID}, KindImageImport)
	if err != nil {
		t.Fatal(err)
	}
	h.runner.Wake()
	for {
		j, _ := h.st.Job(ctx, job.ID)
		if j.Finished() {
			if j.Status != store.JobSucceeded {
				t.Fatalf("job: %s %s", j.Status, j.Error)
			}
			im, _ = h.st.Image(ctx, im.ID)
			return im, j
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }
