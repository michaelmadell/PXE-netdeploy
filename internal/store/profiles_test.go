package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestProfilesInUse(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	iso := hash('a')
	u := completeUpload(t, s, iso, "w.iso")
	im, _, _ := s.CreateImageImport(ctx, NewImageImport{Name: "Win11", UploadID: u.ID}, "image.import")
	tree, err := s.InsertTree(ctx, NewTree{Kind: TreeImage, Entries: []NewTreeEntry{{Path: "a", Fold: "a", SHA256: hash('b'), Size: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteImageImport(ctx, im.ID, ImportResult{Kind: "windows", TreeID: tree, Details: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	bi, err := s.CreateBootImage(ctx, BootImageInput{Name: "pe", SourceImageID: im.ID, SourceIndex: 1, Architecture: "x64",
		ServerURLs: []string{"http://x"}, Builder: "auto", Bootmgr: "auto", TFTPBlockSize: 16384, TFTPWindowSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := s.CreateDriverGroup(ctx, DriverGroupInput{Name: "g", PackageIDs: []string{}})
	in := ProfileInput{Name: "p", BootImageID: bi.ID, InstallImageID: im.ID, EditionIndex: 1, InstallMethod: "agent",
		Identity: json.RawMessage(`{"mode":"none"}`), BootOrderAfterDeploy: "disk-first", OSDriverGroupIDs: []string{g.ID}}
	p, err := s.CreateProfile(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if p.OSDriverGroupIDs[0] != g.ID || string(p.Identity) != `{"mode":"none"}` || p.Variables == nil {
		t.Fatalf("profile: %+v", p)
	}
	bad := in
	bad.BootImageID = "nope"
	if _, err := s.CreateProfile(ctx, bad); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown boot image: %v", err)
	}
	bad = in
	bad.OSDriverGroupIDs = []string{"nope"}
	if _, err := s.CreateProfile(ctx, bad); !errors.Is(err, ErrUnknownDriverGroup) {
		t.Errorf("unknown group: %v", err)
	}

	// What the profile uses can't be deleted.
	if err := s.DeleteBootImage(ctx, bi.ID); !errors.Is(err, ErrBootImageInUse) {
		t.Errorf("boot image: %v", err)
	}
	if err := s.DeleteImage(ctx, im.ID); !errors.Is(err, ErrImageInUse) {
		t.Errorf("image: %v", err)
	}
	if err := s.DeleteDriverGroup(ctx, g.ID); !errors.Is(err, ErrDriverGroupInUse) {
		t.Errorf("group: %v", err)
	}

	// Clients reference profiles by foreign key.
	nope := "nope"
	if _, err := s.CreateClient(ctx, ClientInput{MAC: "00:15:5d:00:00:01", ProfileID: &nope}); !errors.Is(err, ErrUnknownProfile) {
		t.Errorf("client with unknown profile: %v", err)
	}
	c, err := s.CreateClient(ctx, ClientInput{MAC: "00:15:5d:00:00:01", ProfileID: &p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProfile(ctx, p.ID); !errors.Is(err, ErrProfileInUse) {
		t.Errorf("profile used by a client: %v", err)
	}
	c.ProfileID = nil
	if _, err := s.ReplaceClient(ctx, c.ID, c.ClientInput, c.Version); err != nil {
		t.Fatal(err)
	}
	// The default profile is in use too.
	if _, err := s.w.ExecContext(ctx, `INSERT INTO settings (id, version, doc, updated_at) VALUES (1, 1, ?, 0)
		ON CONFLICT (id) DO UPDATE SET doc = excluded.doc`, `{"bootPrompt":{"defaultProfileId":"`+p.ID+`"}}`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProfile(ctx, p.ID); !errors.Is(err, ErrProfileInUse) {
		t.Errorf("default profile: %v", err)
	}
	s.w.ExecContext(ctx, `UPDATE settings SET doc = '{}'`)
	if err := s.DeleteProfile(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBootImage(ctx, bi.ID); err != nil {
		t.Errorf("boot image after the profile went: %v", err)
	}
}

// Trees boot sessions use survive a sweep (ADR 0024).
func TestSweepKeeps(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	tree, err := s.InsertTree(ctx, NewTree{Kind: TreeBootImage, Entries: []NewTreeEntry{{Path: "a", Fold: "a", SHA256: hash('c'), Size: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if trees, _, _ := s.SweepContent(ctx, tree); trees != 0 {
		t.Fatal("kept tree swept")
	}
	if trees, blobs, _ := s.SweepContent(ctx); trees != 1 || len(blobs) != 1 {
		t.Fatalf("unkept tree: %d trees, %v", trees, blobs)
	}
}
