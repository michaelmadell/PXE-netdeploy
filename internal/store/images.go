package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Image statuses.
const (
	ImageImporting = "importing"
	ImageReady     = "ready"
	ImageFailed    = "failed"
)

var (
	// ErrImageImporting: the image is still importing (cancel its job first).
	ErrImageImporting = errors.New("store: image is importing")
)

// Image is an images row (ADR 0018).
type Image struct {
	ID             string
	Name           string
	Notes          string
	Kind           *string
	Status         string
	Error          json.RawMessage
	SourceFilename *string
	VolumeLabel    *string
	UploadID       *string
	SizeBytes      int64
	SHA256         string
	RetainISO      bool
	ISOBlob        *string
	TreeID         *string
	Details        json.RawMessage
	Warnings       []string
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BootManager is an image_boot_managers row.
type BootManager struct {
	SourcePath   string
	SHA256       string
	Signer       string
	Architecture *string
}

const imageCols = `id, name, notes, kind, status, error, source_filename, volume_label, upload_id, size_bytes, sha256,
	retain_iso, iso_blob, tree_id, details, warnings, version, created_at, updated_at`

func scanImage(r rowScanner) (Image, error) {
	var im Image
	var kind, errDoc, src, label, upload, iso, tree, details sql.NullString
	var warnings string
	var retain int
	var created, updated int64
	if err := r.Scan(&im.ID, &im.Name, &im.Notes, &kind, &im.Status, &errDoc, &src, &label, &upload, &im.SizeBytes, &im.SHA256,
		&retain, &iso, &tree, &details, &warnings, &im.Version, &created, &updated); err != nil {
		return Image{}, err
	}
	im.Kind, im.SourceFilename, im.VolumeLabel, im.UploadID = fromNullStr(kind), fromNullStr(src), fromNullStr(label), fromNullStr(upload)
	im.ISOBlob, im.TreeID = fromNullStr(iso), fromNullStr(tree)
	if errDoc.Valid {
		im.Error = json.RawMessage(errDoc.String)
	}
	if details.Valid {
		im.Details = json.RawMessage(details.String)
	}
	if err := json.Unmarshal([]byte(warnings), &im.Warnings); err != nil {
		return Image{}, err
	}
	im.RetainISO = retain == 1
	im.CreatedAt, im.UpdatedAt = fromMS(created), fromMS(updated)
	return im, nil
}

// NewImageImport is a POST /images.
type NewImageImport struct {
	Name      string
	UploadID  string
	RetainISO bool
}

// DuplicateImageError: a live image of the same ISO exists.
type DuplicateImageError struct{ Image Image }

func (e *DuplicateImageError) Error() string {
	return fmt.Sprintf("store: ISO already imported as image %s", e.Image.ID)
}

// CreateImageImport creates an importing image and its queued import job
// and claims the upload, in one transaction. A live image with the same
// SHA-256 gives *DuplicateImageError; the upload problems ErrNotFound,
// ErrUploadIncomplete and ErrUploadInUse.
func (s *Store) CreateImageImport(ctx context.Context, n NewImageImport, jobKind string) (Image, Job, error) {
	var imageID, jobID string
	err := s.tx(ctx, func(t *sql.Tx) error {
		u, err := s.uploadWhere(ctx, t, n.UploadID)
		if err != nil {
			return err
		}
		if !u.Complete() {
			return ErrUploadIncomplete
		}
		if dup, err := scanImage(t.QueryRowContext(ctx, `SELECT `+imageCols+` FROM images WHERE sha256 = ? AND status <> 'failed'`, *u.SHA256)); err == nil {
			return &DuplicateImageError{Image: dup}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: create image: %w", err)
		}
		imageID = newID()
		now := ms(s.now())
		if _, err := t.ExecContext(ctx, `INSERT INTO images (id, name, status, source_filename, upload_id, size_bytes, sha256, retain_iso, created_at, updated_at)
			VALUES (?, ?, 'importing', ?, ?, ?, ?, ?, ?, ?)`,
			imageID, n.Name, nullStr(u.Filename), u.ID, u.Length, *u.SHA256, boolInt(n.RetainISO), now, now); err != nil {
			return fmt.Errorf("store: create image: %w", err)
		}
		if jobID, err = s.insertJob(ctx, t, NewJob{Kind: jobKind, ResourceID: &imageID, Params: map[string]string{"uploadId": u.ID}}); err != nil {
			return err
		}
		return s.claimUpload(ctx, t, u.ID, jobID)
	})
	if err != nil {
		return Image{}, Job{}, err
	}
	im, err := s.Image(ctx, imageID)
	if err != nil {
		return Image{}, Job{}, err
	}
	j, err := s.Job(ctx, jobID)
	return im, j, err
}

// CreateFinishedJob records a job that had nothing to do (e.g. an import of
// media already imported): succeeded at once, pointing at resourceID.
func (s *Store) CreateFinishedJob(ctx context.Context, kind, resourceID, message string) (Job, error) {
	id, now := newID(), ms(s.now())
	_, err := s.w.ExecContext(ctx, `INSERT INTO jobs (id, kind, status, resource_id, percent, message, created_at, started_at, finished_at)
		VALUES (?, ?, 'succeeded', ?, 100, ?, ?, ?, ?)`, id, kind, resourceID, message, now, now, now)
	if err != nil {
		return Job{}, fmt.Errorf("store: create finished job: %w", err)
	}
	return s.Job(ctx, id)
}

// Image reads one image. ErrNotFound.
func (s *Store) Image(ctx context.Context, id string) (Image, error) {
	im, err := scanImage(s.r.QueryRowContext(ctx, `SELECT `+imageCols+` FROM images WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Image{}, ErrNotFound
	}
	if err != nil {
		return Image{}, fmt.Errorf("store: read image: %w", err)
	}
	return im, nil
}

// ImageFilter narrows ListImages.
type ImageFilter struct {
	Kind  string
	Limit int    // 1..200; zero means 50
	After string // cursor
}

// ListImages returns images oldest first and the next cursor.
func (s *Store) ListImages(ctx context.Context, f ImageFilter) ([]Image, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	where, args := []string{"id > ?"}, []any{f.After}
	if f.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, f.Kind)
	}
	args = append(args, limit+1)
	rows, err := s.r.QueryContext(ctx, `SELECT `+imageCols+` FROM images WHERE `+strings.Join(where, " AND ")+` ORDER BY id LIMIT ?`, args...)
	if err != nil {
		return nil, "", fmt.Errorf("store: list images: %w", err)
	}
	defer rows.Close()
	var out []Image
	for rows.Next() {
		im, err := scanImage(rows)
		if err != nil {
			return nil, "", fmt.Errorf("store: list images: %w", err)
		}
		out = append(out, im)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("store: list images: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// UpdateImage changes the name and notes if the version still matches. nil
// keeps a field. ErrNotFound, ErrVersionMismatch.
func (s *Store) UpdateImage(ctx context.Context, id string, name, notes *string, ifVersion int64) (Image, error) {
	res, err := s.w.ExecContext(ctx, `UPDATE images SET name = coalesce(?, name), notes = coalesce(?, notes),
		version = version + 1, updated_at = ? WHERE id = ? AND version = ?`, nullStr(name), nullStr(notes), ms(s.now()), id, ifVersion)
	if err != nil {
		return Image{}, fmt.Errorf("store: update image: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Image(ctx, id); err != nil {
			return Image{}, err
		}
		return Image{}, ErrVersionMismatch
	}
	return s.Image(ctx, id)
}

// DeleteImage deletes a ready or failed image (its boot manager rows go
// with it; a content store sweep reclaims the files). ErrNotFound,
// ErrImageImporting.
func (s *Store) DeleteImage(ctx context.Context, id string) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		var status string
		if err := t.QueryRowContext(ctx, `SELECT status FROM images WHERE id = ?`, id).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("store: delete image: %w", err)
		}
		if status == ImageImporting {
			return ErrImageImporting
		}
		if _, err := t.ExecContext(ctx, `DELETE FROM images WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: delete image: %w", err)
		}
		return nil
	})
}

// ImportResult is what a finished import records.
type ImportResult struct {
	Kind         string
	VolumeLabel  string
	TreeID       string
	ISOBlob      *string // set when the ISO was retained
	Details      json.RawMessage
	Warnings     []string
	BootManagers []BootManager
}

// CompleteImageImport marks an importing image ready with its content, in
// one transaction (run inside the content store's commit, so the tree and
// blobs are referenced before any sweep). ErrNotFound, ErrBadTransition.
func (s *Store) CompleteImageImport(ctx context.Context, id string, r ImportResult) error {
	warnings, err := json.Marshal(append([]string{}, r.Warnings...))
	if err != nil {
		return err
	}
	return s.tx(ctx, func(t *sql.Tx) error {
		res, err := t.ExecContext(ctx, `UPDATE images SET status = 'ready', kind = ?, volume_label = ?, tree_id = ?, iso_blob = ?,
			details = ?, warnings = ?, version = version + 1, updated_at = ? WHERE id = ? AND status = 'importing'`,
			r.Kind, nullIfEmpty(r.VolumeLabel), r.TreeID, nullStr(r.ISOBlob), string(r.Details), string(warnings), ms(s.now()), id)
		if err != nil {
			return fmt.Errorf("store: complete image: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, err := s.Image(ctx, id); err != nil {
				return err
			}
			return ErrBadTransition
		}
		for _, b := range r.BootManagers {
			if _, err := t.ExecContext(ctx, `INSERT INTO image_boot_managers (image_id, source_path, sha256, signer, architecture) VALUES (?, ?, ?, ?, ?)`,
				id, b.SourcePath, b.SHA256, b.Signer, nullStr(b.Architecture)); err != nil {
				return fmt.Errorf("store: record boot manager %s: %w", b.SourcePath, err)
			}
		}
		return nil
	})
}

// FailImage marks an importing image failed with a Problem. No-op if it
// isn't importing (already finished). ErrNotFound.
func (s *Store) FailImage(ctx context.Context, id string, problem json.RawMessage) error {
	_, err := s.w.ExecContext(ctx, `UPDATE images SET status = 'failed', error = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND status = 'importing'`, string(problem), ms(s.now()), id)
	if err != nil {
		return fmt.Errorf("store: fail image: %w", err)
	}
	_, err = s.Image(ctx, id)
	return err
}

// ImageBootManagers lists an image's boot managers.
func (s *Store) ImageBootManagers(ctx context.Context, id string) ([]BootManager, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT source_path, sha256, signer, architecture FROM image_boot_managers WHERE image_id = ? ORDER BY source_path`, id)
	if err != nil {
		return nil, fmt.Errorf("store: boot managers: %w", err)
	}
	defer rows.Close()
	var out []BootManager
	for rows.Next() {
		var b BootManager
		var arch sql.NullString
		if err := rows.Scan(&b.SourcePath, &b.SHA256, &b.Signer, &arch); err != nil {
			return nil, fmt.Errorf("store: boot managers: %w", err)
		}
		b.Architecture = fromNullStr(arch)
		out = append(out, b)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfEmpty(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
