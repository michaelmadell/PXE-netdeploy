package server

import (
	"strings"
	"testing"

	"github.com/michaelmadell/pxe-netdeploy/internal/dhcp"
	"github.com/michaelmadell/pxe-netdeploy/internal/hostcheck"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
)

func TestHostWarnings(t *testing.T) {
	cfg := settings.Defaults()
	in := settings.Interface{Name: "eth1", Services: settings.Services{TFTP: true, HTTP: true, HTTPPort: 8080, HTTPS: true, HTTPSPort: 443},
		DHCP: settings.DHCP{Mode: settings.DHCPFull, Pool: &settings.Pool{PingCheck: true}}}
	cfg.Interfaces = []settings.Interface{in}
	codes := func(w []warning) string {
		var c []string
		for _, x := range w {
			c = append(c, x.Code)
		}
		return strings.Join(c, ",")
	}

	all := hostcheck.Privileges{LowPorts: true, ICMP: dhcp.ICMPRaw}
	if w := hostWarnings(cfg, all, nil); len(w) != 0 {
		t.Errorf("all privileges: %v", w)
	}
	none := hostcheck.Privileges{ICMP: dhcp.ICMPNone}
	w := hostWarnings(cfg, none, []string{"secrets.key can be read by other accounts"})
	if codes(w) != "missing_privileges,dhcp_ping_unavailable,data_dir_permissions" {
		t.Fatalf("codes %s", codes(w))
	}
	msg := w[0].Message
	if !strings.Contains(msg, "DHCP 67 on eth1") || !strings.Contains(msg, "TFTP 69") || !strings.Contains(msg, "HTTPS 443") || strings.Contains(msg, "HTTP 8080") {
		t.Errorf("missing_privileges: %s", msg)
	}
	// The datagram ICMP socket is enough for the ping check.
	if w := hostWarnings(cfg, hostcheck.Privileges{LowPorts: true, ICMP: dhcp.ICMPDatagram}, nil); len(w) != 0 {
		t.Errorf("datagram ping: %v", w)
	}
	// Nothing configured that needs them: no warning.
	cfg.Interfaces = nil
	if w := hostWarnings(cfg, none, nil); len(w) != 0 {
		t.Errorf("no interfaces: %v", w)
	}
}
