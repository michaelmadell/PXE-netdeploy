package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

// Client sources (api ClientSource).
const (
	SourceManual     = "manual"
	SourceDiscovered = "discovered"
)

// ClientInput is the admin-editable part of a client (api ClientInput).
type ClientInput struct {
	MAC        string
	SMBIOSUUID *string
	Serial     *string
	Hostname   *string
	ProfileID  *string
	Variables  map[string]string
	ReservedIP *string
}

// Client is a stored client.
type Client struct {
	ClientInput
	ID         string
	Source     string
	LastSeenAt *time.Time
	LastIP     *string
	LastArch   *string
	Version    int64 // the ETag; sightings don't change it
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NormalizeMAC returns mac as lower-case colon-separated hex. It accepts the
// forms net.ParseMAC does (colons, hyphens, dotted) but only 48-bit addresses.
func NormalizeMAC(mac string) (string, error) {
	hw, err := net.ParseMAC(strings.TrimSpace(mac))
	if err != nil {
		return "", fmt.Errorf("store: invalid mac %q: %w", mac, err)
	}
	if len(hw) != 6 {
		return "", fmt.Errorf("store: invalid mac %q: not a 48-bit address", mac)
	}
	return hw.String(), nil
}

const clientCols = `id, mac, smbios_uuid, serial, hostname, profile_id, variables, reserved_ip,
	source, last_seen_at, last_ip, last_arch, version, created_at, updated_at`

type rowScanner interface{ Scan(...any) error }

func scanClient(r rowScanner) (Client, error) {
	var (
		c                                               Client
		uuid, serial, host, profile, reserved, ip, arch sql.NullString
		vars                                            string
		seen                                            sql.NullInt64
		created, updated                                int64
	)
	if err := r.Scan(&c.ID, &c.MAC, &uuid, &serial, &host, &profile, &vars, &reserved,
		&c.Source, &seen, &ip, &arch, &c.Version, &created, &updated); err != nil {
		return Client{}, err
	}
	c.SMBIOSUUID, c.Serial, c.Hostname = fromNullStr(uuid), fromNullStr(serial), fromNullStr(host)
	c.ProfileID, c.ReservedIP = fromNullStr(profile), fromNullStr(reserved)
	c.LastIP, c.LastArch, c.LastSeenAt = fromNullStr(ip), fromNullStr(arch), fromNullMS(seen)
	c.CreatedAt, c.UpdatedAt = fromMS(created), fromMS(updated)
	if err := json.Unmarshal([]byte(vars), &c.Variables); err != nil {
		return Client{}, fmt.Errorf("store: client %s: variables: %w", c.ID, err)
	}
	return c, nil
}

func encodeVars(v map[string]string) (string, error) {
	if v == nil {
		return "{}", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("store: encode variables: %w", err)
	}
	return string(b), nil
}

// clientWriteErr maps constraint failures on clients to the package errors.
func clientWriteErr(op string, err error) error {
	switch {
	case uniqueViolation(err, "clients.mac"):
		return ErrMACTaken
	case uniqueViolation(err, "clients.reserved_ip"):
		return ErrReservedIPTaken
	}
	return fmt.Errorf("store: %s client: %w", op, err)
}

// CreateClient stores a new manual client. ErrMACTaken, ErrReservedIPTaken.
func (s *Store) CreateClient(ctx context.Context, in ClientInput) (Client, error) {
	mac, err := NormalizeMAC(in.MAC)
	if err != nil {
		return Client{}, err
	}
	in.MAC = mac
	vars, err := encodeVars(in.Variables)
	if err != nil {
		return Client{}, err
	}
	now := ms(s.now())
	id := newID()
	_, err = s.w.ExecContext(ctx, `INSERT INTO clients
		(id, mac, smbios_uuid, serial, hostname, profile_id, variables, reserved_ip, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.MAC, nullStr(in.SMBIOSUUID), nullStr(in.Serial), nullStr(in.Hostname), nullStr(in.ProfileID),
		vars, nullStr(in.ReservedIP), SourceManual, now, now)
	if err != nil {
		return Client{}, clientWriteErr("create", err)
	}
	return s.Client(ctx, id)
}

// Client returns the client with id, or ErrNotFound.
func (s *Store) Client(ctx context.Context, id string) (Client, error) {
	return s.clientWhere(ctx, s.r, "id = ?", id)
}

// ClientByMAC returns the client with mac (any accepted form), or ErrNotFound.
func (s *Store) ClientByMAC(ctx context.Context, mac string) (Client, error) {
	norm, err := NormalizeMAC(mac)
	if err != nil {
		return Client{}, err
	}
	return s.clientWhere(ctx, s.r, "mac = ?", norm)
}

type querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) clientWhere(ctx context.Context, q querier, where string, arg any) (Client, error) {
	c, err := scanClient(q.QueryRowContext(ctx, `SELECT `+clientCols+` FROM clients WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	if err != nil {
		return Client{}, fmt.Errorf("store: read client: %w", err)
	}
	return c, nil
}

// ClientFilter narrows ListClients. Zero values don't filter.
type ClientFilter struct {
	MAC       string
	ProfileID string
	Source    string
	Limit     int    // 1..200; zero means 50
	After     string // cursor: the last ID of the previous page
}

// ListClients returns clients ordered by ID (creation order, as IDs are
// ULIDs) and the cursor for the next page, empty on the last page.
func (s *Store) ListClients(ctx context.Context, f ClientFilter) ([]Client, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	where, args := []string{"id > ?"}, []any{f.After}
	if f.MAC != "" {
		mac, err := NormalizeMAC(f.MAC)
		if err != nil {
			return nil, "", err
		}
		where, args = append(where, "mac = ?"), append(args, mac)
	}
	if f.ProfileID != "" {
		where, args = append(where, "profile_id = ?"), append(args, f.ProfileID)
	}
	if f.Source != "" {
		where, args = append(where, "source = ?"), append(args, f.Source)
	}
	args = append(args, limit+1)
	rows, err := s.r.QueryContext(ctx, `SELECT `+clientCols+` FROM clients WHERE `+
		strings.Join(where, " AND ")+` ORDER BY id LIMIT ?`, args...)
	if err != nil {
		return nil, "", fmt.Errorf("store: list clients: %w", err)
	}
	defer rows.Close()
	var out []Client
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, "", fmt.Errorf("store: list clients: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("store: list clients: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// ReplaceClient replaces the editable fields if the stored version is still
// ifVersion. ErrNotFound, ErrVersionMismatch, ErrMACTaken, ErrReservedIPTaken.
func (s *Store) ReplaceClient(ctx context.Context, id string, in ClientInput, ifVersion int64) (Client, error) {
	mac, err := NormalizeMAC(in.MAC)
	if err != nil {
		return Client{}, err
	}
	in.MAC = mac
	vars, err := encodeVars(in.Variables)
	if err != nil {
		return Client{}, err
	}
	err = s.tx(ctx, func(t *sql.Tx) error {
		var cur int64
		err := t.QueryRowContext(ctx, `SELECT version FROM clients WHERE id = ?`, id).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("store: replace client: %w", err)
		}
		if cur != ifVersion {
			return ErrVersionMismatch
		}
		_, err = t.ExecContext(ctx, `UPDATE clients SET mac = ?, smbios_uuid = ?, serial = ?, hostname = ?,
			profile_id = ?, variables = ?, reserved_ip = ?, version = version + 1, updated_at = ? WHERE id = ?`,
			in.MAC, nullStr(in.SMBIOSUUID), nullStr(in.Serial), nullStr(in.Hostname), nullStr(in.ProfileID),
			vars, nullStr(in.ReservedIP), ms(s.now()), id)
		if err != nil {
			return clientWriteErr("replace", err)
		}
		return nil
	})
	if err != nil {
		return Client{}, err
	}
	return s.Client(ctx, id)
}

// DeleteClient deletes the client and its deployment history.
// ErrDeploymentActive while it has a pending or running deployment.
func (s *Store) DeleteClient(ctx context.Context, id string) error {
	return s.tx(ctx, func(t *sql.Tx) error {
		var active int
		if err := t.QueryRowContext(ctx,
			`SELECT count(*) FROM deployments WHERE client_id = ? AND status IN ('pending', 'running')`, id).
			Scan(&active); err != nil {
			return fmt.Errorf("store: delete client: %w", err)
		}
		if active > 0 {
			return ErrDeploymentActive
		}
		res, err := t.ExecContext(ctx, `DELETE FROM clients WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete client: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("store: delete client: %w", err)
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// Sighting is what DHCP (or an agent check-in) learned about a machine.
type Sighting struct {
	MAC        string
	IP         *string // nil when not known yet (e.g. a proxy-mode DISCOVER)
	Arch       *string
	SMBIOSUUID *string
	Serial     *string
}

// RecordSighting creates the client as `discovered` if its MAC is new
// (ADR 0013), otherwise updates its last_* fields and fills in hardware
// details it didn't have. It never changes Version, so a booting machine
// can't invalidate an admin's If-Match. created reports a new client.
func (s *Store) RecordSighting(ctx context.Context, in Sighting) (c Client, created bool, err error) {
	mac, err := NormalizeMAC(in.MAC)
	if err != nil {
		return Client{}, false, err
	}
	now := ms(s.now())
	err = s.tx(ctx, func(t *sql.Tx) error {
		res, err := t.ExecContext(ctx, `INSERT INTO clients
			(id, mac, smbios_uuid, serial, source, last_seen_at, last_ip, last_arch, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (mac) DO NOTHING`,
			newID(), mac, nullStr(in.SMBIOSUUID), nullStr(in.Serial), SourceDiscovered,
			now, nullStr(in.IP), nullStr(in.Arch), now, now)
		if err != nil {
			return fmt.Errorf("store: record sighting: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: record sighting: %w", err)
		}
		if created = n == 1; !created {
			// Keep known values when this sighting lacks them.
			if _, err := t.ExecContext(ctx, `UPDATE clients SET
				last_seen_at = ?,
				last_ip      = coalesce(?, last_ip),
				last_arch    = coalesce(?, last_arch),
				smbios_uuid  = coalesce(smbios_uuid, ?),
				serial       = coalesce(serial, ?)
				WHERE mac = ?`,
				now, nullStr(in.IP), nullStr(in.Arch), nullStr(in.SMBIOSUUID), nullStr(in.Serial), mac); err != nil {
				return fmt.Errorf("store: record sighting: %w", err)
			}
		}
		c, err = s.clientWhere(ctx, t, "mac = ?", mac)
		return err
	})
	return c, created, err
}

// ReservedIPs returns every client reservation as IP -> MAC. Full-mode DHCP
// reads it so no other client is offered a reserved address.
func (s *Store) ReservedIPs(ctx context.Context) (map[netip.Addr]string, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT reserved_ip, mac FROM clients WHERE reserved_ip IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("store: reserved ips: %w", err)
	}
	defer rows.Close()
	out := map[netip.Addr]string{}
	for rows.Next() {
		var ip, mac string
		if err := rows.Scan(&ip, &mac); err != nil {
			return nil, fmt.Errorf("store: reserved ips: %w", err)
		}
		a, err := netip.ParseAddr(ip)
		if err != nil {
			continue // not an IP; the API validates reservedIp, so this is old or hand-edited data
		}
		out[a] = mac
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reserved ips: %w", err)
	}
	return out, nil
}

// ClientsByMAC returns the clients with the given (normalised) MACs, keyed
// by MAC. MACs without a client are absent.
func (s *Store) ClientsByMAC(ctx context.Context, macs []string) (map[string]Client, error) {
	out := map[string]Client{}
	if len(macs) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(macs)), ",")
	args := make([]any, len(macs))
	for i, m := range macs {
		args[i] = m
	}
	rows, err := s.r.QueryContext(ctx, `SELECT `+clientCols+` FROM clients WHERE mac IN (`+ph+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: clients by mac: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, fmt.Errorf("store: clients by mac: %w", err)
		}
		out[c.MAC] = c
	}
	return out, rows.Err()
}
