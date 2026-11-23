package boot

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/ipxe"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// linuxImage adds a ready Linux image with a kept ISO and a Linux profile.
func (f *profileFixture) linuxImage(t *testing.T) (imageID, profileID, iso string) {
	t.Helper()
	name, sha := "u.iso", strings.Repeat("9", 64)
	u, _ := f.st.CreateUpload(f.ctx, store.NewUpload{Length: 10, Filename: &name, HashState: []byte("h"), ExpiresAt: time.Now().Add(time.Hour)})
	f.st.CheckpointUpload(f.ctx, u.ID, store.UploadCheckpoint{Offset: 10, HashState: []byte("h"), SHA256: &sha, ExpiresAt: time.Now().Add(time.Hour)})
	im, _, err := f.st.CreateImageImport(f.ctx, store.NewImageImport{Name: "Ubuntu", UploadID: u.ID}, "image.import")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := f.st.InsertTree(f.ctx, store.NewTree{Kind: store.TreeImage,
		Entries: []store.NewTreeEntry{{Path: "casper/vmlinuz", Fold: "casper/vmlinuz", SHA256: strings.Repeat("8", 64), Size: 1},
			{Path: "iso", Fold: "iso", SHA256: sha, Size: 10}}}) // a blob row for the ISO
	if err != nil {
		t.Fatal(err)
	}
	f.trees[tree] = "ubuntu"
	if err := f.st.CompleteImageImport(f.ctx, im.ID, store.ImportResult{Kind: "linux", TreeID: tree, ISOBlob: &sha, ForceRetainISO: true,
		Details: json.RawMessage(`{"kind":"linux","family":"ubuntu","architecture":"x64","deployable":true}`)}); err != nil {
		t.Fatal(err)
	}
	p, err := f.st.CreateProfile(f.ctx, store.ProfileInput{Kind: store.ProfileLinux, Name: "Ubuntu", InstallImageID: im.ID})
	if err != nil {
		t.Fatal(err)
	}
	return im.ID, p.ID, sha
}

func TestLinuxProfileDecision(t *testing.T) {
	f := newProfileFixture(t)
	b := f.booter()
	imageID, pid, _ := f.linuxImage(t)
	f.defProf = pid

	// PXE clients get no answer; HTTP Boot clients the iPXE chain.
	if _, ok, why := b.Boot(f.ctx, Request{MAC: "00:15:5d:00:00:09", Arch: ArchUEFIX64}); ok || !strings.Contains(why, "needs HTTP(S) Boot") {
		t.Errorf("PXE: ok=%v %q", ok, why)
	}
	a, ok, why := b.Boot(f.ctx, Request{MAC: "00:15:5d:00:00:09", Arch: ArchUEFIX64HTTP})
	if !ok || a.File != ipxe.ShimPath("x64") || a.Linux == nil || a.Linux.ImageID != imageID || a.Linux.Armed || a.Root != nil {
		t.Fatalf("HTTP Boot: %+v %q", a, why)
	}

	// An armed deployment of the profile: Armed.
	c, _ := f.st.CreateClient(f.ctx, store.ClientInput{MAC: "00:15:5d:00:00:09"})
	snap, _ := json.Marshal(map[string]any{"kind": "linux", "name": "Ubuntu", "installImageId": imageID})
	if _, err := f.st.CreateDeployment(f.ctx, store.NewDeployment{ClientID: c.ID, ProfileID: pid, ProfileSnapshot: snap, Origin: store.OriginScheduled}); err != nil {
		t.Fatal(err)
	}
	if a, ok, _ := b.Boot(f.ctx, Request{MAC: c.MAC, Arch: ArchUEFIX64HTTP}); !ok || a.Linux == nil || !a.Linux.Armed {
		t.Errorf("armed: %+v", a)
	}
}

func TestLinuxContent(t *testing.T) {
	f := newProfileFixture(t)
	imageID, _, iso := f.linuxImage(t)
	isoFile := filepath.Join(t.TempDir(), "iso")
	os.WriteFile(isoFile, []byte("ISO DATA"), 0o644)
	b := f.booter()
	b.Tree = func(id string) fs.FS { return fstest.MapFS{"casper/vmlinuz": {Data: []byte(f.trees[id])}} }
	b.BlobPath = func(sha string) (string, error) {
		if sha != iso {
			t.Errorf("blob %s", sha)
		}
		return isoFile, nil
	}
	root, err := b.LinuxContent(imageID)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := fs.ReadFile(root, "casper/vmlinuz"); string(got) != "ubuntu" {
		t.Errorf("tree file %q", got)
	}
	if got, _ := fs.ReadFile(root, "image.iso"); string(got) != "ISO DATA" {
		t.Errorf("iso %q", got)
	}
	// Windows images and unknown ids aren't served.
	for _, id := range []string{f.image, "nope"} {
		if _, err := b.LinuxContent(id); err == nil {
			t.Errorf("%s served", id)
		}
	}
}
