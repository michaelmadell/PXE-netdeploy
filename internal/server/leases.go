package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/netip"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// ListDhcpLeases implements GET /dhcp/leases: bound, unexpired leases on
// interfaces in full mode (declined addresses are left out), ordered by
// interface then address.
func (s *Server) ListDhcpLeases(ctx context.Context, req api.ListDhcpLeasesRequestObject) (api.ListDhcpLeasesResponseObject, error) {
	p := req.Params
	limit := 50
	if p.Limit != nil {
		limit = *p.Limit
	}
	var after leaseCursor
	if p.Cursor != nil && *p.Cursor != "" {
		c, ok := parseLeaseCursor(*p.Cursor)
		if !ok {
			return api.ListDhcpLeases400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
				newProblem(http.StatusBadRequest, "invalid_cursor", "Invalid cursor", "Use nextCursor from a previous page.", nil))}, nil
		}
		after = c
	}
	iface := ""
	if p.Interface != nil {
		iface = *p.Interface
	}
	mac := ""
	if p.Mac != nil {
		norm, err := store.NormalizeMAC(*p.Mac)
		if err != nil {
			return api.ListDhcpLeases400ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse: api.ProblemApplicationProblemPlusJSONResponse(
				newProblem(http.StatusBadRequest, "invalid_request", "Invalid request", "mac is not a MAC address.", nil))}, nil
		}
		mac = norm
	}

	all, err := s.store.Leases(ctx, iface)
	if err != nil {
		return nil, err
	}
	var page []store.Lease
	next := ""
	for _, l := range all {
		if l.State != store.LeaseBound || (mac != "" && l.MAC != mac) || !after.before(l) {
			continue
		}
		if len(page) == limit {
			next = leaseCursor{page[len(page)-1].Interface, page[len(page)-1].IP}.String()
			break
		}
		page = append(page, l)
	}

	macs := make([]string, len(page))
	for i, l := range page {
		macs[i] = l.MAC
	}
	clients, err := s.store.ClientsByMAC(ctx, macs)
	if err != nil {
		return nil, err
	}
	items := make([]api.DhcpLease, len(page))
	for i, l := range page {
		it := api.DhcpLease{Interface: l.Interface, Ip: l.IP.String(), Mac: l.MAC, Hostname: l.Hostname, ExpiresAt: l.ExpiresAt}
		if l.Arch != nil {
			a := api.ClientArch(*l.Arch)
			it.Arch = &a
		}
		reserved := false
		if c, ok := clients[l.MAC]; ok {
			it.ClientId = &c.ID
			reserved = c.ReservedIP != nil && *c.ReservedIP == l.IP.String()
		}
		it.Reserved = &reserved
		items[i] = it
	}
	out := api.ListDhcpLeases200JSONResponse{Items: items}
	if next != "" {
		out.NextCursor = &next
	}
	return out, nil
}

// leaseCursor is the last (interface, ip) of a page; zero starts at the top.
type leaseCursor struct {
	iface string
	ip    netip.Addr
}

func (c leaseCursor) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.iface + "\x00" + c.ip.String()))
}

func parseLeaseCursor(s string) (leaseCursor, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return leaseCursor{}, false
	}
	iface, ip, ok := strings.Cut(string(b), "\x00")
	if !ok {
		return leaseCursor{}, false
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return leaseCursor{}, false
	}
	return leaseCursor{iface, a}, true
}

// before reports whether l comes after the cursor (store order).
func (c leaseCursor) before(l store.Lease) bool {
	if !c.ip.IsValid() {
		return true
	}
	if l.Interface != c.iface {
		return l.Interface > c.iface
	}
	return c.ip.Less(l.IP)
}
