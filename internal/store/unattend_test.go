package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestUnattendTemplates(t *testing.T) {
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

	tin := UnattendTemplateInput{Name: "t", Content: "<unattend/>", IdentityModes: []string{"none", "local"},
		VariableDefaults: map[string]string{"site": "ldn"}}
	tp, err := s.CreateUnattendTemplate(ctx, tin)
	if err != nil {
		t.Fatal(err)
	}
	if tp.VariableDefaults["site"] != "ldn" || tp.RequiredVariables == nil || len(tp.IdentityModes) != 2 {
		t.Fatalf("template %+v", tp)
	}
	if list, _, err := s.ListUnattendTemplates(ctx, 0, ""); err != nil || len(list) != 1 {
		t.Fatalf("list %v %v", list, err)
	}

	pin := ProfileInput{Name: "p", BootImageID: bi.ID, InstallImageID: im.ID, EditionIndex: 1, InstallMethod: "agent",
		Identity: json.RawMessage(`{"mode":"local"}`), BootOrderAfterDeploy: "disk-first", UnattendTemplateID: &tp.ID}
	p, err := s.CreateProfile(ctx, pin)
	if err != nil {
		t.Fatal(err)
	}
	if p.UnattendTemplateID == nil || *p.UnattendTemplateID != tp.ID {
		t.Fatalf("profile template %v", p.UnattendTemplateID)
	}
	bad := pin
	bad.Identity = json.RawMessage(`{"mode":"ad"}`)
	if _, err := s.CreateProfile(ctx, bad); !errors.Is(err, ErrTemplateIdentityMismatch) {
		t.Errorf("mode mismatch: %v", err)
	}
	nope := "nope"
	bad = pin
	bad.UnattendTemplateID = &nope
	if _, err := s.CreateProfile(ctx, bad); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown template: %v", err)
	}

	// Dropping a mode the profile uses, or deleting, is refused.
	drop := tin
	drop.IdentityModes = []string{"none"}
	if _, err := s.ReplaceUnattendTemplate(ctx, tp.ID, drop, tp.Version); !errors.Is(err, ErrTemplateInUse) {
		t.Errorf("drop mode: %v", err)
	}
	if _, err := s.ReplaceUnattendTemplate(ctx, tp.ID, tin, tp.Version+1); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("version: %v", err)
	}
	keep := tin
	keep.IdentityModes = []string{"local"}
	if tp, err = s.ReplaceUnattendTemplate(ctx, tp.ID, keep, tp.Version); err != nil || tp.Version != 2 {
		t.Errorf("replace: %v %+v", err, tp)
	}
	if err := s.DeleteUnattendTemplate(ctx, tp.ID); !errors.Is(err, ErrTemplateInUse) {
		t.Errorf("delete in use: %v", err)
	}
	if err := s.DeleteProfile(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUnattendTemplate(ctx, tp.ID); err != nil {
		t.Errorf("delete: %v", err)
	}
	if err := s.DeleteUnattendTemplate(ctx, tp.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: %v", err)
	}
}
