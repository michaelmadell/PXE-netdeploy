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

var (
	// ErrPackageImporting: the package is still importing.
	ErrPackageImporting = errors.New("store: driver package is importing")
	// ErrPackageInUse: a driver group includes the package.
	ErrPackageInUse = errors.New("store: driver package is in a driver group")
	// ErrUnknownINF: an INF id isn't in the package.
	ErrUnknownINF = errors.New("store: unknown INF id")
	// ErrUnknownPackage: a driver group names a package that doesn't exist.
	ErrUnknownPackage = errors.New("store: unknown driver package")
)

// DriverPackage is a driver_packages row with its INFs.
type DriverPackage struct {
	ID             string
	Name           string
	Status         string // ImageImporting/Ready/Failed values
	Error          json.RawMessage
	SourceFilename *string
	UploadID       *string
	SizeBytes      int64
	SHA256         string
	TreeID         *string
	Warnings       []string
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
	INFs           []DriverINF
}

// DriverINF is a driver_infs row with its hardware IDs.
type DriverINF struct {
	ID               string
	Path             string
	Class            string
	Role             string
	Provider         *string
	Version          *string
	DriverDate       *string // YYYY-MM-DD
	Architectures    []string
	TargetOSVersions []string
	Signature        string
	Signer           *string
	SignatureDetail  *string
	Enabled          bool
	HardwareIDs      []string
}

const packageCols = `id, name, status, error, source_filename, upload_id, size_bytes, sha256, tree_id, warnings, version, created_at, updated_at`

func scanPackage(r rowScanner) (DriverPackage, error) {
	var p DriverPackage
	var errDoc, src, upload, tree sql.NullString
	var warnings string
	var created, updated int64
	if err := r.Scan(&p.ID, &p.Name, &p.Status, &errDoc, &src, &upload, &p.SizeBytes, &p.SHA256, &tree, &warnings, &p.Version, &created, &updated); err != nil {
		return DriverPackage{}, err
	}
	if errDoc.Valid {
		p.Error = json.RawMessage(errDoc.String)
	}
	p.SourceFilename, p.UploadID, p.TreeID = fromNullStr(src), fromNullStr(upload), fromNullStr(tree)
	if err := json.Unmarshal([]byte(warnings), &p.Warnings); err != nil {
		return DriverPackage{}, err
	}
	p.CreatedAt, p.UpdatedAt = fromMS(created), fromMS(updated)
	return p, nil
}

// DuplicatePackageError: a live package from the same archive exists.
type DuplicatePackageError struct{ Package DriverPackage }

func (e *DuplicatePackageError) Error() string {
	return fmt.Sprintf("store: archive already imported as driver package %s", e.Package.ID)
}

// CreateDriverImport creates an importing package and its queued job and
// claims the upload, in one transaction (as CreateImageImport).
func (s *Store) CreateDriverImport(ctx context.Context, name, uploadID, jobKind string) (DriverPackage, Job, error) {
	var pkgID, jobID string
	err := s.tx(ctx, func(t *sql.Tx) error {
		u, err := s.uploadWhere(ctx, t, uploadID)
		if err != nil {
			return err
		}
		if !u.Complete() {
			return ErrUploadIncomplete
		}
		if dup, err := scanPackage(t.QueryRowContext(ctx, `SELECT `+packageCols+` FROM driver_packages WHERE sha256 = ? AND status <> 'failed'`, *u.SHA256)); err == nil {
			return &DuplicatePackageError{Package: dup}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: create driver package: %w", err)
		}
		pkgID = newID()
		now := ms(s.now())
		if _, err := t.ExecContext(ctx, `INSERT INTO driver_packages (id, name, status, source_filename, upload_id, size_bytes, sha256, created_at, updated_at)
			VALUES (?, ?, 'importing', ?, ?, ?, ?, ?, ?)`, pkgID, name, nullStr(u.Filename), u.ID, u.Length, *u.SHA256, now, now); err != nil {
			return fmt.Errorf("store: create driver package: %w", err)
		}
		if jobID, err = s.insertJob(ctx, t, NewJob{Kind: jobKind, ResourceID: &pkgID, Params: map[string]string{"uploadId": u.ID}}); err != nil {
			return err
		}
		return s.claimUpload(ctx, t, u.ID, jobID)
	})
	if err != nil {
		return DriverPackage{}, Job{}, err
	}
	p, err := s.DriverPackage(ctx, pkgID)
	if err != nil {
		return DriverPackage{}, Job{}, err
	}
	j, err := s.Job(ctx, jobID)
	return p, j, err
}

// DriverPackage reads a package with its INFs. ErrNotFound.
func (s *Store) DriverPackage(ctx context.Context, id string) (DriverPackage, error) {
	p, err := scanPackage(s.r.QueryRowContext(ctx, `SELECT `+packageCols+` FROM driver_packages WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return DriverPackage{}, ErrNotFound
	}
	if err != nil {
		return DriverPackage{}, fmt.Errorf("store: read driver package: %w", err)
	}
	list := []DriverPackage{p}
	if err := s.loadINFs(ctx, list); err != nil {
		return DriverPackage{}, err
	}
	return list[0], nil
}

// PackageFilter narrows ListDriverPackages: packages with at least one INF
// matching every set field.
type PackageFilter struct {
	Class      string
	Role       string
	HardwareID string // an INF ID equal to it, or a prefix of it ending before '&'
	Limit      int
	After      string
}

// ListDriverPackages returns packages oldest first and the next cursor.
func (s *Store) ListDriverPackages(ctx context.Context, f PackageFilter) ([]DriverPackage, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	where, args := []string{"p.id > ?"}, []any{f.After}
	if f.Class != "" || f.Role != "" || f.HardwareID != "" {
		sub, subArgs := []string{"i.package_id = p.id"}, []any{}
		if f.Class != "" {
			sub, subArgs = append(sub, "i.class = ? COLLATE NOCASE"), append(subArgs, f.Class)
		}
		if f.Role != "" {
			sub, subArgs = append(sub, "i.role = ?"), append(subArgs, f.Role)
		}
		if f.HardwareID != "" {
			q := strings.ToLower(f.HardwareID)
			sub = append(sub, `EXISTS (SELECT 1 FROM driver_hardware_ids h WHERE h.inf_id = i.id
				AND (h.fold = ? OR substr(?, 1, length(h.fold) + 1) = h.fold || '&'))`)
			subArgs = append(subArgs, q, q)
		}
		where = append(where, "EXISTS (SELECT 1 FROM driver_infs i WHERE "+strings.Join(sub, " AND ")+")")
		args = append(args, subArgs...)
	}
	args = append(args, limit+1)
	rows, err := s.r.QueryContext(ctx, `SELECT p.`+strings.ReplaceAll(packageCols, ", ", ", p.")+` FROM driver_packages p WHERE `+
		strings.Join(where, " AND ")+` ORDER BY p.id LIMIT ?`, args...)
	if err != nil {
		return nil, "", fmt.Errorf("store: list driver packages: %w", err)
	}
	var out []DriverPackage
	for rows.Next() {
		p, err := scanPackage(rows)
		if err != nil {
			rows.Close()
			return nil, "", fmt.Errorf("store: list driver packages: %w", err)
		}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	if err := s.loadINFs(ctx, out); err != nil {
		return nil, "", err
	}
	return out, next, nil
}

// loadINFs fills in the INFs (and their hardware IDs) of packages.
func (s *Store) loadINFs(ctx context.Context, pkgs []DriverPackage) error {
	if len(pkgs) == 0 {
		return nil
	}
	idx := map[string]int{}
	args := make([]any, len(pkgs))
	for i, p := range pkgs {
		idx[p.ID] = i
		args[i] = p.ID
	}
	in := "(?" + strings.Repeat(", ?", len(pkgs)-1) + ")"
	rows, err := s.r.QueryContext(ctx, `SELECT id, package_id, path, class, role, provider, version, driver_date, architectures, target_os,
		signature, signer, signature_detail, enabled FROM driver_infs WHERE package_id IN `+in+` ORDER BY path`, args...)
	if err != nil {
		return fmt.Errorf("store: read INFs: %w", err)
	}
	infAt := map[string][2]int{}
	for rows.Next() {
		var d DriverINF
		var pkg, archs, targets string
		var provider, version, date, signer, detail sql.NullString
		var enabled int
		if err := rows.Scan(&d.ID, &pkg, &d.Path, &d.Class, &d.Role, &provider, &version, &date, &archs, &targets,
			&d.Signature, &signer, &detail, &enabled); err != nil {
			rows.Close()
			return fmt.Errorf("store: read INFs: %w", err)
		}
		d.Provider, d.Version, d.DriverDate, d.Signer, d.SignatureDetail = fromNullStr(provider), fromNullStr(version), fromNullStr(date), fromNullStr(signer), fromNullStr(detail)
		json.Unmarshal([]byte(archs), &d.Architectures)
		json.Unmarshal([]byte(targets), &d.TargetOSVersions)
		d.Enabled = enabled == 1
		i := idx[pkg]
		pkgs[i].INFs = append(pkgs[i].INFs, d)
		infAt[d.ID] = [2]int{i, len(pkgs[i].INFs) - 1}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.r.QueryContext(ctx, `SELECT h.inf_id, h.hardware_id FROM driver_hardware_ids h JOIN driver_infs i ON i.id = h.inf_id
		WHERE i.package_id IN `+in+` ORDER BY h.inf_id, h.fold`, args...)
	if err != nil {
		return fmt.Errorf("store: read hardware IDs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var infID, hw string
		if err := rows.Scan(&infID, &hw); err != nil {
			return fmt.Errorf("store: read hardware IDs: %w", err)
		}
		if at, ok := infAt[infID]; ok {
			inf := &pkgs[at[0]].INFs[at[1]]
			inf.HardwareIDs = append(inf.HardwareIDs, hw)
		}
	}
	return rows.Err()
}

// UpdateDriverPackage renames a package and/or switches INFs on or off if
// the version still matches. ErrNotFound, ErrVersionMismatch, ErrUnknownINF.
func (s *Store) UpdateDriverPackage(ctx context.Context, id string, name *string, enabled map[string]bool, ifVersion int64) (DriverPackage, error) {
	err := s.tx(ctx, func(t *sql.Tx) error {
		var v int64
		if err := t.QueryRowContext(ctx, `SELECT version FROM driver_packages WHERE id = ?`, id).Scan(&v); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("store: update driver package: %w", err)
		}
		if v != ifVersion {
			return ErrVersionMismatch
		}
		for infID, on := range enabled {
			res, err := t.ExecContext(ctx, `UPDATE driver_infs SET enabled = ? WHERE id = ? AND package_id = ?`, boolInt(on), infID, id)
			if err != nil {
				return fmt.Errorf("store: update INF: %w", err)
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("%w: %s", ErrUnknownINF, infID)
			}
		}
		_, err := t.ExecContext(ctx, `UPDATE driver_packages SET name = coalesce(?, name), version = version + 1, updated_at = ? WHERE id = ?`,
			nullStr(name), ms(s.now()), id)
		return err
	})
	if err != nil {
		return DriverPackage{}, err
	}
	return s.DriverPackage(ctx, id)
}

// DeleteDriverPackage deletes a ready or failed package that no group
// includes. ErrNotFound, ErrPackageImporting, ErrPackageInUse.
func (s *Store) DeleteDriverPackage(ctx context.Context, id string) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		var status string
		if err := t.QueryRowContext(ctx, `SELECT status FROM driver_packages WHERE id = ?`, id).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("store: delete driver package: %w", err)
		}
		if status == ImageImporting {
			return ErrPackageImporting
		}
		var n int
		t.QueryRowContext(ctx, `SELECT count(*) FROM driver_group_packages WHERE package_id = ?`, id).Scan(&n)
		if n > 0 {
			return ErrPackageInUse
		}
		_, err := t.ExecContext(ctx, `DELETE FROM driver_packages WHERE id = ?`, id)
		return err
	})
}

// NewDriverINF is an INF found by an import.
type NewDriverINF struct {
	Path             string
	Class            string
	Role             string
	Provider         string
	Version          string
	DriverDate       *time.Time
	Architectures    []string
	TargetOSVersions []string
	Signature        string
	Signer           string
	SignatureDetail  string
	Enabled          bool
	HardwareIDs      []string
}

// CompleteDriverImport marks an importing package ready with its tree and
// INFs, in one transaction (inside the content store's commit).
// ErrNotFound, ErrBadTransition.
func (s *Store) CompleteDriverImport(ctx context.Context, id, treeID string, warnings []string, infs []NewDriverINF) error {
	w, err := json.Marshal(append([]string{}, warnings...))
	if err != nil {
		return err
	}
	return s.tx(ctx, func(t *sql.Tx) error {
		res, err := t.ExecContext(ctx, `UPDATE driver_packages SET status = 'ready', tree_id = ?, warnings = ?, version = version + 1, updated_at = ?
			WHERE id = ? AND status = 'importing'`, treeID, string(w), ms(s.now()), id)
		if err != nil {
			return fmt.Errorf("store: complete driver package: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, err := s.DriverPackage(ctx, id); err != nil {
				return err
			}
			return ErrBadTransition
		}
		hw, err := t.PrepareContext(ctx, `INSERT INTO driver_hardware_ids (inf_id, fold, hardware_id) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`)
		if err != nil {
			return err
		}
		defer hw.Close()
		for _, d := range infs {
			infID := newID()
			archs, _ := json.Marshal(append([]string{}, d.Architectures...))
			targets, _ := json.Marshal(append([]string{}, d.TargetOSVersions...))
			var date sql.NullString
			if d.DriverDate != nil {
				date = sql.NullString{String: d.DriverDate.Format("2006-01-02"), Valid: true}
			}
			if _, err := t.ExecContext(ctx, `INSERT INTO driver_infs (id, package_id, path, class, role, provider, version, driver_date,
				architectures, target_os, signature, signer, signature_detail, enabled) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				infID, id, d.Path, d.Class, d.Role, nullIfEmpty(d.Provider), nullIfEmpty(d.Version), date, string(archs), string(targets),
				d.Signature, nullIfEmpty(d.Signer), nullIfEmpty(d.SignatureDetail), boolInt(d.Enabled)); err != nil {
				return fmt.Errorf("store: record INF %s: %w", d.Path, err)
			}
			for _, h := range d.HardwareIDs {
				if _, err := hw.ExecContext(ctx, infID, strings.ToLower(h), h); err != nil {
					return fmt.Errorf("store: record hardware ID: %w", err)
				}
			}
		}
		return nil
	})
}

// FailDriverPackage marks an importing package failed. No-op otherwise.
func (s *Store) FailDriverPackage(ctx context.Context, id string, problem json.RawMessage) error {
	_, err := s.w.ExecContext(ctx, `UPDATE driver_packages SET status = 'failed', error = ?, version = version + 1, updated_at = ?
		WHERE id = ? AND status = 'importing'`, string(problem), ms(s.now()), id)
	if err != nil {
		return fmt.Errorf("store: fail driver package: %w", err)
	}
	_, err = s.DriverPackage(ctx, id)
	return err
}

// DriverGroup is a driver_groups row with its packages in order.
type DriverGroup struct {
	ID          string
	Name        string
	Description string
	PackageIDs  []string
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// DriverGroupInput is a group's editable fields.
type DriverGroupInput struct {
	Name        string
	Description string
	PackageIDs  []string
}

// CreateDriverGroup creates a group. ErrUnknownPackage.
func (s *Store) CreateDriverGroup(ctx context.Context, in DriverGroupInput) (DriverGroup, error) {
	id := newID()
	err := s.tx(ctx, func(t *sql.Tx) error {
		now := ms(s.now())
		if _, err := t.ExecContext(ctx, `INSERT INTO driver_groups (id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			id, in.Name, in.Description, now, now); err != nil {
			return fmt.Errorf("store: create driver group: %w", err)
		}
		return s.setGroupPackages(ctx, t, id, in.PackageIDs)
	})
	if err != nil {
		return DriverGroup{}, err
	}
	return s.DriverGroup(ctx, id)
}

func (s *Store) setGroupPackages(ctx context.Context, t *sql.Tx, groupID string, ids []string) error {
	if _, err := t.ExecContext(ctx, `DELETE FROM driver_group_packages WHERE group_id = ?`, groupID); err != nil {
		return err
	}
	for i, p := range ids {
		var status string
		if err := t.QueryRowContext(ctx, `SELECT status FROM driver_packages WHERE id = ?`, p).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrUnknownPackage, p)
			}
			return err
		}
		if status == ImageFailed {
			return fmt.Errorf("%w: %s failed to import", ErrUnknownPackage, p)
		}
		if _, err := t.ExecContext(ctx, `INSERT INTO driver_group_packages (group_id, package_id, position) VALUES (?, ?, ?)`, groupID, p, i); err != nil {
			if uniqueViolation(err, "") {
				return fmt.Errorf("%w: %s listed twice", ErrDuplicate, p)
			}
			return fmt.Errorf("store: group package: %w", err)
		}
	}
	return nil
}

// DriverGroup reads a group. ErrNotFound.
func (s *Store) DriverGroup(ctx context.Context, id string) (DriverGroup, error) {
	g, err := s.scanGroup(ctx, s.r.QueryRowContext(ctx, `SELECT id, name, description, version, created_at, updated_at FROM driver_groups WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return DriverGroup{}, ErrNotFound
	}
	return g, err
}

func (s *Store) scanGroup(ctx context.Context, r rowScanner) (DriverGroup, error) {
	var g DriverGroup
	var created, updated int64
	if err := r.Scan(&g.ID, &g.Name, &g.Description, &g.Version, &created, &updated); err != nil {
		return DriverGroup{}, err
	}
	g.CreatedAt, g.UpdatedAt = fromMS(created), fromMS(updated)
	rows, err := s.r.QueryContext(ctx, `SELECT package_id FROM driver_group_packages WHERE group_id = ? ORDER BY position`, g.ID)
	if err != nil {
		return DriverGroup{}, err
	}
	defer rows.Close()
	g.PackageIDs = []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return DriverGroup{}, err
		}
		g.PackageIDs = append(g.PackageIDs, p)
	}
	return g, rows.Err()
}

// ListDriverGroups returns groups oldest first and the next cursor.
func (s *Store) ListDriverGroups(ctx context.Context, limit int, after string) ([]DriverGroup, string, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	rows, err := s.r.QueryContext(ctx, `SELECT id FROM driver_groups WHERE id > ? ORDER BY id LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("store: list driver groups: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = ids[limit-1]
	}
	out := make([]DriverGroup, 0, len(ids))
	for _, id := range ids {
		g, err := s.DriverGroup(ctx, id)
		if err != nil {
			return nil, "", err
		}
		out = append(out, g)
	}
	return out, next, nil
}

// ReplaceDriverGroup replaces a group if the version still matches.
// ErrNotFound, ErrVersionMismatch, ErrUnknownPackage.
func (s *Store) ReplaceDriverGroup(ctx context.Context, id string, in DriverGroupInput, ifVersion int64) (DriverGroup, error) {
	err := s.tx(ctx, func(t *sql.Tx) error {
		var v int64
		if err := t.QueryRowContext(ctx, `SELECT version FROM driver_groups WHERE id = ?`, id).Scan(&v); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if v != ifVersion {
			return ErrVersionMismatch
		}
		if _, err := t.ExecContext(ctx, `UPDATE driver_groups SET name = ?, description = ?, version = version + 1, updated_at = ? WHERE id = ?`,
			in.Name, in.Description, ms(s.now()), id); err != nil {
			return err
		}
		return s.setGroupPackages(ctx, t, id, in.PackageIDs)
	})
	if err != nil {
		return DriverGroup{}, err
	}
	return s.DriverGroup(ctx, id)
}

// DeleteDriverGroup deletes a group. ErrNotFound, ErrDriverGroupInUse.
func (s *Store) DeleteDriverGroup(ctx context.Context, id string) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		var used int
		if err := t.QueryRowContext(ctx, `SELECT count(*) FROM boot_image_driver_groups WHERE group_id = ?`, id).Scan(&used); err != nil {
			return fmt.Errorf("store: delete driver group: %w", err)
		}
		if used > 0 {
			return ErrDriverGroupInUse
		}
		if err := inUse(ctx, t, `SELECT count(*) FROM profile_os_driver_groups WHERE group_id = ?`, id, ErrDriverGroupInUse); err != nil {
			return err
		}
		res, err := t.ExecContext(ctx, `DELETE FROM driver_groups WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete driver group: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}
