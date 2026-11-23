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

// Unattend templates (ADR 0027). The built-in template isn't stored.

var (
	// ErrTemplateInUse: a profile uses the template (or the identity mode a
	// replacement drops).
	ErrTemplateInUse = errors.New("store: unattend template is in use")
	// ErrTemplateIdentityMismatch: the profile's identity mode isn't one the
	// template renders.
	ErrTemplateIdentityMismatch = errors.New("store: the unattend template doesn't render the profile's identity mode")
	// ErrTemplateFormatMismatch: the template's format isn't the profile
	// kind's (unattend for Windows, autoinstall for Linux; ADR 0035).
	ErrTemplateFormatMismatch = errors.New("store: the template's format doesn't match the profile's kind")
	// ErrFormatChanged: a replace changes a template's format.
	ErrFormatChanged = errors.New("store: a template's format can't change")
)

// Template formats.
const (
	FormatUnattend    = "unattend"
	FormatAutoinstall = "autoinstall"
)

// UnattendTemplateInput is a template's editable fields.
type UnattendTemplateInput struct {
	Name              string
	Description       string
	Format            string // FormatUnattend ("" too) or FormatAutoinstall
	Content           string
	IdentityModes     []string
	VariableDefaults  map[string]string
	RequiredVariables []string
}

// UnattendTemplate is an unattend_templates row.
type UnattendTemplate struct {
	ID string
	UnattendTemplateInput
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

const unattendCols = `id, name, description, format, content, identity_modes, variable_defaults, required_variables, version, created_at, updated_at`

func scanUnattendTemplate(r rowScanner) (UnattendTemplate, error) {
	var t UnattendTemplate
	var modes, defaults, required string
	var created, updated int64
	if err := r.Scan(&t.ID, &t.Name, &t.Description, &t.Format, &t.Content, &modes, &defaults, &required, &t.Version, &created, &updated); err != nil {
		return UnattendTemplate{}, err
	}
	if err := json.Unmarshal([]byte(modes), &t.IdentityModes); err != nil {
		return UnattendTemplate{}, err
	}
	if err := json.Unmarshal([]byte(defaults), &t.VariableDefaults); err != nil {
		return UnattendTemplate{}, err
	}
	if err := json.Unmarshal([]byte(required), &t.RequiredVariables); err != nil {
		return UnattendTemplate{}, err
	}
	t.CreatedAt, t.UpdatedAt = fromMS(created), fromMS(updated)
	return t, nil
}

func (in UnattendTemplateInput) columns() ([]any, error) {
	if in.VariableDefaults == nil {
		in.VariableDefaults = map[string]string{}
	}
	if in.RequiredVariables == nil {
		in.RequiredVariables = []string{}
	}
	if in.IdentityModes == nil {
		in.IdentityModes = []string{}
	}
	modes, err := jsonText(in.IdentityModes)
	if err != nil {
		return nil, err
	}
	defaults, err := jsonText(in.VariableDefaults)
	if err != nil {
		return nil, err
	}
	required, err := jsonText(in.RequiredVariables)
	if err != nil {
		return nil, err
	}
	return []any{in.Name, in.Description, in.Content, modes, defaults, required}, nil
}

func (in UnattendTemplateInput) format() string {
	if in.Format == "" {
		return FormatUnattend
	}
	return in.Format
}

// CreateUnattendTemplate stores a template.
func (s *Store) CreateUnattendTemplate(ctx context.Context, in UnattendTemplateInput) (UnattendTemplate, error) {
	cols, err := in.columns()
	if err != nil {
		return UnattendTemplate{}, err
	}
	id, now := newID(), ms(s.now())
	args := append(append([]any{id}, cols...), in.format(), now, now)
	if _, err := s.w.ExecContext(ctx, `INSERT INTO unattend_templates (id, name, description, content, identity_modes,
		variable_defaults, required_variables, format, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...); err != nil {
		return UnattendTemplate{}, fmt.Errorf("store: create unattend template: %w", err)
	}
	return s.UnattendTemplate(ctx, id)
}

// UnattendTemplate reads one template. ErrNotFound.
func (s *Store) UnattendTemplate(ctx context.Context, id string) (UnattendTemplate, error) {
	t, err := scanUnattendTemplate(s.r.QueryRowContext(ctx, `SELECT `+unattendCols+` FROM unattend_templates WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return UnattendTemplate{}, ErrNotFound
	}
	if err != nil {
		return UnattendTemplate{}, fmt.Errorf("store: read unattend template: %w", err)
	}
	return t, nil
}

// ListUnattendTemplates returns templates oldest first and the next cursor.
func (s *Store) ListUnattendTemplates(ctx context.Context, limit int, after string) ([]UnattendTemplate, string, error) {
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	rows, err := s.r.QueryContext(ctx, `SELECT `+unattendCols+` FROM unattend_templates WHERE id > ? ORDER BY id LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("store: list unattend templates: %w", err)
	}
	defer rows.Close()
	var out []UnattendTemplate
	for rows.Next() {
		t, err := scanUnattendTemplate(rows)
		if err != nil {
			return nil, "", fmt.Errorf("store: list unattend templates: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// templateUsers returns the profiles using a template: name by identity mode.
func templateUsers(ctx context.Context, t *sql.Tx, id string) (map[string][]string, int, error) {
	rows, err := t.QueryContext(ctx, `SELECT name, COALESCE(json_extract(identity, '$.mode'), '') FROM profiles WHERE unattend_template_id = ? ORDER BY name`, id)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out, n := map[string][]string{}, 0
	for rows.Next() {
		var name, mode string
		if err := rows.Scan(&name, &mode); err != nil {
			return nil, 0, err
		}
		out[mode] = append(out[mode], name)
		n++
	}
	return out, n, rows.Err()
}

// ReplaceUnattendTemplate replaces a template if the version still matches
// and every profile using it keeps its identity mode. ErrNotFound,
// ErrVersionMismatch, ErrTemplateInUse (wrapped, naming the profiles).
func (s *Store) ReplaceUnattendTemplate(ctx context.Context, id string, in UnattendTemplateInput, ifVersion int64) (UnattendTemplate, error) {
	cols, err := in.columns()
	if err != nil {
		return UnattendTemplate{}, err
	}
	err = s.tx(ctx, func(t *sql.Tx) error {
		var v int64
		var format string
		if err := t.QueryRowContext(ctx, `SELECT version, format FROM unattend_templates WHERE id = ?`, id).Scan(&v, &format); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if v != ifVersion {
			return ErrVersionMismatch
		}
		if format != in.format() {
			return ErrFormatChanged
		}
		users, _, err := templateUsers(ctx, t, id)
		if err != nil {
			return err
		}
		keep := map[string]bool{}
		for _, m := range in.IdentityModes {
			keep[m] = true
		}
		var lost []string
		for mode, names := range users {
			if mode != "" && !keep[mode] {
				lost = append(lost, fmt.Sprintf("%s (%s)", strings.Join(names, ", "), mode))
			}
		}
		if len(lost) > 0 {
			return fmt.Errorf("%w: profiles %s use identity modes it would no longer render", ErrTemplateInUse, strings.Join(lost, "; "))
		}
		args := append(cols, ms(s.now()), id)
		if _, err := t.ExecContext(ctx, `UPDATE unattend_templates SET name = ?, description = ?, content = ?, identity_modes = ?,
			variable_defaults = ?, required_variables = ?, version = version + 1, updated_at = ? WHERE id = ?`, args...); err != nil {
			return fmt.Errorf("store: replace unattend template: %w", err)
		}
		return nil
	})
	if err != nil {
		return UnattendTemplate{}, err
	}
	return s.UnattendTemplate(ctx, id)
}

// DeleteUnattendTemplate deletes a template no profile uses. Deployments
// keep their own copy. ErrNotFound, ErrTemplateInUse (wrapped).
func (s *Store) DeleteUnattendTemplate(ctx context.Context, id string) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		users, n, err := templateUsers(ctx, t, id)
		if err != nil {
			return err
		}
		if n > 0 {
			var names []string
			for _, ns := range users {
				names = append(names, ns...)
			}
			return fmt.Errorf("%w: profiles %s use it", ErrTemplateInUse, strings.Join(names, ", "))
		}
		res, err := t.ExecContext(ctx, `DELETE FROM unattend_templates WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete unattend template: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// profileTemplateRef checks the profile's template exists and renders its
// identity mode, inside the profile's transaction.
func profileTemplateRef(ctx context.Context, t *sql.Tx, in ProfileInput) error {
	if in.UnattendTemplateID == nil {
		return nil
	}
	var modes, format string
	err := t.QueryRowContext(ctx, `SELECT identity_modes, format FROM unattend_templates WHERE id = ?`, *in.UnattendTemplateID).Scan(&modes, &format)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: unattend template %s", ErrNotFound, *in.UnattendTemplateID)
	}
	if err != nil {
		return err
	}
	if want := map[bool]string{true: FormatAutoinstall, false: FormatUnattend}[in.Kind == ProfileLinux]; format != want {
		return fmt.Errorf("%w (the template is %s; a %s profile needs %s)", ErrTemplateFormatMismatch, format, cmpOr(in.Kind, ProfileWindows), want)
	}
	if format == FormatAutoinstall {
		return nil
	}
	var list []string
	var id struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal([]byte(modes), &list); err != nil {
		return err
	}
	if err := json.Unmarshal(in.Identity, &id); err != nil {
		return err
	}
	for _, m := range list {
		if m == id.Mode {
			return nil
		}
	}
	return fmt.Errorf("%w (%s; it renders %s)", ErrTemplateIdentityMismatch, id.Mode, strings.Join(list, ", "))
}

func cmpOr(a, b string) string {
	if a == "" {
		return b
	}
	return a
}
