package server

import (
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/michaelmadell/pxe-netdeploy/internal/dhcp"
	"github.com/michaelmadell/pxe-netdeploy/internal/hostcheck"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
)

// probedPrivileges is worked out once: capabilities only change with a
// restart (ADR 0030).
var probedPrivileges = sync.OnceValue(hostcheck.Probe)

func (s *Server) privileges() hostcheck.Privileges {
	if s.privs != nil {
		return s.privs()
	}
	return probedPrivileges()
}

// hostWarnings: missing_privileges, dhcp_ping_unavailable and
// data_dir_permissions, for what the settings actually use.
func hostWarnings(cfg settings.Config, p hostcheck.Privileges, dataDirProblems []string) []warning {
	var w []warning
	var low []string
	full := false
	for _, in := range cfg.Interfaces {
		svc := in.Services
		need := func(on bool, port int, what string) {
			if on && !p.Port(port) {
				low = append(low, fmt.Sprintf("%s %d on %s", what, port, in.Name))
			}
		}
		need(in.DHCP.Mode == settings.DHCPFull || in.DHCP.Mode == settings.DHCPProxy, 67, "DHCP")
		need(svc.TFTP, 69, "TFTP")
		need(svc.HTTP, svc.HTTPPort, "HTTP")
		need(svc.HTTPS, svc.HTTPSPort, "HTTPS")
		full = full || (in.DHCP.Mode == settings.DHCPFull && in.DHCP.Pool != nil && in.DHCP.Pool.PingCheck)
	}
	if len(low) > 0 {
		w = append(w, warning{"missing_privileges", "This server may not bind ports below 1024, so these can't start: " + strings.Join(low, ", ") +
			". Grant CAP_NET_BIND_SERVICE (systemd: AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_RAW) or use ports from 1024 up."})
	}
	if full && p.ICMP == dhcp.ICMPNone {
		fix := "Grant CAP_NET_RAW (systemd: AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_RAW), or allow the server's group in net.ipv4.ping_group_range."
		if runtime.GOOS == "windows" {
			fix = "Run netdeployd elevated (as a service or from an elevated shell)."
		}
		w = append(w, warning{"dhcp_ping_unavailable", "Full-mode DHCP has the ping check on, but no ICMP socket can be opened: addresses are offered without checking whether they're in use. " + fix})
	}
	if len(dataDirProblems) > 0 {
		w = append(w, warning{"data_dir_permissions", "The data directory isn't private to the server's account: " + strings.Join(dataDirProblems, "; ") + "."})
	}
	return w
}
