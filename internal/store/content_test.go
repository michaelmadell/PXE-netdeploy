package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func hash(c byte) string { return strings.Repeat(string(c), 64) }

// ownTrees makes every tree in owners referenced, standing in for the owner
// tables later migrations add.
func ownTrees(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE test_owners (tree_id TEXT NOT NULL REFERENCES trees (id) ON DELETE RESTRICT) STRICT`,
		`DROP VIEW tree_refs`,
		`CREATE VIEW tree_refs (tree_id) AS SELECT tree_id FROM test_owners`,
	} {
		if _, err := s.w.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTreeInsertLookupSweep(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	ownTrees(t, s)

	kept, err := s.InsertTree(ctx, NewTree{Kind: TreeImage, Entries: []NewTreeEntry{
		{Path: "Boot/BCD", Fold: "boot/bcd", SHA256: hash('a'), Size: 10},
		{Path: "Boot/boot.sdi", Fold: "boot/boot.sdi", SHA256: hash('b'), Size: 20},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.w.ExecContext(ctx, `INSERT INTO test_owners VALUES (?)`, kept); err != nil {
		t.Fatal(err)
	}
	// Shares boot.sdi with the kept tree; owned by nothing.
	if _, err := s.InsertTree(ctx, NewTree{Kind: TreeBootImage, Entries: []NewTreeEntry{
		{Path: "boot.sdi", Fold: "boot.sdi", SHA256: hash('b'), Size: 20},
		{Path: "x.efi", Fold: "x.efi", SHA256: hash('c'), Size: 30},
	}}); err != nil {
		t.Fatal(err)
	}

	e, err := s.LookupTreeEntry(ctx, kept, "boot/bcd")
	if err != nil || e.Path != "Boot/BCD" || e.SHA256 != hash('a') || e.Size != 10 {
		t.Fatalf("lookup = %+v, %v", e, err)
	}
	if _, err := s.LookupTreeEntry(ctx, kept, "Boot/BCD"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unfolded lookup: %v, want ErrNotFound", err)
	}
	if n, _ := s.ContentBytes(ctx); n != 60 {
		t.Errorf("content bytes = %d, want 60 (boot.sdi counted once)", n)
	}

	trees, blobs, err := s.SweepContent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if trees != 1 || len(blobs) != 1 || blobs[0] != hash('c') {
		t.Errorf("sweep = %d trees, blobs %v; want 1 tree and only %s", trees, blobs, hash('c'))
	}
	if es, err := s.TreeEntries(ctx, kept); err != nil || len(es) != 2 {
		t.Errorf("kept tree entries = %v, %v", es, err)
	}
	// An owned tree can't be deleted from under its owner.
	if _, err := s.w.ExecContext(ctx, `DELETE FROM trees WHERE id = ?`, kept); err == nil {
		t.Error("deleting an owned tree succeeded")
	}
}

func TestTreeConstraints(t *testing.T) {
	ctx := context.Background()
	s, _ := testStore(t)
	cases := map[string]NewTreeEntry{
		"case-only duplicate": {Path: "a.txt", Fold: "a.txt", SHA256: hash('a')},
		"fold mismatch":       {Path: "B.txt", Fold: "B.txt", SHA256: hash('a')},
		"dotdot":              {Path: "x/../y", Fold: "x/../y", SHA256: hash('a')},
		"backslash":           {Path: `x\y`, Fold: `x\y`, SHA256: hash('a')},
		"leading slash":       {Path: "/x", Fold: "/x", SHA256: hash('a')},
		"bad hash":            {Path: "h", Fold: "h", SHA256: strings.Repeat("A", 64)},
	}
	for name, bad := range cases {
		entries := []NewTreeEntry{{Path: "A.txt", Fold: "a.txt", SHA256: hash('a'), Size: 1}, bad}
		_, err := s.InsertTree(ctx, NewTree{Kind: TreeImage, Entries: entries})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if name == "case-only duplicate" && !errors.Is(err, ErrDuplicatePath) {
			t.Errorf("%s: %v, want ErrDuplicatePath", name, err)
		}
	}
	// Nothing from the failed transactions remains.
	if n, _ := s.ContentBytes(ctx); n != 0 {
		t.Errorf("content bytes = %d after failed inserts", n)
	}
}
