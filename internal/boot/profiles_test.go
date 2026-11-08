package boot

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

type profileFixture struct {
	ctx     context.Context
	st      *store.Store
	image   string
	trees   map[string]string // tree id → marker file content
	defProf string
}

func newProfileFixture(t *testing.T) *profileFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &profileFixture{ctx: ctx, st: st, trees: map[string]string{}}

	name, sha := "w.iso", strings.Repeat("a", 64)
	u, _ := st.CreateUpload(ctx, store.NewUpload{Length: 10, Filename: &name, HashState: []byte("h"), ExpiresAt: time.Now().Add(time.Hour)})
	st.CheckpointUpload(ctx, u.ID, store.UploadCheckpoint{Offset: 10, HashState: []byte("h"), SHA256: &sha, ExpiresAt: time.Now().Add(time.Hour)})
	im, _, err := st.CreateImageImport(ctx, store.NewImageImport{Name: "Win11", UploadID: u.ID}, "image.import")
	if err != nil {
		t.Fatal(err)
	}
	tree := f.tree(t, "media")
	if err := st.CompleteImageImport(ctx, im.ID, store.ImportResult{Kind: "windows", TreeID: tree,
		Details: json.RawMessage(`{"kind":"windows","architecture":"x64","editions":[{"index":1,"name":"Pro"}]}`)}); err != nil {
		t.Fatal(err)
	}
	f.image = im.ID
	return f
}

// tree inserts a one-file tree whose file says marker.
func (f *profileFixture) tree(t *testing.T, marker string) string {
	t.Helper()
	sha := strings.Repeat(string("0123456789abcdef"[len(f.trees)%16]), 64)
	id, err := f.st.InsertTree(f.ctx, store.NewTree{Kind: store.TreeBootImage,
		Entries: []store.NewTreeEntry{{Path: "bootmgfw.efi", Fold: "bootmgfw.efi", SHA256: sha, Size: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	f.trees[id] = marker
	return id
}

// bootImage creates a boot image of arch, built (with a tree) or not.
func (f *profileFixture) bootImage(t *testing.T, name, arch string, built bool) store.BootImage {
	t.Helper()
	bi, err := f.st.CreateBootImage(f.ctx, store.BootImageInput{Name: name, SourceImageID: f.image, SourceIndex: 1, Architecture: arch,
		DriverGroupIDs: []string{}, ServerURLs: []string{"http://x"}, Builder: "auto", Bootmgr: "auto", TFTPBlockSize: 16384, TFTPWindowSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	if built {
		tree := f.tree(t, name)
		if err := f.st.AdoptBootImageBuild(f.ctx, bi.ID, store.BootBuild{TreeID: tree, InputHash: strings.Repeat("e", 64),
			Inputs: json.RawMessage(`{}`), BuiltWith: "wimlib", SelectedBootManager: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		bi, _ = f.st.BootImage(f.ctx, bi.ID)
	}
	return bi
}

func (f *profileFixture) profile(t *testing.T, name, bootImage string) string {
	t.Helper()
	p, err := f.st.CreateProfile(f.ctx, store.ProfileInput{Name: name, BootImageID: bootImage, InstallImageID: f.image, EditionIndex: 1,
		InstallMethod: "agent", Identity: json.RawMessage(`{"mode":"none"}`), BootOrderAfterDeploy: "disk-first"})
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func (f *profileFixture) booter() *Profiles {
	return &Profiles{Store: f.st,
		Tree:             func(id string) fs.FS { return fstest.MapFS{"marker": {Data: []byte(f.trees[id])}} },
		DefaultProfileID: func() string { return f.defProf }}
}

func marker(t *testing.T, a Answer) string {
	t.Helper()
	b, _ := fs.ReadFile(a.Root, "marker")
	return string(b)
}

func TestProfilesDecision(t *testing.T) {
	f := newProfileFixture(t)
	b := f.booter()
	x64 := Request{MAC: "00:15:5d:00:00:01", Arch: ArchUEFIX64}

	// Nothing to offer: no answer.
	if _, ok, why := b.Boot(f.ctx, x64); ok || !strings.Contains(why, "no profile to offer") {
		t.Fatalf("fresh install answered: %q", why)
	}

	// The default profile.
	defBI := f.bootImage(t, "default-pe", "x64", true)
	f.defProf = f.profile(t, "default", defBI.ID)
	a, ok, why := b.Boot(f.ctx, x64)
	if !ok || a.File != PXEBootFile || marker(t, a) != "default-pe" || a.Source != "default profile" || a.TreeID != *defBI.TreeID {
		t.Fatalf("default: %+v %q", a, why)
	}

	// The client's profile wins over the default.
	ownBI := f.bootImage(t, "own-pe", "x64", true)
	own := f.profile(t, "own", ownBI.ID)
	c, err := f.st.CreateClient(f.ctx, store.ClientInput{MAC: x64.MAC, ProfileID: &own})
	if err != nil {
		t.Fatal(err)
	}
	if a, ok, _ := b.Boot(f.ctx, x64); !ok || marker(t, a) != "own-pe" || a.Source != "client profile" {
		t.Fatalf("client profile: %+v", a)
	}

	// An armed deployment wins, using its snapshot's boot image.
	armedBI := f.bootImage(t, "armed-pe", "x64", true)
	snap, _ := json.Marshal(map[string]string{"bootImageId": armedBI.ID})
	d, err := f.st.CreateDeployment(f.ctx, store.NewDeployment{ClientID: c.ID, ProfileID: own, ProfileSnapshot: snap, Origin: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	if a, ok, _ := b.Boot(f.ctx, x64); !ok || marker(t, a) != "armed-pe" || a.Source != "deployment" {
		t.Fatalf("armed: %+v", a)
	}
	// Running (checked in): back to the client's profile (ADR 0013).
	if _, err := f.st.StartDeployment(f.ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if a, _, _ := b.Boot(f.ctx, x64); marker(t, a) != "own-pe" {
		t.Fatalf("running deployment still armed: %+v", a)
	}

	// No fallback past a broken choice.
	unbuilt := f.bootImage(t, "unbuilt", "x64", false)
	arm := f.bootImage(t, "arm-pe", "arm64", true)
	for name, c := range map[string]struct {
		bootImage string
		arch      Arch
		want      string
	}{
		"unbuilt":    {unbuilt.ID, ArchUEFIX64, "never been built"},
		"wrong arch": {arm.ID, ArchUEFIX64, "is arm64, the client is uefi-x64"},
		"http boot":  {ownBI.ID, ArchUEFIX64HTTP, "HTTP Boot"},
		"bios":       {ownBI.ID, ArchBIOS, "no boot images for bios"},
	} {
		p := f.profile(t, name, c.bootImage)
		cl, _ := f.st.ClientByMAC(f.ctx, x64.MAC)
		cl.ProfileID = &p
		if _, err := f.st.ReplaceClient(f.ctx, cl.ID, cl.ClientInput, cl.Version); err != nil {
			t.Fatal(err)
		}
		if _, ok, why := b.Boot(f.ctx, Request{MAC: x64.MAC, Arch: c.arch}); ok || !strings.Contains(why, c.want) {
			t.Errorf("%s: answered %v, reason %q", name, ok, why)
		}
	}

	// An unknown client gets the default; Default works without a MAC.
	if a, ok, _ := b.Boot(f.ctx, Request{MAC: "00:15:5d:00:00:99", Arch: ArchUEFIX64}); !ok || marker(t, a) != "default-pe" {
		t.Errorf("unknown client: %+v", a)
	}
	if a, ok, _ := b.Default(f.ctx, ArchUEFIX64); !ok || marker(t, a) != "default-pe" {
		t.Errorf("Default: %+v", a)
	}
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

func TestSessionTreeIDs(t *testing.T) {
	now := time.Now()
	s := &Sessions{Now: func() time.Time { return now }}
	s.Record(Session{Interface: "a", IP: mustAddr("10.0.0.1"), Answer: Answer{TreeID: "t1"}})
	s.Record(Session{Interface: "a", IP: mustAddr("10.0.0.2"), Answer: Answer{TreeID: "t1"}})
	s.Record(Session{Interface: "a", IP: mustAddr("10.0.0.3")})
	if got := s.TreeIDs(); len(got) != 1 || got[0] != "t1" {
		t.Errorf("TreeIDs %v", got)
	}
	now = now.Add(time.Hour)
	if got := s.TreeIDs(); len(got) != 0 {
		t.Errorf("expired sessions pinned %v", got)
	}
}
