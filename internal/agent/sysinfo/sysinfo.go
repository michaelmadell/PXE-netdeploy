// Package sysinfo reads the machine's identity for check-in: its MACs and
// the SMBIOS system information (manufacturer, model, serial, UUID).
package sysinfo

import (
	"fmt"
	"net"
	"strings"
)

// System is SMBIOS type 1.
type System struct {
	Manufacturer string
	Model        string
	Serial       string
	UUID         string // lower-case, standard text form; "" if unset
}

// ParseSMBIOS finds the type 1 structure in raw SMBIOS table data.
func ParseSMBIOS(t []byte) (System, bool) {
	for len(t) >= 4 {
		typ, l := t[0], int(t[1])
		if l < 4 || l > len(t) {
			return System{}, false
		}
		// The strings follow the formatted area, ending with a double NUL.
		end := l
		for end+1 < len(t) && !(t[end] == 0 && t[end+1] == 0) {
			end++
		}
		strs := strings.Split(string(t[l:end]), "\x00")
		str := func(off int) string {
			if off >= l || t[off] == 0 || int(t[off]) > len(strs) {
				return ""
			}
			return strings.TrimSpace(strs[t[off]-1])
		}
		if typ == 1 && l >= 0x19 {
			s := System{Manufacturer: str(4), Model: str(5), Serial: str(7)}
			u := t[8:24]
			allZero, allFF := true, true
			for _, b := range u {
				allZero = allZero && b == 0
				allFF = allFF && b == 0xFF
			}
			if !allZero && !allFF {
				// The first three fields are little-endian (SMBIOS 2.6+).
				s.UUID = fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
					u[3], u[2], u[1], u[0], u[5], u[4], u[7], u[6], u[8], u[9], u[10], u[11], u[12], u[13], u[14], u[15])
			}
			return s, true
		}
		if typ == 127 {
			return System{}, false
		}
		t = t[min(end+2, len(t)):]
	}
	return System{}, false
}

// MACs lists the Ethernet addresses of the machine's network adapters.
func MACs() []string {
	ifaces, _ := net.Interfaces()
	var out []string
	for _, i := range ifaces {
		if i.Flags&net.FlagLoopback != 0 || len(i.HardwareAddr) != 6 {
			continue
		}
		out = append(out, i.HardwareAddr.String())
	}
	return out
}

// IPv4 lists the up interfaces' usable IPv4 addresses (not link-local).
func IPv4() []string {
	ifaces, _ := net.Interfaces()
	var out []string
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
				out = append(out, n.IP.String())
			}
		}
	}
	return out
}
