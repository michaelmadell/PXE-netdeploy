// Package hostcheck reports what the server process may do on this host
// and whether its data directory is private (ADR 0030).
package hostcheck

import "github.com/michaelmadell/pxe-netdeploy/internal/dhcp"

// Linux capabilities the server needs.
const (
	CapNetBindService = "CAP_NET_BIND_SERVICE"
	CapNetRaw         = "CAP_NET_RAW"
	Elevation         = "elevation"
)

// Privileges is what this process may do.
type Privileges struct {
	// LowPorts: may bind ports below 1024.
	LowPorts bool
	// ICMP: how the DHCP ping check can send (dhcp.ICMPRaw, ICMPDatagram,
	// ICMPNone).
	ICMP string
	// Elevated: Windows, an elevated token (DISM needs it).
	Elevated bool
	// Missing: privileges to grant, e.g. CAP_NET_RAW.
	Missing []string
}

// Probe works out the process's privileges now.
func Probe() Privileges {
	p := probe()
	p.ICMP = dhcp.ProbeICMP()
	p.Missing = missing(p)
	return p
}

// Port reports whether this process may bind port.
func (p Privileges) Port(port int) bool { return port >= 1024 || p.LowPorts }
