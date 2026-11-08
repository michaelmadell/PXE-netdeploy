package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Content store rows (ADR 0015). The files themselves are managed by
// internal/cas; these functions only keep the manifest.

// TreeKind is what a tree holds.
type TreeKind string

// Tree kinds.
const (
	TreeImage         TreeKind = "image"
	TreeDriverPackage TreeKind = "driver-package"
	TreeBootImage     TreeKind = "boot-image"
)

// TreeEntry is one file in a tree.
type TreeEntry struct {
	Path   string // as stored, '/'-separated
	SHA256 string
	Size   int64
}

// NewTree is a tree to insert. Fold is computed by the caller (cas.Fold) so
// the Go and SQL rules can't drift silently: the CHECK on tree_entries.fold
// rejects a mismatch. Blobs are extra files stored with the tree but not in
// it (e.g. boot managers read out of a WIM); something must reference them
// (blob_refs) once the commit's attach step runs, or they are swept.
type NewTree struct {
	Kind    TreeKind
	Entries []NewTreeEntry
	Blobs   []NewBlob
}

// NewBlob is a blob recorded with a tree.
type NewBlob struct {
	SHA256 string
	Size   int64
}

// NewTreeEntry is a file in a NewTree.
type NewTreeEntry struct {
	Path   string
	Fold   string
	SHA256 string
	Size   int64
}

// ErrDuplicatePath: two entries in one tree have the same folded path.
var ErrDuplicatePath = errors.New("store: two files in the tree differ only in case")

// InsertTree records t's blobs (if new) and the tree, in one transaction,
// and returns the tree's id.
func (s *Store) InsertTree(ctx context.Context, t NewTree) (string, error) {
	id := newID()
	now := ms(s.now())
	var total int64
	for _, e := range t.Entries {
		total += e.Size
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO trees (id, kind, file_count, total_bytes, created_at) VALUES (?, ?, ?, ?, ?)`,
			id, string(t.Kind), len(t.Entries), total, now); err != nil {
			return fmt.Errorf("store: insert tree: %w", err)
		}
		blob, err := tx.PrepareContext(ctx, `INSERT INTO blobs (sha256, size, created_at) VALUES (?, ?, ?) ON CONFLICT (sha256) DO NOTHING`)
		if err != nil {
			return fmt.Errorf("store: insert tree: %w", err)
		}
		defer blob.Close()
		entry, err := tx.PrepareContext(ctx, `INSERT INTO tree_entries (tree_id, path, fold, sha256) VALUES (?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("store: insert tree: %w", err)
		}
		defer entry.Close()
		for _, b := range t.Blobs {
			if _, err := blob.ExecContext(ctx, b.SHA256, b.Size, now); err != nil {
				return fmt.Errorf("store: insert blob %s: %w", b.SHA256, err)
			}
		}
		for _, e := range t.Entries {
			if _, err := blob.ExecContext(ctx, e.SHA256, e.Size, now); err != nil {
				return fmt.Errorf("store: insert blob %s: %w", e.SHA256, err)
			}
			if _, err := entry.ExecContext(ctx, id, e.Path, e.Fold, e.SHA256); err != nil {
				if uniqueViolation(err, "tree_entries.") {
					return fmt.Errorf("%w: %q", ErrDuplicatePath, e.Path)
				}
				return fmt.Errorf("store: insert tree entry %q: %w", e.Path, err)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// LookupTreeEntry returns the file at fold (a folded path) in tree id.
// ErrNotFound if the tree has no such file.
func (s *Store) LookupTreeEntry(ctx context.Context, treeID, fold string) (TreeEntry, error) {
	var e TreeEntry
	err := s.r.QueryRowContext(ctx,
		`SELECT e.path, e.sha256, b.size FROM tree_entries e JOIN blobs b USING (sha256) WHERE e.tree_id = ? AND e.fold = ?`,
		treeID, fold).Scan(&e.Path, &e.SHA256, &e.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return TreeEntry{}, ErrNotFound
	}
	if err != nil {
		return TreeEntry{}, fmt.Errorf("store: lookup tree entry: %w", err)
	}
	return e, nil
}

// TreeEntries lists tree id's files ordered by path. ErrNotFound if the tree
// doesn't exist.
func (s *Store) TreeEntries(ctx context.Context, treeID string) ([]TreeEntry, error) {
	var one int
	if err := s.r.QueryRowContext(ctx, `SELECT 1 FROM trees WHERE id = ?`, treeID).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store: tree entries: %w", err)
	}
	rows, err := s.r.QueryContext(ctx,
		`SELECT e.path, e.sha256, b.size FROM tree_entries e JOIN blobs b USING (sha256) WHERE e.tree_id = ? ORDER BY e.path`, treeID)
	if err != nil {
		return nil, fmt.Errorf("store: tree entries: %w", err)
	}
	defer rows.Close()
	var out []TreeEntry
	for rows.Next() {
		var e TreeEntry
		if err := rows.Scan(&e.Path, &e.SHA256, &e.Size); err != nil {
			return nil, fmt.Errorf("store: tree entries: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Blobs calls fn for every blob row (startup check, orphan scan).
func (s *Store) Blobs(ctx context.Context, fn func(sha256 string, size int64) error) error {
	rows, err := s.r.QueryContext(ctx, `SELECT sha256, size FROM blobs`)
	if err != nil {
		return fmt.Errorf("store: list blobs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		var n int64
		if err := rows.Scan(&h, &n); err != nil {
			return fmt.Errorf("store: list blobs: %w", err)
		}
		if err := fn(h, n); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ContentBytes is the total size of all blobs.
func (s *Store) ContentBytes(ctx context.Context) (int64, error) {
	var n int64
	if err := s.r.QueryRowContext(ctx, `SELECT coalesce(sum(size), 0) FROM blobs`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: content bytes: %w", err)
	}
	return n, nil
}

// SweepContent deletes trees nothing refers to (tree_refs), then blobs no
// tree entry or owner refers to (blob_refs), and returns the deleted blobs'
// hashes so their files can be removed. One transaction.
func (s *Store) SweepContent(ctx context.Context) (trees int64, blobs []string, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM trees WHERE id NOT IN (SELECT tree_id FROM tree_refs WHERE tree_id IS NOT NULL)`)
		if err != nil {
			return fmt.Errorf("store: sweep trees: %w", err)
		}
		trees, _ = res.RowsAffected()
		rows, err := tx.QueryContext(ctx, `DELETE FROM blobs
			WHERE NOT EXISTS (SELECT 1 FROM tree_entries e WHERE e.sha256 = blobs.sha256)
			  AND sha256 NOT IN (SELECT sha256 FROM blob_refs WHERE sha256 IS NOT NULL)
			RETURNING sha256`)
		if err != nil {
			return fmt.Errorf("store: sweep blobs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				return fmt.Errorf("store: sweep blobs: %w", err)
			}
			blobs = append(blobs, h)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, nil, err
	}
	return trees, blobs, nil
}
