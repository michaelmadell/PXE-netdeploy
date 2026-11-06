package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// Lease states.
const (
	LeaseBound    = "bound"
	LeaseDeclined = "declined"
)

// Lease is a persisted DHCP lease (full mode) or a declined address.
type Lease struct {
	Interface string
	IP        netip.Addr
	MAC       string
	Hostname  *string
	Arch      *string
	State     string
	ExpiresAt time.Time
	UpdatedAt time.Time
}

const leaseCols = `interface, ip, mac, hostname, arch, state, expires_at, updated_at`

func scanLease(r rowScanner) (Lease, error) {
	var (
		l                Lease
		ip               string
		host, arch       sql.NullString
		expires, updated int64
	)
	if err := r.Scan(&l.Interface, &ip, &l.MAC, &host, &arch, &l.State, &expires, &updated); err != nil {
		return Lease{}, err
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return Lease{}, fmt.Errorf("store: lease ip %q: %w", ip, err)
	}
	l.IP = addr
	l.Hostname, l.Arch = fromNullStr(host), fromNullStr(arch)
	l.ExpiresAt, l.UpdatedAt = fromMS(expires), fromMS(updated)
	return l, nil
}

// PutLease records a bound lease (on ACK or renewal). A MAC holds at most one
// bound lease per interface, so a previous lease of this MAC at another
// address is released.
func (s *Store) PutLease(ctx context.Context, l Lease) error {
	mac, err := NormalizeMAC(l.MAC)
	if err != nil {
		return err
	}
	if !l.IP.Is4() {
		return fmt.Errorf("store: put lease: %v is not an IPv4 address", l.IP)
	}
	now := ms(s.now())
	return s.tx(ctx, func(t *sql.Tx) error {
		if _, err := t.ExecContext(ctx,
			`DELETE FROM dhcp_leases WHERE interface = ? AND mac = ? AND state = 'bound' AND ip <> ?`,
			l.Interface, mac, l.IP.String()); err != nil {
			return fmt.Errorf("store: put lease: %w", err)
		}
		if _, err := t.ExecContext(ctx, `INSERT INTO dhcp_leases (`+leaseCols+`)
			VALUES (?, ?, ?, ?, ?, 'bound', ?, ?)
			ON CONFLICT (interface, ip) DO UPDATE SET
				mac = excluded.mac, hostname = excluded.hostname, arch = excluded.arch,
				state = 'bound', expires_at = excluded.expires_at, updated_at = excluded.updated_at`,
			l.Interface, l.IP.String(), mac, nullStr(l.Hostname), nullStr(l.Arch), ms(l.ExpiresAt), now); err != nil {
			return fmt.Errorf("store: put lease: %w", err)
		}
		return nil
	})
}

// DeclineLease marks ip as unusable until `until` (DHCPDECLINE or a failed
// ping check), replacing any lease on it.
func (s *Store) DeclineLease(ctx context.Context, iface string, ip netip.Addr, mac string, until time.Time) error {
	norm, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	_, err = s.w.ExecContext(ctx, `INSERT INTO dhcp_leases (`+leaseCols+`)
		VALUES (?, ?, ?, NULL, NULL, 'declined', ?, ?)
		ON CONFLICT (interface, ip) DO UPDATE SET
			mac = excluded.mac, hostname = NULL, arch = NULL,
			state = 'declined', expires_at = excluded.expires_at, updated_at = excluded.updated_at`,
		iface, ip.String(), norm, ms(until), ms(s.now()))
	if err != nil {
		return fmt.Errorf("store: decline lease: %w", err)
	}
	return nil
}

// ReleaseLease deletes mac's bound lease on iface (DHCPRELEASE). No error if
// there is none.
func (s *Store) ReleaseLease(ctx context.Context, iface, mac string) error {
	norm, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	if _, err := s.w.ExecContext(ctx, `DELETE FROM dhcp_leases WHERE interface = ? AND mac = ? AND state = 'bound'`,
		iface, norm); err != nil {
		return fmt.Errorf("store: release lease: %w", err)
	}
	return nil
}

// LeaseByIP returns the unexpired bound lease on ip, or ErrNotFound. TFTP
// uses it to map a client IP to its MAC after a restart lost the in-memory
// boot sessions.
func (s *Store) LeaseByIP(ctx context.Context, iface string, ip netip.Addr) (Lease, error) {
	l, err := scanLease(s.r.QueryRowContext(ctx, `SELECT `+leaseCols+` FROM dhcp_leases
		WHERE interface = ? AND ip = ? AND state = 'bound' AND expires_at > ?`,
		iface, ip.String(), ms(s.now())))
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, ErrNotFound
	}
	if err != nil {
		return Lease{}, fmt.Errorf("store: read lease: %w", err)
	}
	return l, nil
}

// Leases returns the unexpired leases (bound and declined) on iface, or on
// every interface when iface is empty, ordered by interface then address.
// Used to rebuild the pool at startup and for GET /dhcp/leases.
func (s *Store) Leases(ctx context.Context, iface string) ([]Lease, error) {
	q, args := `SELECT `+leaseCols+` FROM dhcp_leases WHERE expires_at > ?`, []any{ms(s.now())}
	if iface != "" {
		q, args = q+` AND interface = ?`, append(args, iface)
	}
	rows, err := s.r.QueryContext(ctx, q+` ORDER BY interface, ip`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list leases: %w", err)
	}
	defer rows.Close()
	var out []Lease
	for rows.Next() {
		l, err := scanLease(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list leases: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list leases: %w", err)
	}
	// Text order puts 10.0.0.10 before 10.0.0.9; sort numerically.
	slices.SortFunc(out, func(a, b Lease) int {
		if c := strings.Compare(a.Interface, b.Interface); c != 0 {
			return c
		}
		return a.IP.Compare(b.IP)
	})
	return out, nil
}

// PurgeExpiredLeases deletes leases that expired before `before` and returns
// how many.
func (s *Store) PurgeExpiredLeases(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM dhcp_leases WHERE expires_at <= ?`, ms(before))
	if err != nil {
		return 0, fmt.Errorf("store: purge leases: %w", err)
	}
	return res.RowsAffected()
}
