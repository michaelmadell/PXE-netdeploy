package server

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// agentReachable reports whether any of a boot image's serverUrls points
// at an endpoint this server serves: http:// URLs against httpEPs,
// https:// URLs against httpsEPs (ADR 0028). A URL naming a host by DNS
// name counts when its port is served (the name can't be checked here).
func agentReachable(serverURLs []string, httpEPs, httpsEPs []netip.AddrPort) bool {
	for _, raw := range serverURLs {
		u, ok := parseServerURL(raw)
		if !ok {
			continue
		}
		eps := httpEPs
		if u.https {
			eps = httpsEPs
		}
		for _, e := range eps {
			if int(e.Port()) != u.port {
				continue
			}
			if !u.addr.IsValid() || u.addr == e.Addr().Unmap() {
				return true
			}
		}
	}
	return false
}

// agentOverHTTP returns the boot image's http:// URLs whose address also
// serves HTTPS (agent_over_http, ADR 0028): the boot prompt password and
// unattend secrets travel in clear though HTTPS is on there. URLs naming a
// host by DNS name aren't counted.
func agentOverHTTP(serverURLs []string, httpsEPs []netip.AddrPort) []string {
	var out []string
	for _, raw := range serverURLs {
		u, ok := parseServerURL(raw)
		if !ok || u.https || !u.addr.IsValid() {
			continue
		}
		for _, e := range httpsEPs {
			if e.Addr().Unmap() == u.addr {
				out = append(out, raw)
				break
			}
		}
	}
	return out
}

type serverURL struct {
	https bool
	addr  netip.Addr // invalid: a DNS name
	port  int
}

func parseServerURL(raw string) (serverURL, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return serverURL{}, false
	}
	var out serverURL
	switch strings.ToLower(u.Scheme) {
	case "http":
		out.port = 80
	case "https":
		out.https, out.port = true, 443
	default:
		return serverURL{}, false
	}
	if p := u.Port(); p != "" {
		if out.port, err = strconv.Atoi(p); err != nil {
			return serverURL{}, false
		}
	}
	if a, err := netip.ParseAddr(u.Hostname()); err == nil {
		out.addr = a.Unmap()
	}
	return out, true
}
