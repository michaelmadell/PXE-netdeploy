package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrUnknownProfile: a referenced profile doesn't exist.
	ErrUnknownProfile = errors.New("store: unknown profile")
	// ErrProfileInUse: the profile is the default profile or a client's.
	ErrProfileInUse = errors.New("store: profile is in use")
	// ErrBootImageInUse: a profile uses the boot image.
	ErrBootImageInUse = errors.New("store: boot image is used by a profile")
)

// ProfileInput is a profile's editable fields (ProfileInput with defaults
// applied). Identity and OSSettings are the API's JSON.
type ProfileInput struct {
	Name                 string
	Description          string
	BootImageID          string
	InstallImageID       string
	EditionIndex         int
	OSDriverGroupIDs     []string
	UnattendTemplateID   *string
	Variables            map[string]string
	InstallMethod        string
	SMBTargetID          *string
	Identity             json.RawMessage
	OSSettings           json.RawMessage // nil: none
	BootOrderAfterDeploy string
}

// Profile is a profiles row with its OS driver groups.
type Profile struct {
	ID string
	ProfileInput
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

const profileCols = `id, name, description, boot_image_id, install_image_id, edition_index, install_method, smb_target_id,
	unattend_template_id, identity, os_settings, variables, boot_order_after_deploy, version, created_at, updated_at`

func (s *Store) scanProfile(ctx context.Context, r rowScanner) (Profile, error) {
	var p Profile
	var smb, tmpl, osSettings sql.NullString
	var identity, vars string
	var created, updated int64
	if err := r.Scan(&p.ID, &p.Name, &p.Description, &p.BootImageID, &p.InstallImageID, &p.EditionIndex, &p.InstallMethod, &smb,
		&tmpl, &identity, &osSettings, &vars, &p.BootOrderAfterDeploy, &p.Version, &created, &updated); err != nil {
		return Profile{}, err
	}
	p.SMBTargetID, p.UnattendTemplateID = fromNullStr(smb), fromNullStr(tmpl)
	p.Identity = json.RawMessage(identity)
	if osSettings.Valid {
		p.OSSettings = json.RawMessage(osSettings.String)
	}
	if err := json.Unmarshal([]byte(vars), &p.Variables); err != nil {
		return Profile{}, err
	}
	p.CreatedAt, p.UpdatedAt = fromMS(created), fromMS(updated)
	groups, err := s.ids(ctx, `SELECT group_id FROM profile_os_driver_groups WHERE profile_id = ? ORDER BY position`, p.ID)
	if err != nil {
		return Profile{}, err
	}
	p.OSDriverGroupIDs = append([]string{}, groups...)
	return p, nil
}

func (in ProfileInput) columns() ([]any, error) {
	vars := in.Variables
	if vars == nil {
		vars = map[string]string{}
	}
	v, err := jsonText(vars)
	if err != nil {
		return nil, err
	}
	var osSettings sql.NullString
	if len(in.OSSettings) > 0 && string(in.OSSettings) != "null" {
		osSettings = sql.NullString{String: string(in.OSSettings), Valid: true}
	}
	return []any{in.Name, in.Description, in.BootImageID, in.InstallImageID, in.EditionIndex, in.InstallMethod,
		nullStr(in.SMBTargetID), nullStr(in.UnattendTemplateID), string(in.Identity), osSettings, v, in.BootOrderAfterDeploy}, nil
}

func (s *Store) setProfileGroups(ctx context.Context, t *sql.Tx, id string, groups []string) error {
	if _, err := t.ExecContext(ctx, `DELETE FROM profile_os_driver_groups WHERE profile_id = ?`, id); err != nil {
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
		if _, err := t.ExecContext(ctx, `INSERT INTO profile_os_driver_groups (profile_id, group_id, position) VALUES (?, ?, ?)`, id, g, i); err != nil {
			if uniqueViolation(err, "") {
				return fmt.Errorf("%w: driver group %s listed twice", ErrDuplicate, g)
			}
			return fmt.Errorf("store: profile driver group: %w", err)
		}
	}
	return nil
}

// profileRefs checks the boot and install images exist (the caller checks
// what they contain).
func profileRefs(ctx context.Context, t *sql.Tx, in ProfileInput) error {
	var one int
	if err := t.QueryRowContext(ctx, `SELECT 1 FROM boot_images WHERE id = ?`, in.BootImageID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: boot image %s", ErrNotFound, in.BootImageID)
	} else if err != nil {
		return err
	}
	if err := t.QueryRowContext(ctx, `SELECT 1 FROM images WHERE id = ?`, in.InstallImageID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: install image %s", ErrNotFound, in.InstallImageID)
	} else if err != nil {
		return err
	}
	return nil
}

// CreateProfile creates a profile. ErrNotFound (wrapped, naming the boot
// or install image), ErrUnknownDriverGroup, ErrDuplicate.
func (s *Store) CreateProfile(ctx context.Context, in ProfileInput) (Profile, error) {
	cols, err := in.columns()
	if err != nil {
		return Profile{}, err
	}
	id := newID()
	err = s.tx(ctx, func(t *sql.Tx) error {
		if err := profileRefs(ctx, t, in); err != nil {
			return err
		}
		now := ms(s.now())
		args := append(append([]any{id}, cols...), now, now)
		if _, err := t.ExecContext(ctx, `INSERT INTO profiles (id, name, description, boot_image_id, install_image_id, edition_index,
			install_method, smb_target_id, unattend_template_id, identity, os_settings, variables, boot_order_after_deploy, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...); err != nil {
			return fmt.Errorf("store: create profile: %w", err)
		}
		return s.setProfileGroups(ctx, t, id, in.OSDriverGroupIDs)
	})
	if err != nil {
		return Profile{}, err
	}
	return s.Profile(ctx, id)
}

// Profile reads one profile. ErrNotFound.
func (s *Store) Profile(ctx context.Context, id string) (Profile, error) {
	p, err := s.scanProfile(ctx, s.r.QueryRowContext(ctx, `SELECT `+profileCols+` FROM profiles WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, fmt.Errorf("store: read profile: %w", err)
	}
	return p, nil
}

// ListProfiles returns profiles oldest first and the next cursor.
func (s *Store) ListProfiles(ctx context.Context, limit int, after string) ([]Profile, string, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	ids, err := s.ids(ctx, `SELECT id FROM profiles WHERE id > ? ORDER BY id LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("store: list profiles: %w", err)
	}
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = ids[limit-1]
	}
	out := make([]Profile, 0, len(ids))
	for _, id := range ids {
		p, err := s.Profile(ctx, id)
		if err != nil {
			return nil, "", err
		}
		out = append(out, p)
	}
	return out, next, nil
}

// ReplaceProfile replaces a profile if the version still matches.
// ErrNotFound (unwrapped: the profile; wrapped: a referenced image),
// ErrVersionMismatch, ErrUnknownDriverGroup, ErrDuplicate.
func (s *Store) ReplaceProfile(ctx context.Context, id string, in ProfileInput, ifVersion int64) (Profile, error) {
	cols, err := in.columns()
	if err != nil {
		return Profile{}, err
	}
	err = s.tx(ctx, func(t *sql.Tx) error {
		var v int64
		if err := t.QueryRowContext(ctx, `SELECT version FROM profiles WHERE id = ?`, id).Scan(&v); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if v != ifVersion {
			return ErrVersionMismatch
		}
		if err := profileRefs(ctx, t, in); err != nil {
			return err
		}
		args := append(cols, ms(s.now()), id)
		if _, err := t.ExecContext(ctx, `UPDATE profiles SET name = ?, description = ?, boot_image_id = ?, install_image_id = ?,
			edition_index = ?, install_method = ?, smb_target_id = ?, unattend_template_id = ?, identity = ?, os_settings = ?,
			variables = ?, boot_order_after_deploy = ?, version = version + 1, updated_at = ? WHERE id = ?`, args...); err != nil {
			return fmt.Errorf("store: replace profile: %w", err)
		}
		return s.setProfileGroups(ctx, t, id, in.OSDriverGroupIDs)
	})
	if err != nil {
		return Profile{}, err
	}
	return s.Profile(ctx, id)
}

// DeleteProfile deletes a profile that is neither the default profile nor
// a client's. ErrNotFound, ErrProfileInUse (wrapped, saying by what).
func (s *Store) DeleteProfile(ctx context.Context, id string) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		var def sql.NullString
		if err := t.QueryRowContext(ctx, `SELECT json_extract(doc, '$.bootPrompt.defaultProfileId') FROM settings WHERE id = 1`).Scan(&def); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if def.Valid && def.String == id {
			return fmt.Errorf("%w: it is bootPrompt.defaultProfileId", ErrProfileInUse)
		}
		var clients int
		if err := t.QueryRowContext(ctx, `SELECT count(*) FROM clients WHERE profile_id = ?`, id).Scan(&clients); err != nil {
			return err
		}
		if clients > 0 {
			return fmt.Errorf("%w: %d client(s) use it", ErrProfileInUse, clients)
		}
		res, err := t.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete profile: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ProfileExists reports whether a profile with id exists.
func (s *Store) ProfileExists(ctx context.Context, id string) (bool, error) {
	var one int
	err := s.r.QueryRowContext(ctx, `SELECT 1 FROM profiles WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// inUse counts rows of q (with arg) and returns err when there are any.
func inUse(ctx context.Context, t *sql.Tx, q, arg string, err error) error {
	var n int
	if qerr := t.QueryRowContext(ctx, q, arg).Scan(&n); qerr != nil {
		return qerr
	}
	if n > 0 {
		return err
	}
	return nil
}
