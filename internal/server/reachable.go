package server

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// agentReachable reports whether any of a boot image's serverUrls points
// at an endpoint this server serves HTTP on. A URL naming a host by DNS
// name counts when its port is served (the name can't be checked here);
// https never counts until the content listener has HTTPS (Phase 6).
func agentReachable(serverURLs []string, endpoints []netip.AddrPort) bool {
	for _, raw := range serverURLs {
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Scheme, "http") {
			continue
		}
		port := 80
		if p := u.Port(); p != "" {
			if port, err = strconv.Atoi(p); err != nil {
				continue
			}
		}
		addr, perr := netip.ParseAddr(u.Hostname())
		byName := perr != nil
		for _, e := range endpoints {
			if int(e.Port()) != port {
				continue
			}
			if byName || addr.Unmap() == e.Addr().Unmap() {
				return true
			}
		}
	}
	return false
}
