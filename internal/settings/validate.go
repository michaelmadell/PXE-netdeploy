package settings

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// FieldError is one entry of a Problem's errors[]: field is a path such as
// "interfaces[0].dhcp.pool.start".
type FieldError struct {
	Field   string
	Message string
}

func (e FieldError) String() string { return e.Field + ": " + e.Message }

type fieldErrors []FieldError

func (f *fieldErrors) add(field, format string, args ...any) {
	*f = append(*f, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}

func (f fieldErrors) list() []FieldError { return f }

// HostInterface is an OS network interface and its IPv4 networks.
type HostInterface struct {
	Name     string
	Prefixes []netip.Prefix // IPv4 only, primary first (as the OS lists them)
}

// Primary is the interface's first IPv4 network.
func (h HostInterface) Primary() (netip.Prefix, bool) {
	if len(h.Prefixes) == 0 {
		return netip.Prefix{}, false
	}
	return h.Prefixes[0], true
}

// Host answers questions about the machine netdeployd runs on.
type Host interface {
	// Interface returns the named interface, or an error if it doesn't exist.
	Interface(name string) (HostInterface, error)
}

// SystemHost reads the real network interfaces.
type SystemHost struct{}

// ErrNoInterface: the OS has no interface with that name.
var ErrNoInterface = errors.New("no such interface")

// Interface implements Host.
func (SystemHost) Interface(name string) (HostInterface, error) {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return HostInterface{}, fmt.Errorf("%w %q (have: %s)", ErrNoInterface, name, interfaceNames())
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return HostInterface{}, fmt.Errorf("read addresses of %q: %w", name, err)
	}
	out := HostInterface{Name: name}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(ipn.IP)
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if !ip.Is4() {
			continue
		}
		ones, _ := ipn.Mask.Size()
		out.Prefixes = append(out.Prefixes, netip.PrefixFrom(ip, ones))
	}
	return out, nil
}

func interfaceNames() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return "unknown"
	}
	names := make([]string, 0, len(ifs))
	for _, i := range ifs {
		names = append(names, fmt.Sprintf("%q", i.Name))
	}
	return strings.Join(names, ", ")
}

// endpoint is a socket a config will need, for conflict checks.
type endpoint struct {
	proto string // "tcp" or "udp"
	addr  netip.Addr
	port  int
	field string
	what  string
}

func (e endpoint) overlaps(o endpoint) bool {
	if e.proto != o.proto || e.port != o.port {
		return false
	}
	return e.addr == o.addr || e.addr.IsUnspecified() || o.addr.IsUnspecified()
}

// Validate checks cfg against the host: interfaces exist and have an IPv4
// address, pools fit their interface's subnet, and no two services want the
// same socket. Modes not implemented yet are refused here too.
func Validate(cfg Config, host Host) []FieldError {
	var errs fieldErrors
	var eps []endpoint

	if !cfg.Admin.BindAddress.IsValid() {
		errs.add("admin.bindAddress", "must be an IPv4 address")
	} else {
		eps = append(eps, endpoint{"tcp", cfg.Admin.BindAddress, cfg.Admin.Port, "admin", "admin listener"})
	}

	seen := map[string]int{}
	for i, in := range cfg.Interfaces {
		path := fmt.Sprintf("interfaces[%d]", i)
		if j, dup := seen[strings.ToLower(in.Name)]; dup {
			errs.add(path+".name", "%q is already configured as interfaces[%d]", in.Name, j)
			continue
		}
		seen[strings.ToLower(in.Name)] = i

		hi, err := host.Interface(in.Name)
		if err != nil {
			errs.add(path+".name", "%v", err)
			continue
		}
		primary, ok := hi.Primary()
		if !ok {
			errs.add(path+".name", "interface %q has no IPv4 address", in.Name)
			continue
		}
		ip := primary.Addr()

		switch in.DHCP.Mode {
		case DHCPOff:
		case DHCPProxy:
			errs.add(path+".dhcp.mode", "proxy mode is not implemented yet (Phase 2 step 3); use full or off")
		case DHCPFull:
			// No endpoint check for UDP 67: Linux binds 0.0.0.0:67 per device
			// and Windows binds the interface address, so two interfaces
			// never share a DHCP socket.
			validatePool(path+".dhcp.pool", in.DHCP.Pool, primary, &errs)
		default:
			errs.add(path+".dhcp.mode", "unknown mode %q", in.DHCP.Mode)
		}

		if in.Services.TFTP {
			eps = append(eps, endpoint{"udp", ip, 69, path + ".services.tftp", "TFTP on " + in.Name})
		}
		if in.Services.HTTP {
			eps = append(eps, endpoint{"tcp", ip, in.Services.HTTPPort, path + ".services.httpPort", "HTTP content on " + in.Name})
		}
		if in.Services.HTTPS {
			eps = append(eps, endpoint{"tcp", ip, in.Services.HTTPSPort, path + ".services.httpsPort", "HTTPS content on " + in.Name})
		}
	}

	for i := range eps {
		for j := range i {
			if eps[i].overlaps(eps[j]) {
				errs.add(eps[i].field, "%s on %s/%d conflicts with %s (%s)",
					eps[i].what, eps[i].proto, eps[i].port, eps[j].what, eps[j].field)
			}
		}
	}
	return errs.list()
}

func validatePool(path string, p *Pool, iface netip.Prefix, errs *fieldErrors) {
	if p == nil {
		errs.add(path, "required for full mode")
		return
	}
	if !p.Start.IsValid() || !p.End.IsValid() || !p.SubnetMask.IsValid() {
		return // already reported by FromAPI
	}
	subnet := iface.Masked()
	if ones := maskBits(p.SubnetMask); ones != subnet.Bits() {
		errs.add(path+".subnetMask", "%s (/%d) doesn't match the interface's network %s", p.SubnetMask, ones, subnet)
		return
	}
	if !subnet.Contains(p.Start) {
		errs.add(path+".start", "%s is outside the interface's network %s", p.Start, subnet)
	}
	if !subnet.Contains(p.End) {
		errs.add(path+".end", "%s is outside the interface's network %s", p.End, subnet)
	}
	if p.End.Less(p.Start) {
		errs.add(path+".end", "%s is before start %s", p.End, p.Start)
		return
	}
	if p.Start == subnet.Addr() {
		errs.add(path+".start", "%s is the network address", p.Start)
	}
	if p.End == broadcast(subnet) {
		errs.add(path+".end", "%s is the broadcast address", p.End)
	}
	if server := iface.Addr(); !server.Less(p.Start) && !p.End.Less(server) {
		errs.add(path, "pool %s-%s includes this server's address %s", p.Start, p.End, server)
	}
	if p.Router.IsValid() && !subnet.Contains(p.Router) {
		errs.add(path+".router", "%s is outside the interface's network %s", p.Router, subnet)
	}
}

// maskBits returns the prefix length of a contiguous IPv4 mask, or -1.
func maskBits(mask netip.Addr) int {
	b := mask.As4()
	ones, bits := net.IPMask(b[:]).Size()
	if bits == 0 {
		return -1
	}
	return ones
}

func broadcast(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	v |= host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}
