package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDriverPackageLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	u := completeUpload(t, s, hash('e'), "i210.zip")
	p, job, err := s.CreateDriverImport(ctx, "Intel I210", u.ID, "driverPackage.import")
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != ImageImporting || *p.SourceFilename != "i210.zip" || *job.ResourceID != p.ID {
		t.Fatalf("created: %+v", p)
	}
	var dup *DuplicatePackageError
	if _, _, err := s.CreateDriverImport(ctx, "again", completeUpload(t, s, hash('e'), "copy.zip").ID, "driverPackage.import"); !errors.As(err, &dup) {
		t.Errorf("duplicate: %v", err)
	}

	tree, _ := s.InsertTree(ctx, NewTree{Kind: TreeDriverPackage, Entries: []NewTreeEntry{{Path: "e1r68x64.inf", Fold: "e1r68x64.inf", SHA256: hash('f'), Size: 1}}})
	date := time.Date(2024, 6, 21, 0, 0, 0, 0, time.UTC)
	infs := []NewDriverINF{
		{Path: "PRO1000/Winx64/W11/e1r68x64.inf", Class: "Net", Role: "network", Provider: "Intel", Version: "13.0.14.0", DriverDate: &date,
			Architectures: []string{"x64"}, TargetOSVersions: []string{"10.0...22000"}, Signature: "whql", Signer: "CN=Microsoft Windows Hardware Compatibility Publisher",
			Enabled: true, HardwareIDs: []string{`PCI\VEN_8086&DEV_1533`, `PCI\VEN_8086&DEV_157B`}},
		{Path: "x86/old.inf", Class: "Net", Role: "network", Signature: "unsigned", SignatureDetail: "no CatalogFile", Enabled: false},
		{Path: "storage/iaStorVD.inf", Class: "SCSIAdapter", Role: "storage", Architectures: []string{"x64"}, Signature: "whql", Enabled: true,
			HardwareIDs: []string{`PCI\VEN_8086&CC_010802`}},
	}
	if err := s.CompleteDriverImport(ctx, p.ID, tree, []string{"w"}, infs); err != nil {
		t.Fatal(err)
	}
	p, _ = s.DriverPackage(ctx, p.ID)
	if p.Status != ImageReady || len(p.INFs) != 3 || p.Version != 2 {
		t.Fatalf("ready: %+v", p)
	}
	i210 := p.INFs[0]
	if i210.Path != "PRO1000/Winx64/W11/e1r68x64.inf" || *i210.DriverDate != "2024-06-21" || len(i210.HardwareIDs) != 2 || !i210.Enabled {
		t.Errorf("inf: %+v", i210)
	}
	if trees, _, _ := s.SweepContent(ctx); trees != 0 {
		t.Error("sweep took the package's tree")
	}

	// Filters.
	for name, f := range map[string]PackageFilter{
		"class":            {Class: "net"},
		"role":             {Role: "storage"},
		"exact hwid":       {HardwareID: `pci\ven_8086&dev_1533`},
		"device's full id": {HardwareID: `PCI\VEN_8086&DEV_1533&SUBSYS_00008086&REV_03`},
	} {
		if list, _, err := s.ListDriverPackages(ctx, f); err != nil || len(list) != 1 || len(list[0].INFs) != 3 {
			t.Errorf("%s: %d packages, %v", name, len(list), err)
		}
	}
	for name, f := range map[string]PackageFilter{
		"no such class": {Class: "Display"},
		"partial id":    {HardwareID: `PCI\VEN_8086&DEV_15`},
	} {
		if list, _, _ := s.ListDriverPackages(ctx, f); len(list) != 0 {
			t.Errorf("%s matched", name)
		}
	}

	// Enable/disable INFs with If-Match.
	if _, err := s.UpdateDriverPackage(ctx, p.ID, nil, map[string]bool{p.INFs[1].ID: true}, 1); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("stale: %v", err)
	}
	if _, err := s.UpdateDriverPackage(ctx, p.ID, nil, map[string]bool{"nope": true}, 2); !errors.Is(err, ErrUnknownINF) {
		t.Errorf("unknown INF: %v", err)
	}
	name := "Intel I210 (W11)"
	if p, err = s.UpdateDriverPackage(ctx, p.ID, &name, map[string]bool{p.INFs[0].ID: false}, 2); err != nil || p.INFs[0].Enabled || p.Name != name {
		t.Fatalf("update: %+v %v", p, err)
	}

	// Groups.
	if _, err := s.CreateDriverGroup(ctx, DriverGroupInput{Name: "x", PackageIDs: []string{"nope"}}); !errors.Is(err, ErrUnknownPackage) {
		t.Errorf("group with unknown package: %v", err)
	}
	g, err := s.CreateDriverGroup(ctx, DriverGroupInput{Name: "NICs", Description: "lab", PackageIDs: []string{p.ID}})
	if err != nil || len(g.PackageIDs) != 1 || g.Version != 1 {
		t.Fatalf("group: %+v %v", g, err)
	}
	if err := s.DeleteDriverPackage(ctx, p.ID); !errors.Is(err, ErrPackageInUse) {
		t.Errorf("delete package in group: %v", err)
	}
	if g, err = s.ReplaceDriverGroup(ctx, g.ID, DriverGroupInput{Name: "NICs", PackageIDs: []string{}}, 1); err != nil || len(g.PackageIDs) != 0 {
		t.Fatalf("replace group: %+v %v", g, err)
	}
	if list, _, _ := s.ListDriverGroups(ctx, 0, ""); len(list) != 1 {
		t.Errorf("groups: %d", len(list))
	}
	if err := s.DeleteDriverPackage(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if trees, _, _ := s.SweepContent(ctx); trees != 1 {
		t.Errorf("tree not swept after delete")
	}
	if err := s.DeleteDriverGroup(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
}
