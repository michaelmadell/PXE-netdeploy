// Package dhcp is netdeployd's DHCP server.
//
// Full mode only, one Server per interface, in-memory leases (kept across
// settings changes by TakeLeases). Every client on the interface gets an
// address (WinPE needs one after boot); UEFI x64 PXE clients also get the
// boot file, and UEFI x64 HTTP Boot clients get a boot URL when one is
// configured. Proxy mode, the ADR 0013 boot decision and persisted leases
// arrive in Phase 2 step 3.
package dhcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/insomniacslk/dhcp/iana"
)

// Config configures a Server.
type Config struct {
	Interface string     // interface name (Windows: the adapter alias, e.g. "vEthernet (NetDeployLab)")
	ServerIP  net.IP     // this host's address on Interface; default: its first IPv4 address
	Netmask   net.IPMask // default: from Interface
	PoolStart net.IP
	PoolEnd   net.IP
	Router    net.IP   // optional (option 3)
	DNS       []net.IP // optional (option 6)
	LeaseTime time.Duration

	// BootFileUEFI is sent to UEFI x64 PXE clients (option 93 = 7 or 9),
	// e.g. "bootmgfw.efi". The TFTP server is ServerIP.
	BootFileUEFI string
	// HTTPBootURL is sent to UEFI x64 HTTP Boot clients (option 93 = 16).
	// Empty: those clients get an address only.
	HTTPBootURL string
	// PXEClientClass adds vendor class "PXEClient" (option 60) to PXE
	// replies. Off by default: UEFI firmware takes siaddr + file without it.
	// A spike toggle in case some firmware ignores offers without it.
	PXEClientClass bool

	Logger *slog.Logger
}

// DefaultLeaseTime is used when Config.LeaseTime is zero.
const DefaultLeaseTime = time.Hour

// Server answers DHCPv4 on one interface.
type Server struct {
	cfg    Config
	subnet *net.IPNet
	log    *slog.Logger

	mu   sync.Mutex
	pool *pool
	now  func() time.Time
}

// New validates cfg, filling ServerIP and Netmask from the interface when
// they are unset.
func New(cfg Config) (*Server, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Interface == "" {
		return nil, errors.New("dhcp: interface is required")
	}
	if cfg.ServerIP == nil || cfg.Netmask == nil {
		ipnet, err := interfaceIPv4(cfg.Interface, cfg.ServerIP)
		if err != nil {
			return nil, err
		}
		if cfg.ServerIP == nil {
			cfg.ServerIP = ipnet.IP
		}
		if cfg.Netmask == nil {
			cfg.Netmask = ipnet.Mask
		}
	}
	return newServer(cfg)
}

// Check validates a fully specified cfg (ServerIP and Netmask set) without
// touching interfaces or sockets.
func Check(cfg Config) error {
	_, err := newServer(cfg)
	return err
}

// newServer validates a fully specified cfg without touching interfaces.
func newServer(cfg Config) (*Server, error) {
	if cfg.ServerIP.To4() == nil {
		return nil, fmt.Errorf("dhcp: server IP %v is not IPv4", cfg.ServerIP)
	}
	cfg.ServerIP = cfg.ServerIP.To4()
	if ones, bits := cfg.Netmask.Size(); bits != 32 || ones == 0 {
		return nil, fmt.Errorf("dhcp: invalid netmask %v", cfg.Netmask)
	}
	subnet := &net.IPNet{IP: cfg.ServerIP.Mask(cfg.Netmask), Mask: cfg.Netmask}
	if cfg.PoolStart.To4() == nil || cfg.PoolEnd.To4() == nil {
		return nil, errors.New("dhcp: pool start and end are required")
	}
	s, e := ip4ToUint(cfg.PoolStart), ip4ToUint(cfg.PoolEnd)
	if s > e {
		return nil, fmt.Errorf("dhcp: pool start %v is after end %v", cfg.PoolStart, cfg.PoolEnd)
	}
	if !subnet.Contains(cfg.PoolStart) || !subnet.Contains(cfg.PoolEnd) {
		return nil, fmt.Errorf("dhcp: pool %v-%v is not inside %v", cfg.PoolStart, cfg.PoolEnd, subnet)
	}
	network, broadcast := ip4ToUint(subnet.IP), ip4ToUint(subnet.IP)|^ip4ToUint(net.IP(cfg.Netmask))
	if s == network || e == broadcast {
		return nil, fmt.Errorf("dhcp: pool %v-%v includes the network or broadcast address", cfg.PoolStart, cfg.PoolEnd)
	}
	if srv := ip4ToUint(cfg.ServerIP); srv >= s && srv <= e {
		return nil, fmt.Errorf("dhcp: pool %v-%v includes the server address %v", cfg.PoolStart, cfg.PoolEnd, cfg.ServerIP)
	}
	if cfg.LeaseTime <= 0 {
		cfg.LeaseTime = DefaultLeaseTime
	}
	return &Server{
		cfg:    cfg,
		subnet: subnet,
		log:    cfg.Logger.With("svc", "dhcp"),
		pool:   newPool(cfg.PoolStart, cfg.PoolEnd),
		now:    time.Now,
	}, nil
}

// interfaceIPv4 returns want's network on iface, or iface's first IPv4
// network if want is nil.
func interfaceIPv4(name string, want net.IP) (*net.IPNet, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("dhcp: interface %q: %w (names: %s)", name, err, interfaceNames())
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, fmt.Errorf("dhcp: addresses of %q: %w", name, err)
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.To4() == nil {
			continue
		}
		if want == nil || ipnet.IP.Equal(want) {
			return &net.IPNet{IP: ipnet.IP.To4(), Mask: ipnet.Mask}, nil
		}
	}
	if want != nil {
		return nil, fmt.Errorf("dhcp: %v is not an address of %q", want, name)
	}
	return nil, fmt.Errorf("dhcp: interface %q has no IPv4 address", name)
}

func interfaceNames() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return "unavailable"
	}
	names := make([]string, 0, len(ifs))
	for _, i := range ifs {
		names = append(names, fmt.Sprintf("%q", i.Name))
	}
	return strings.Join(names, ", ")
}

// ListenAndServe binds UDP 67 and serves until ctx ends.
//
// Linux: binds 0.0.0.0:67 with SO_BINDTODEVICE, so broadcasts are received
// and replies leave through Interface. Windows: binds ServerIP:67; whether
// broadcasts reach a socket bound to a unicast address is a Phase 1
// question, so the bind is logged.
func (s *Server) ListenAndServe(ctx context.Context) error {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: dhcpv4.ServerPort}
	if runtime.GOOS == "windows" {
		addr.IP = s.cfg.ServerIP
	}
	conn, err := server4.NewIPv4UDPConn(s.cfg.Interface, addr)
	if err != nil {
		return fmt.Errorf("dhcp: listen on %s (%s): %w", s.cfg.Interface, addr, err)
	}
	s.log.Info("dhcp listener started", "iface", s.cfg.Interface, "bind", addr.String(),
		"server_ip", s.cfg.ServerIP.String(), "subnet", s.subnet.String(),
		"pool", s.cfg.PoolStart.String()+"-"+s.cfg.PoolEnd.String(),
		"boot_file", s.cfg.BootFileUEFI, "http_boot_url", s.cfg.HTTPBootURL)
	return s.Serve(ctx, conn)
}

// Serve handles packets from conn until ctx ends.
func (s *Server) Serve(ctx context.Context, conn net.PacketConn) error {
	var cur atomic.Pointer[Server]
	cur.Store(s)
	return ServeSwappable(ctx, conn, &cur)
}

// ServeSwappable handles packets from conn with whichever Server cur holds
// when each packet arrives, until ctx ends (which closes conn). Storing a new
// Server in cur reconfigures the listener without rebinding its socket.
func ServeSwappable(ctx context.Context, conn net.PacketConn, cur *atomic.Pointer[Server]) error {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	buf := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(buf)
		s := cur.Load()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			s.log.Warn("dhcp read error", "err", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		req, err := dhcpv4.FromBytes(buf[:n])
		if err != nil {
			s.log.Warn("dhcp malformed packet", "from", peer.String(), "len", n, "err", err)
			continue
		}
		resp := s.Handle(req)
		if resp == nil {
			continue
		}
		dst := replyAddr(req, resp)
		if _, err := conn.WriteTo(resp.ToBytes(), dst); err != nil {
			s.log.Error("dhcp send failed", "to", dst.String(), "mac", req.ClientHWAddr.String(), "err", err)
		}
	}
}

// TakeLeases copies old's leases and offers into s when both serve the same
// pool range, so a settings change doesn't forget who holds which address.
// It reports whether anything was copied. Call it before s starts serving.
func (s *Server) TakeLeases(old *Server) bool {
	if old == nil || old == s {
		return false
	}
	old.mu.Lock()
	defer old.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if old.pool.start != s.pool.start || old.pool.end != s.pool.end {
		return false
	}
	s.pool = old.pool.clone()
	return true
}

// Handle returns the reply to req, or nil for no reply. It logs the request
// and the reply.
func (s *Server) Handle(req *dhcpv4.DHCPv4) *dhcpv4.DHCPv4 {
	if req.OpCode != dhcpv4.OpcodeBootRequest {
		return nil
	}
	c := classify(req)
	mt := req.MessageType()
	log := s.log.With("mac", req.ClientHWAddr.String(), "xid", req.TransactionID.String())
	log.Info("dhcp rx", "type", mt.String(), "arch", c.archString(), "class", c.vendorClass,
		"requested_ip", ipString(req.RequestedIPAddress()), "ciaddr", ipString(req.ClientIPAddr),
		"giaddr", ipString(req.GatewayIPAddr), "server_id", ipString(req.ServerIdentifier()))

	if len(req.ClientHWAddr) != 6 {
		log.Warn("dhcp ignoring non-Ethernet client", "hlen", len(req.ClientHWAddr))
		return nil
	}

	s.mu.Lock()
	resp, why := s.decide(req, mt, c)
	s.mu.Unlock()

	if resp == nil {
		if why != "" {
			log.Info("dhcp no reply", "reason", why)
		}
		return nil
	}
	log.Info("dhcp tx", "type", resp.MessageType().String(), "yiaddr", ipString(resp.YourIPAddr),
		"boot", c.kind(s.cfg), "file", resp.BootFileName, "siaddr", ipString(resp.ServerIPAddr),
		"to", replyAddr(req, resp).String())
	return resp
}

// decide runs the DHCP state machine. Callers hold s.mu.
func (s *Server) decide(req *dhcpv4.DHCPv4, mt dhcpv4.MessageType, c client) (*dhcpv4.DHCPv4, string) {
	mac := req.ClientHWAddr.String()
	now := s.now()

	switch mt {
	case dhcpv4.MessageTypeDiscover:
		ip, ok := s.pool.offer(mac, req.RequestedIPAddress(), now)
		if !ok {
			return nil, "pool exhausted"
		}
		return s.reply(req, dhcpv4.MessageTypeOffer, ip, c), ""

	case dhcpv4.MessageTypeRequest:
		if sid := req.ServerIdentifier(); sid != nil && !sid.Equal(s.cfg.ServerIP) {
			// SELECTING state, client chose another server.
			s.pool.release(mac)
			return nil, "client selected server " + sid.String()
		}
		ip := req.RequestedIPAddress()
		if ip == nil || ip.IsUnspecified() {
			ip = req.ClientIPAddr // RENEWING / REBINDING
		}
		if ip == nil || ip.IsUnspecified() {
			return nil, "request without an address"
		}
		if !s.pool.bind(mac, ip, s.cfg.LeaseTime, now) {
			// Includes INIT-REBOOT with an address from another network.
			return s.nak(req, "address not available"), ""
		}
		return s.reply(req, dhcpv4.MessageTypeAck, ip, c), ""

	case dhcpv4.MessageTypeRelease:
		s.pool.release(mac)
		return nil, "released"

	case dhcpv4.MessageTypeDecline:
		s.pool.decline(req.RequestedIPAddress(), now)
		return nil, "declined " + ipString(req.RequestedIPAddress())

	case dhcpv4.MessageTypeInform:
		return s.reply(req, dhcpv4.MessageTypeAck, nil, c), ""
	}
	return nil, "unhandled message type"
}

// reply builds an OFFER or ACK. yiaddr is nil for INFORM.
func (s *Server) reply(req *dhcpv4.DHCPv4, mt dhcpv4.MessageType, yiaddr net.IP, c client) *dhcpv4.DHCPv4 {
	mods := []dhcpv4.Modifier{
		dhcpv4.WithMessageType(mt),
		dhcpv4.WithServerIP(s.cfg.ServerIP),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(s.cfg.ServerIP)),
		dhcpv4.WithNetmask(s.cfg.Netmask),
	}
	if yiaddr != nil {
		mods = append(mods,
			dhcpv4.WithYourIP(yiaddr),
			dhcpv4.WithLeaseTime(uint32(s.cfg.LeaseTime/time.Second)))
	}
	if s.cfg.Router != nil {
		mods = append(mods, dhcpv4.WithRouter(s.cfg.Router))
	}
	if len(s.cfg.DNS) > 0 {
		mods = append(mods, dhcpv4.WithDNS(s.cfg.DNS...))
	}
	switch c.kind(s.cfg) {
	case bootPXE:
		mods = append(mods,
			dhcpv4.WithOption(dhcpv4.OptTFTPServerName(s.cfg.ServerIP.String())),
			dhcpv4.WithOption(dhcpv4.OptBootFileName(s.cfg.BootFileUEFI)))
		if s.cfg.PXEClientClass {
			mods = append(mods, dhcpv4.WithOption(dhcpv4.OptClassIdentifier("PXEClient")))
		}
	case bootHTTP:
		mods = append(mods,
			dhcpv4.WithOption(dhcpv4.OptClassIdentifier("HTTPClient")),
			dhcpv4.WithOption(dhcpv4.OptBootFileName(s.cfg.HTTPBootURL)))
	}
	resp, err := dhcpv4.NewReplyFromRequest(req, mods...)
	if err != nil {
		// Only fails if the random transaction ID can't be generated,
		// which NewReplyFromRequest doesn't do. Treat as a bug.
		s.log.Error("dhcp build reply", "err", err)
		return nil
	}
	switch c.kind(s.cfg) {
	case bootPXE:
		resp.BootFileName = s.cfg.BootFileUEFI
	case bootHTTP:
		resp.BootFileName = s.cfg.HTTPBootURL
	}
	return resp
}

func (s *Server) nak(req *dhcpv4.DHCPv4, msg string) *dhcpv4.DHCPv4 {
	resp, err := dhcpv4.NewReplyFromRequest(req,
		dhcpv4.WithMessageType(dhcpv4.MessageTypeNak),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(s.cfg.ServerIP)),
		dhcpv4.WithOption(dhcpv4.OptMessage(msg)),
	)
	if err != nil {
		s.log.Error("dhcp build nak", "err", err)
		return nil
	}
	return resp
}

// replyAddr picks the destination (RFC 2131 4.1). Unicast to yiaddr would
// need an ARP entry we can't add from a UDP socket, so clients without an
// address get a broadcast.
func replyAddr(req, resp *dhcpv4.DHCPv4) *net.UDPAddr {
	if gi := req.GatewayIPAddr; gi != nil && !gi.IsUnspecified() {
		return &net.UDPAddr{IP: gi, Port: dhcpv4.ServerPort}
	}
	if resp.MessageType() != dhcpv4.MessageTypeNak {
		if ci := req.ClientIPAddr; ci != nil && !ci.IsUnspecified() {
			return &net.UDPAddr{IP: ci, Port: dhcpv4.ClientPort}
		}
	}
	return &net.UDPAddr{IP: net.IPv4bcast, Port: dhcpv4.ClientPort}
}

type bootKind string

const (
	bootNone bootKind = "none"
	bootPXE  bootKind = "pxe-uefi-x64"
	bootHTTP bootKind = "http-uefi-x64"
)

// client is what the request says about the firmware.
type client struct {
	arch        []iana.Arch
	vendorClass string
}

func classify(req *dhcpv4.DHCPv4) client {
	return client{arch: req.ClientArch(), vendorClass: req.ClassIdentifier()}
}

func (c client) hasArch(a ...iana.Arch) bool {
	for _, have := range c.arch {
		for _, want := range a {
			if have == want {
				return true
			}
		}
	}
	return false
}

func (c client) kind(cfg Config) bootKind {
	switch {
	case strings.HasPrefix(c.vendorClass, "PXEClient") && cfg.BootFileUEFI != "" &&
		c.hasArch(iana.EFI_X86_64, iana.EFI_BC):
		return bootPXE
	case strings.HasPrefix(c.vendorClass, "HTTPClient") && cfg.HTTPBootURL != "" &&
		c.hasArch(iana.EFI_X86_64_HTTP):
		return bootHTTP
	}
	return bootNone
}

func (c client) archString() string {
	if len(c.arch) == 0 {
		return "-"
	}
	parts := make([]string, len(c.arch))
	for i, a := range c.arch {
		parts[i] = fmt.Sprintf("%d(%s)", uint16(a), a.String())
	}
	return strings.Join(parts, ",")
}

func ipString(ip net.IP) string {
	if ip == nil || ip.IsUnspecified() {
		return "-"
	}
	return ip.String()
}
