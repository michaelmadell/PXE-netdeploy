package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// completeUpload makes a fully received upload with the given hash.
func completeUpload(t *testing.T, s *Store, sha string, name string) Upload {
	t.Helper()
	ctx := context.Background()
	u, err := s.CreateUpload(ctx, NewUpload{Length: 10, Filename: &name, HashState: []byte("h"), ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckpointUpload(ctx, u.ID, UploadCheckpoint{Offset: 10, HashState: []byte("h"), SHA256: &sha, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	u, _ = s.Upload(ctx, u.ID)
	return u
}

func TestImageImportLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	iso := hash('a')
	u := completeUpload(t, s, iso, "Win11.iso")

	im, job, err := s.CreateImageImport(ctx, NewImageImport{Name: "Win11 25H2", UploadID: u.ID, RetainISO: true}, "image.import")
	if err != nil {
		t.Fatal(err)
	}
	if im.Status != ImageImporting || im.Kind != nil || *im.SourceFilename != "Win11.iso" || im.SHA256 != iso || im.SizeBytes != 10 ||
		job.Status != JobQueued || *job.ResourceID != im.ID || string(job.Params) != `{"uploadId":"`+u.ID+`"}` {
		t.Fatalf("created: %+v / %+v", im, job)
	}
	if u2, _ := s.Upload(ctx, u.ID); !u2.InUse {
		t.Error("upload not claimed")
	}
	// Same ISO again: points at the existing image.
	var dup *DuplicateImageError
	u2 := completeUpload(t, s, iso, "copy.iso")
	if _, _, err := s.CreateImageImport(ctx, NewImageImport{Name: "again", UploadID: u2.ID}, "image.import"); !errors.As(err, &dup) || dup.Image.ID != im.ID {
		t.Fatalf("duplicate: %v", err)
	}
	if fj, err := s.CreateFinishedJob(ctx, "image.import", im.ID, "already imported"); err != nil || fj.Status != JobSucceeded || fj.Percent != 100 {
		t.Fatalf("finished job: %+v %v", fj, err)
	}
	if err := s.DeleteImage(ctx, im.ID); !errors.Is(err, ErrImageImporting) {
		t.Errorf("delete while importing: %v", err)
	}

	// Complete: the tree, the ISO blob and the boot manager blob are owned.
	tree, err := s.InsertTree(ctx, NewTree{Kind: TreeImage,
		Entries: []NewTreeEntry{{Path: "efi/boot/bootx64.efi", Fold: "efi/boot/bootx64.efi", SHA256: hash('b'), Size: 3}},
		Blobs:   []NewBlob{{SHA256: iso, Size: 10}, {SHA256: hash('c'), Size: 4}}})
	if err != nil {
		t.Fatal(err)
	}
	x64 := "x64"
	res := ImportResult{Kind: "windows", VolumeLabel: "CPBA_X64FRE_EN-GB_DV9", TreeID: tree, ISOBlob: &iso,
		Details: json.RawMessage(`{"kind":"windows"}`), Warnings: []string{"w"},
		BootManagers: []BootManager{
			{SourcePath: "efi/boot/bootx64.efi", SHA256: hash('b'), Signer: "windows-pca-2011", Architecture: &x64},
			{SourcePath: "boot.wim:2/Windows/Boot/EFI_EX/bootmgfw_EX.efi", SHA256: hash('c'), Signer: "windows-uefi-ca-2023", Architecture: &x64},
		}}
	if err := s.CompleteImageImport(ctx, im.ID, res); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteImageImport(ctx, im.ID, res); !errors.Is(err, ErrBadTransition) {
		t.Errorf("complete twice: %v", err)
	}
	im, _ = s.Image(ctx, im.ID)
	if im.Status != ImageReady || *im.Kind != "windows" || *im.VolumeLabel != "CPBA_X64FRE_EN-GB_DV9" || *im.ISOBlob != iso ||
		len(im.Warnings) != 1 || im.Version != 2 {
		t.Fatalf("ready: %+v", im)
	}
	if bms, _ := s.ImageBootManagers(ctx, im.ID); len(bms) != 2 || bms[0].Signer != "windows-uefi-ca-2023" {
		t.Errorf("boot managers: %+v", bms)
	}
	if trees, blobs, _ := s.SweepContent(ctx); trees != 0 || len(blobs) != 0 {
		t.Errorf("sweep took owned content: %d trees, %v", trees, blobs)
	}

	// Rename with If-Match.
	name := "Windows 11 25H2 en-GB"
	if _, err := s.UpdateImage(ctx, im.ID, &name, nil, 1); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale update: %v", err)
	}
	if im, err = s.UpdateImage(ctx, im.ID, &name, nil, 2); err != nil || im.Name != name || im.Version != 3 {
		t.Fatalf("update: %+v %v", im, err)
	}

	// Deleting the upload keeps the image (and its record of the ISO).
	s.StartJob(ctx, job.ID)
	s.FinishJob(ctx, job.ID, JobOutcome{Status: JobSucceeded})
	if err := s.DeleteUpload(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if im, _ = s.Image(ctx, im.ID); im.UploadID != nil || *im.SourceFilename != "Win11.iso" {
		t.Errorf("after upload delete: %+v", im)
	}

	// Delete the image: everything becomes garbage.
	if err := s.DeleteImage(ctx, im.ID); err != nil {
		t.Fatal(err)
	}
	trees, blobs, err := s.SweepContent(ctx)
	if err != nil || trees != 1 || len(blobs) != 3 {
		t.Errorf("sweep after delete: %d trees, %v, %v", trees, blobs, err)
	}
}

func TestImageImportFailure(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	u := completeUpload(t, s, hash('d'), "bad.iso")
	im, _, err := s.CreateImageImport(ctx, NewImageImport{Name: "bad", UploadID: u.ID}, "image.import")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FailImage(ctx, im.ID, json.RawMessage(`{"code":"unsupported_media","title":"x","status":422,"type":"about:blank"}`)); err != nil {
		t.Fatal(err)
	}
	im, _ = s.Image(ctx, im.ID)
	if im.Status != ImageFailed || !strings.Contains(string(im.Error), "unsupported_media") {
		t.Fatalf("failed: %+v", im)
	}
	// A failed image doesn't block importing the same ISO again.
	u2 := completeUpload(t, s, hash('d'), "bad.iso")
	if _, _, err := s.CreateImageImport(ctx, NewImageImport{Name: "retry", UploadID: u2.ID}, "image.import"); err != nil {
		t.Errorf("retry after failure: %v", err)
	}
	if err := s.DeleteImage(ctx, im.ID); err != nil {
		t.Errorf("delete failed image: %v", err)
	}
	// Upload problems.
	inc, _ := s.CreateUpload(ctx, NewUpload{Length: 5, HashState: []byte("h"), ExpiresAt: time.Now().Add(time.Hour)})
	if _, _, err := s.CreateImageImport(ctx, NewImageImport{Name: "x", UploadID: inc.ID}, "image.import"); !errors.Is(err, ErrUploadIncomplete) {
		t.Errorf("incomplete upload: %v", err)
	}
	if _, _, err := s.CreateImageImport(ctx, NewImageImport{Name: "x", UploadID: "nope"}, "image.import"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing upload: %v", err)
	}
}
