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

// Boot image states (boot_images.state; ADR 0022).
const (
	BootImageDraft    = "draft"
	BootImageBuilding = "building"
	BootImageBuilt    = "built"
	BootImageFailed   = "failed"
)

var (
	// ErrBuildInProgress: the boot image has a queued or running build.
	ErrBuildInProgress = errors.New("store: boot image build in progress")
	// ErrUnknownDriverGroup: a referenced driver group doesn't exist.
	ErrUnknownDriverGroup = errors.New("store: unknown driver group")
	// ErrImageInUse: a boot image is built from the image.
	ErrImageInUse = errors.New("store: image is used by a boot image")
	// ErrDriverGroupInUse: a boot image uses the driver group.
	ErrDriverGroupInUse = errors.New("store: driver group is used by a boot image")
)

// BootImageInput is a boot image's editable fields (BootImageInput with
// defaults applied).
type BootImageInput struct {
	Name               string
	SourceImageID      string
	SourceIndex        int
	Architecture       string
	DriverGroupIDs     []string
	AgentVersion       *string
	ServerURLs         []string
	Builder            string
	OptionalComponents []string
	Bootmgr            string
	TFTPBlockSize      int
	TFTPWindowSize     int
	ProduceHTTPBootISO bool
}

// BootImage is a boot_images row with its driver groups.
type BootImage struct {
	ID string
	BootImageInput
	State               string
	TreeID              *string
	InputHash           *string
	BuiltInputs         json.RawMessage
	BuiltWith           *string
	BuiltAt             *time.Time
	SelectedBootManager json.RawMessage
	Warnings            []string
	Error               json.RawMessage
	LastJobID           *string
	Version             int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// BootBuild is a finished build to record.
type BootBuild struct {
	TreeID              string
	InputHash           string
	Inputs              json.RawMessage
	BuiltWith           string
	SelectedBootManager json.RawMessage
	Warnings            []string
}

const bootImageCols = `id, name, source_image_id, source_index, architecture, agent_version, server_urls, builder, optional_components,
	bootmgr, tftp_block_size, tftp_window_size, produce_http_boot_iso, state, tree_id, input_hash, built_inputs, built_with, built_at,
	selected_boot_manager, warnings, error, last_job_id, version, created_at, updated_at`

func (s *Store) scanBootImage(ctx context.Context, q queryer, r rowScanner) (BootImage, error) {
	var b BootImage
	var agent, tree, hash, inputs, with, sbm, errDoc, job sql.NullString
	var urls, ocs, warnings string
	var iso int
	var builtAt sql.NullInt64
	var created, updated int64
	if err := r.Scan(&b.ID, &b.Name, &b.SourceImageID, &b.SourceIndex, &b.Architecture, &agent, &urls, &b.Builder, &ocs,
		&b.Bootmgr, &b.TFTPBlockSize, &b.TFTPWindowSize, &iso, &b.State, &tree, &hash, &inputs, &with, &builtAt,
		&sbm, &warnings, &errDoc, &job, &b.Version, &created, &updated); err != nil {
		return BootImage{}, err
	}
	b.AgentVersion, b.TreeID, b.InputHash, b.BuiltWith, b.LastJobID = fromNullStr(agent), fromNullStr(tree), fromNullStr(hash), fromNullStr(with), fromNullStr(job)
	b.BuiltAt = fromNullMS(builtAt)
	b.ProduceHTTPBootISO = iso == 1
	for _, j := range []struct {
		src string
		dst any
	}{{urls, &b.ServerURLs}, {ocs, &b.OptionalComponents}, {warnings, &b.Warnings}} {
		if err := json.Unmarshal([]byte(j.src), j.dst); err != nil {
			return BootImage{}, err
		}
	}
	if inputs.Valid {
		b.BuiltInputs = json.RawMessage(inputs.String)
	}
	if sbm.Valid {
		b.SelectedBootManager = json.RawMessage(sbm.String)
	}
	if errDoc.Valid {
		b.Error = json.RawMessage(errDoc.String)
	}
	b.CreatedAt, b.UpdatedAt = fromMS(created), fromMS(updated)
	rows, err := q.QueryContext(ctx, `SELECT group_id FROM boot_image_driver_groups WHERE boot_image_id = ? ORDER BY position`, b.ID)
	if err != nil {
		return BootImage{}, err
	}
	defer rows.Close()
	b.DriverGroupIDs = []string{}
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return BootImage{}, err
		}
		b.DriverGroupIDs = append(b.DriverGroupIDs, g)
	}
	return b, rows.Err()
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

func (in BootImageInput) columns() ([]any, error) {
	urls, err := jsonText(in.ServerURLs)
	if err != nil {
		return nil, err
	}
	ocs := in.OptionalComponents
	if ocs == nil {
		ocs = []string{}
	}
	ocText, err := jsonText(ocs)
	if err != nil {
		return nil, err
	}
	return []any{in.Name, in.SourceImageID, in.SourceIndex, in.Architecture, nullStr(in.AgentVersion), urls, in.Builder, ocText,
		in.Bootmgr, in.TFTPBlockSize, in.TFTPWindowSize, boolInt(in.ProduceHTTPBootISO)}, nil
}

func (s *Store) setBootImageGroups(ctx context.Context, t *sql.Tx, id string, groups []string) error {
	if _, err := t.ExecContext(ctx, `DELETE FROM boot_image_driver_groups WHERE boot_image_id = ?`, id); err != nil {
		return err
	}
	for i, g := range groups {
		var one int
		if err := t.QueryRowContext(ctx, `SELECT 1 FROM driver_groups WHERE id = ?`, g).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrUnknownDriverGroup, g)
			}
			return err
		}
		if _, err := t.ExecContext(ctx, `INSERT INTO boot_image_driver_groups (boot_image_id, group_id, position) VALUES (?, ?, ?)`, id, g, i); err != nil {
			if uniqueViolation(err, "") {
				return fmt.Errorf("%w: driver group %s listed twice", ErrDuplicate, g)
			}
			return fmt.Errorf("store: boot image driver group: %w", err)
		}
	}
	return nil
}

// CreateBootImage creates a draft boot image. ErrNotFound (source image),
// ErrUnknownDriverGroup, ErrDuplicate.
func (s *Store) CreateBootImage(ctx context.Context, in BootImageInput) (BootImage, error) {
	cols, err := in.columns()
	if err != nil {
		return BootImage{}, err
	}
	id := newID()
	err = s.tx(ctx, func(t *sql.Tx) error {
		var one int
		if err := t.QueryRowContext(ctx, `SELECT 1 FROM images WHERE id = ?`, in.SourceImageID).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		now := ms(s.now())
		args := append(append([]any{id}, cols...), now, now)
		if _, err := t.ExecContext(ctx, `INSERT INTO boot_images (id, name, source_image_id, source_index, architecture, agent_version,
			server_urls, builder, optional_components, bootmgr, tftp_block_size, tftp_window_size, produce_http_boot_iso, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'draft', ?, ?)`, args...); err != nil {
			return fmt.Errorf("store: create boot image: %w", err)
		}
		return s.setBootImageGroups(ctx, t, id, in.DriverGroupIDs)
	})
	if err != nil {
		return BootImage{}, err
	}
	return s.BootImage(ctx, id)
}

// BootImage reads one boot image. ErrNotFound.
func (s *Store) BootImage(ctx context.Context, id string) (BootImage, error) {
	b, err := s.scanBootImage(ctx, s.r, s.r.QueryRowContext(ctx, `SELECT `+bootImageCols+` FROM boot_images WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return BootImage{}, ErrNotFound
	}
	if err != nil {
		return BootImage{}, fmt.Errorf("store: read boot image: %w", err)
	}
	return b, nil
}

// ListBootImages returns boot images oldest first and the next cursor.
func (s *Store) ListBootImages(ctx context.Context, limit int, after string) ([]BootImage, string, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	ids, err := s.ids(ctx, `SELECT id FROM boot_images WHERE id > ? ORDER BY id LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("store: list boot images: %w", err)
	}
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = ids[limit-1]
	}
	out := make([]BootImage, 0, len(ids))
	for _, id := range ids {
		b, err := s.BootImage(ctx, id)
		if err != nil {
			return nil, "", err
		}
		out = append(out, b)
	}
	return out, next, nil
}

func (s *Store) ids(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// BootImagesUsing lists the ids of boot images whose driver groups include
// any of groupIDs or contain packageID (for bootImage.updated events when
// their drivers change).
func (s *Store) BootImagesUsing(ctx context.Context, groupIDs []string, packageID string) ([]string, error) {
	q := `SELECT DISTINCT b.boot_image_id FROM boot_image_driver_groups b
		LEFT JOIN driver_group_packages p ON p.group_id = b.group_id
		WHERE p.package_id = ?`
	args := []any{packageID}
	if len(groupIDs) > 0 {
		q += ` OR b.group_id IN (?` + strings.Repeat(", ?", len(groupIDs)-1) + `)`
		for _, g := range groupIDs {
			args = append(args, g)
		}
	}
	return s.ids(ctx, q+` ORDER BY 1`, args...)
}

// ReplaceBootImage replaces the inputs if the version still matches. The
// build being served is kept. ErrNotFound, ErrVersionMismatch,
// ErrUnknownDriverGroup, ErrDuplicate; ErrNotFound wrapped with "source
// image" for a missing source image.
func (s *Store) ReplaceBootImage(ctx context.Context, id string, in BootImageInput, ifVersion int64) (BootImage, error) {
	cols, err := in.columns()
	if err != nil {
		return BootImage{}, err
	}
	err = s.tx(ctx, func(t *sql.Tx) error {
		var v int64
		if err := t.QueryRowContext(ctx, `SELECT version FROM boot_images WHERE id = ?`, id).Scan(&v); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if v != ifVersion {
			return ErrVersionMismatch
		}
		var one int
		if err := t.QueryRowContext(ctx, `SELECT 1 FROM images WHERE id = ?`, in.SourceImageID).Scan(&one); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: source image %s", ErrNotFound, in.SourceImageID)
			}
			return err
		}
		args := append(cols, ms(s.now()), id)
		if _, err := t.ExecContext(ctx, `UPDATE boot_images SET name = ?, source_image_id = ?, source_index = ?, architecture = ?,
			agent_version = ?, server_urls = ?, builder = ?, optional_components = ?, bootmgr = ?, tftp_block_size = ?,
			tftp_window_size = ?, produce_http_boot_iso = ?, version = version + 1, updated_at = ? WHERE id = ?`, args...); err != nil {
			return fmt.Errorf("store: replace boot image: %w", err)
		}
		return s.setBootImageGroups(ctx, t, id, in.DriverGroupIDs)
	})
	if err != nil {
		return BootImage{}, err
	}
	return s.BootImage(ctx, id)
}

// DeleteBootImage deletes a boot image that isn't building or used by a
// profile (a content store sweep reclaims its build). ErrNotFound,
// ErrBuildInProgress, ErrBootImageInUse.
func (s *Store) DeleteBootImage(ctx context.Context, id string) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		var state string
		if err := t.QueryRowContext(ctx, `SELECT state FROM boot_images WHERE id = ?`, id).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if state == BootImageBuilding {
			return ErrBuildInProgress
		}
		if err := inUse(ctx, t, `SELECT count(*) FROM profiles WHERE boot_image_id = ?`, id, ErrBootImageInUse); err != nil {
			return err
		}
		_, err := t.ExecContext(ctx, `DELETE FROM boot_images WHERE id = ?`, id)
		return err
	})
}

// QueueBootImageBuild marks the boot image building and queues its build
// job, in one transaction. ErrNotFound, ErrBuildInProgress.
func (s *Store) QueueBootImageBuild(ctx context.Context, id, jobKind string, params any) (Job, error) {
	var jobID string
	err := s.tx(ctx, func(t *sql.Tx) error {
		var state string
		if err := t.QueryRowContext(ctx, `SELECT state FROM boot_images WHERE id = ?`, id).Scan(&state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if state == BootImageBuilding {
			return ErrBuildInProgress
		}
		var err error
		if jobID, err = s.insertJob(ctx, t, NewJob{Kind: jobKind, ResourceID: &id, Params: params}); err != nil {
			if errors.Is(err, ErrResourceBusy) {
				return ErrBuildInProgress
			}
			return err
		}
		_, err = t.ExecContext(ctx, `UPDATE boot_images SET state = 'building', error = NULL, last_job_id = ?, version = version + 1, updated_at = ?
			WHERE id = ?`, jobID, ms(s.now()), id)
		return err
	})
	if err != nil {
		return Job{}, err
	}
	return s.Job(ctx, jobID)
}

// CompleteBootImageBuild records a finished build of a building boot image
// (run inside the content store's commit). ErrNotFound, ErrBadTransition.
func (s *Store) CompleteBootImageBuild(ctx context.Context, id string, b BootBuild) error {
	return s.recordBuild(ctx, id, b, `state = 'building'`)
}

// AdoptBootImageBuild points a boot image that isn't building at an
// existing build (same input hash), without a job. ErrNotFound,
// ErrBadTransition (it is building).
func (s *Store) AdoptBootImageBuild(ctx context.Context, id string, b BootBuild) error {
	return s.recordBuild(ctx, id, b, `state <> 'building'`)
}

func (s *Store) recordBuild(ctx context.Context, id string, b BootBuild, cond string) error {
	warnings, err := jsonText(append([]string{}, b.Warnings...))
	if err != nil {
		return err
	}
	now := ms(s.now())
	res, err := s.w.ExecContext(ctx, `UPDATE boot_images SET state = 'built', tree_id = ?, input_hash = ?, built_inputs = ?, built_with = ?,
		built_at = ?, selected_boot_manager = ?, warnings = ?, error = NULL, version = version + 1, updated_at = ?
		WHERE id = ? AND `+cond, b.TreeID, b.InputHash, string(b.Inputs), b.BuiltWith, now, string(b.SelectedBootManager), warnings, now, id)
	if err != nil {
		return fmt.Errorf("store: record boot image build: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.BootImage(ctx, id); err != nil {
			return err
		}
		return ErrBadTransition
	}
	return nil
}

// FailBootImageBuild marks a building boot image failed with a Problem; its
// previous build (if any) stays attached. No-op if it isn't building.
func (s *Store) FailBootImageBuild(ctx context.Context, id string, problem json.RawMessage) error {
	_, err := s.w.ExecContext(ctx, `UPDATE boot_images SET state = 'failed', error = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND state = 'building'`, string(problem), ms(s.now()), id)
	if err != nil {
		return fmt.Errorf("store: fail boot image build: %w", err)
	}
	return nil
}

// BootImageWithBuild returns a boot image (other than exceptID) serving a
// build with this input hash. ErrNotFound.
func (s *Store) BootImageWithBuild(ctx context.Context, inputHash, exceptID string) (BootImage, error) {
	b, err := s.scanBootImage(ctx, s.r, s.r.QueryRowContext(ctx, `SELECT `+bootImageCols+` FROM boot_images
		WHERE input_hash = ? AND id <> ? ORDER BY built_at DESC LIMIT 1`, inputHash, exceptID))
	if errors.Is(err, sql.ErrNoRows) {
		return BootImage{}, ErrNotFound
	}
	return b, err
}

// BootDriver is an enabled INF of a ready package in a boot image's
// driver groups.
type BootDriver struct {
	INFID         string
	PackageID     string
	PackageSHA256 string
	TreeID        string
	Path          string
	Role          string
	Signature     string
	Architectures []string
}

// BootDrivers lists the enabled INFs of ready packages in groupIDs, each
// once, ordered by group position, package position, then path.
func (s *Store) BootDrivers(ctx context.Context, groupIDs []string) ([]BootDriver, error) {
	var out []BootDriver
	seen := map[string]bool{}
	for _, g := range groupIDs {
		rows, err := s.r.QueryContext(ctx, `SELECT i.id, p.id, p.sha256, p.tree_id, i.path, i.role, i.signature, i.architectures
			FROM driver_group_packages gp JOIN driver_packages p ON p.id = gp.package_id JOIN driver_infs i ON i.package_id = p.id
			WHERE gp.group_id = ? AND p.status = 'ready' AND i.enabled = 1 ORDER BY gp.position, i.path`, g)
		if err != nil {
			return nil, fmt.Errorf("store: boot drivers: %w", err)
		}
		for rows.Next() {
			var d BootDriver
			var arch string
			if err := rows.Scan(&d.INFID, &d.PackageID, &d.PackageSHA256, &d.TreeID, &d.Path, &d.Role, &d.Signature, &arch); err != nil {
				rows.Close()
				return nil, fmt.Errorf("store: boot drivers: %w", err)
			}
			if err := json.Unmarshal([]byte(arch), &d.Architectures); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[d.INFID] {
				seen[d.INFID] = true
				out = append(out, d)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
