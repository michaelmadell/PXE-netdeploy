// Package dhcp is netdeployd's DHCP server, one Server per interface.
//
// Full mode hands out addresses from a pool, with leases written to the store
// before each ACK, and adds boot options for network-boot clients. Proxy mode
// (RFC 4578 / PXE 2.1 proxyDHCP) never assigns addresses: it answers PXE and
// HTTP Boot clients' DISCOVERs on UDP 67 with a boot-only offer, answers
// REQUESTs on UDP 4011, and watches the client's REQUEST to the real DHCP
// server to learn its IP. In both modes the boot answer comes from the same
// place (Server.boot): the architecture and MAC filters, a sighting in the
// client table (ADR 0013), the Booter, and a boot session for TFTP.
package dhcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/insomniacslk/dhcp/iana"

	"github.com/michaelmadell/pxe-netdeploy/internal/boot"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Mode is how a Server answers.
type Mode string

// Modes (Settings.interfaces[].dhcp.mode, except "off", which runs no Server).
const (
	ModeFull  Mode = "full"
	ModeProxy Mode = "proxy"
)

// ProxyPort is the PXE boot server port proxy mode also listens on.
const ProxyPort = 4011

// Archs says which client architectures get a boot answer
// (Settings.architectures).
type Archs struct {
	BIOS, UEFIX64, UEFIArm64, HTTPBoot bool
}

// AllArchs answers every architecture.
var AllArchs = Archs{BIOS: true, UEFIX64: true, UEFIArm64: true, HTTPBoot: true}

func (a Archs) allows(arch boot.Arch) bool {
	switch arch {
	case boot.ArchBIOS:
		return a.BIOS
	case boot.ArchUEFIX64:
		return a.UEFIX64
	case boot.ArchUEFIArm64:
		return a.UEFIArm64
	case boot.ArchUEFIX64HTTP:
		return a.HTTPBoot && a.UEFIX64
	case boot.ArchUEFIArm64HTTP:
		return a.HTTPBoot && a.UEFIArm64
	}
	return false
}

// Config configures a Server.
type Config struct {
	Mode      Mode       // default full
	Interface string     // interface name (Windows: the adapter alias, e.g. "vEthernet (NetDeployLab)")
	ServerIP  net.IP     // this host's address on Interface; default: its first IPv4 address
	Netmask   net.IPMask // default: from Interface

	// Full mode.
	PoolStart  net.IP
	PoolEnd    net.IP
	Router     net.IP   // optional (option 3)
	DNS        []net.IP // optional (option 6)
	DomainName string   // optional (option 15)
	LeaseTime  time.Duration
	PingCheck  bool   // ICMP-probe addresses with no lease history before offering them
	Pinger     Pinger // nil: ICMP echo (needs administrator/root)

	// Who gets a boot answer.
	Archs       *Archs   // nil: all
	MACPrefixes []string // lower-case, colon-separated; empty: everyone

	// What they boot (ADR 0024). Booter nil uses boot.Static{BootFileUEFI,
	// HTTPBootURL} (protocol tests).
	Booter       boot.Booter
	BootFileUEFI string
	HTTPBootURL  string
	// PXEClientClass adds vendor class "PXEClient" (option 60) to full-mode
	// PXE replies. Off by default: UEFI firmware takes siaddr + file without
	// it. Proxy replies always carry it, as the PXE spec requires.
	PXEClientClass bool

	// Shared state. Store nil keeps leases in memory and records no
	// sightings (tests). Sessions nil uses a private table.
	Store    *store.Store
	Sessions *boot.Sessions
	Events   *events.Bus // client.updated (seen/created) and lease.updated; nil: none

	Logger *slog.Logger
}

// DefaultLeaseTime is used when Config.LeaseTime is zero.
const DefaultLeaseTime = time.Hour

// Server answers DHCPv4 on one interface.
type Server struct {
	cfg    Config
	subnet *net.IPNet
	log    *slog.Logger
	booter boot.Booter

	mu   sync.Mutex // serialises the state machine and lease writes
	pool *pool      // full mode only
	now  func() time.Time

	pingWarn sync.Once
}

// New validates cfg, filling ServerIP and Netmask from the interface when
// they are unset, and in full mode loads the interface's leases from the
// store.
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
	s, err := newServer(cfg)
	if err != nil {
		return nil, err
	}
	if err := s.loadLeases(); err != nil {
		return nil, err
	}
	return s, nil
}

// Check validates a fully specified cfg (ServerIP and Netmask set) without
// touching interfaces, sockets or the store.
func Check(cfg Config) error {
	_, err := newServer(cfg)
	return err
}

// newServer validates a fully specified cfg without touching interfaces.
func newServer(cfg Config) (*Server, error) {
	if cfg.Mode == "" {
		cfg.Mode = ModeFull
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.ServerIP.To4() == nil {
		return nil, fmt.Errorf("dhcp: server IP %v is not IPv4", cfg.ServerIP)
	}
	cfg.ServerIP = cfg.ServerIP.To4()
	if ones, bits := cfg.Netmask.Size(); bits != 32 || ones == 0 {
		return nil, fmt.Errorf("dhcp: invalid netmask %v", cfg.Netmask)
	}
	subnet := &net.IPNet{IP: cfg.ServerIP.Mask(cfg.Netmask), Mask: cfg.Netmask}
	s := &Server{
		cfg:    cfg,
		subnet: subnet,
		log:    cfg.Logger.With("svc", "dhcp", "iface", cfg.Interface, "mode", string(cfg.Mode)),
		now:    time.Now,
	}
	if cfg.Booter != nil {
		s.booter = cfg.Booter
	} else {
		s.booter = boot.Static{File: cfg.BootFileUEFI, URL: cfg.HTTPBootURL}
	}
	if s.cfg.Sessions == nil {
		s.cfg.Sessions = &boot.Sessions{}
	}

	switch cfg.Mode {
	case ModeProxy:
		return s, nil
	case ModeFull:
	default:
		return nil, fmt.Errorf("dhcp: unknown mode %q", cfg.Mode)
	}

	if cfg.PoolStart.To4() == nil || cfg.PoolEnd.To4() == nil {
		return nil, errors.New("dhcp: pool start and end are required")
	}
	st, e := ip4ToUint(cfg.PoolStart), ip4ToUint(cfg.PoolEnd)
	if st > e {
		return nil, fmt.Errorf("dhcp: pool start %v is after end %v", cfg.PoolStart, cfg.PoolEnd)
	}
	if !subnet.Contains(cfg.PoolStart) || !subnet.Contains(cfg.PoolEnd) {
		return nil, fmt.Errorf("dhcp: pool %v-%v is not inside %v", cfg.PoolStart, cfg.PoolEnd, subnet)
	}
	network, broadcast := ip4ToUint(subnet.IP), ip4ToUint(subnet.IP)|^ip4ToUint(net.IP(cfg.Netmask))
	if st == network || e == broadcast {
		return nil, fmt.Errorf("dhcp: pool %v-%v includes the network or broadcast address", cfg.PoolStart, cfg.PoolEnd)
	}
	if srv := ip4ToUint(cfg.ServerIP); srv >= st && srv <= e {
		return nil, fmt.Errorf("dhcp: pool %v-%v includes the server address %v", cfg.PoolStart, cfg.PoolEnd, cfg.ServerIP)
	}
	if s.cfg.LeaseTime <= 0 {
		s.cfg.LeaseTime = DefaultLeaseTime
	}
	s.pool = newPool(cfg.PoolStart, cfg.PoolEnd)
	return s, nil
}

// loadLeases fills the pool from the store (full mode with a store).
func (s *Server) loadLeases() error {
	if s.cfg.Mode != ModeFull || s.cfg.Store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	leases, err := s.cfg.Store.Leases(ctx, s.cfg.Interface)
	if err != nil {
		return fmt.Errorf("dhcp: load leases for %s: %w", s.cfg.Interface, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, l := range leases {
		ip := net.IP(l.IP.AsSlice())
		if !s.subnet.Contains(ip) {
			continue // the interface's network changed; the lease expires on its own
		}
		u := ip4ToUint(ip)
		switch l.State {
		case store.LeaseBound:
			s.pool.set(u, l.MAC, l.ExpiresAt, true)
			n++
		case store.LeaseDeclined:
			s.pool.declined[u] = l.ExpiresAt
		}
	}
	if n > 0 {
		s.log.Info("dhcp leases loaded", "count", n)
	}
	return nil
}

// TakeLeases copies old's pool (leases and pending offers) into s when both
// serve the same range, so a settings change doesn't forget an offer that
// hasn't been ACKed yet. Bound leases are in the store either way. It
// reports whether anything was copied. Call it before s starts serving.
func (s *Server) TakeLeases(old *Server) bool {
	if old == nil || old == s || old.pool == nil || s.pool == nil {
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
// and replies leave through Interface. Windows: binds ServerIP:67, which
// receives broadcasts too (Phase 1 finding).
func (s *Server) ListenAndServe(ctx context.Context) error {
	addr := &net.UDPAddr{IP: net.IPv4zero, Port: dhcpv4.ServerPort}
	if runtime.GOOS == "windows" {
		addr.IP = s.cfg.ServerIP
	}
	conn, err := server4.NewIPv4UDPConn(s.cfg.Interface, addr)
	if err != nil {
		return fmt.Errorf("dhcp: listen on %s (%s): %w", s.cfg.Interface, addr, err)
	}
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

	localPort := dhcpv4.ServerPort
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok && a.Port != 0 {
		localPort = a.Port
	}
	s := cur.Load()
	s.log.Info("dhcp listener serving", "bind", conn.LocalAddr().String(), "server_ip", s.cfg.ServerIP.String())

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
		udpPeer, _ := peer.(*net.UDPAddr)
		resp, dst := s.Respond(ctx, req, udpPeer, localPort)
		if resp == nil {
			continue
		}
		if _, err := conn.WriteTo(resp.ToBytes(), dst); err != nil {
			s.log.Error("dhcp send failed", "to", dst.String(), "mac", req.ClientHWAddr.String(), "err", err)
		}
	}
}

// Handle answers req as if it arrived on UDP 67 (tests and tools).
func (s *Server) Handle(req *dhcpv4.DHCPv4) *dhcpv4.DHCPv4 {
	resp, _ := s.Respond(context.Background(), req, nil, dhcpv4.ServerPort)
	return resp
}

// Respond returns the reply to req, received from peer on localPort, and
// where to send it; nil for no reply. It logs the request and the reply.
func (s *Server) Respond(ctx context.Context, req *dhcpv4.DHCPv4, peer *net.UDPAddr, localPort int) (*dhcpv4.DHCPv4, *net.UDPAddr) {
	if req.OpCode != dhcpv4.OpcodeBootRequest {
		return nil, nil
	}
	c := classify(req)
	mt := req.MessageType()
	log := s.log.With("mac", req.ClientHWAddr.String(), "xid", req.TransactionID.String())
	log.Info("dhcp rx", "type", mt.String(), "port", localPort, "arch", c.archString(), "class", c.vendorClass,
		"requested_ip", ipString(req.RequestedIPAddress()), "ciaddr", ipString(req.ClientIPAddr),
		"giaddr", ipString(req.GatewayIPAddr), "server_id", ipString(req.ServerIdentifier()))

	if len(req.ClientHWAddr) != 6 {
		log.Warn("dhcp ignoring non-Ethernet client", "hlen", len(req.ClientHWAddr))
		return nil, nil
	}

	var (
		resp *dhcpv4.DHCPv4
		dst  *net.UDPAddr
		why  string
	)
	if s.cfg.Mode == ModeProxy {
		resp, dst, why = s.proxy(ctx, req, mt, c, peer, localPort)
	} else {
		resp, why = s.full(ctx, req, mt, c)
		if resp != nil {
			dst = replyAddr(req, resp)
		}
	}
	if resp == nil {
		if why != "" {
			log.Info("dhcp no reply", "reason", why)
		}
		return nil, nil
	}
	log.Info("dhcp tx", "type", resp.MessageType().String(), "yiaddr", ipString(resp.YourIPAddr),
		"file", resp.BootFileName, "siaddr", ipString(resp.ServerIPAddr), "to", dst.String())
	return resp, dst
}

// boot decides the boot answer for a network-boot client (ADR 0013): the
// filters, a sighting, then the Booter. ok=false with a reason when the
// client gets no boot answer; reason is empty for clients that aren't
// network booting at all. ip is the client's address when known.
func (s *Server) boot(ctx context.Context, req *dhcpv4.DHCPv4, c client, ip net.IP) (boot.Answer, boot.Arch, bool, string) {
	if !c.pxe() && !c.http() {
		return boot.Answer{}, "", false, ""
	}
	arch, known := c.bootArch()
	if !known {
		return boot.Answer{}, "", false, "unsupported architecture " + c.archString()
	}
	archs := AllArchs
	if s.cfg.Archs != nil {
		archs = *s.cfg.Archs
	}
	if !archs.allows(arch) {
		return boot.Answer{}, arch, false, "architecture " + string(arch) + " is disabled in settings"
	}
	mac := req.ClientHWAddr.String()
	if !macAllowed(mac, s.cfg.MACPrefixes) {
		return boot.Answer{}, arch, false, "MAC not in this interface's macPrefixes"
	}
	s.sighting(ctx, mac, ip, arch, c.uuid)
	ans, ok, reason := s.booter.Boot(ctx, boot.Request{Interface: s.cfg.Interface, MAC: mac, Arch: arch})
	return ans, arch, ok, reason
}

// sighting records the client (ADR 0013: clients are created at first
// contact), at most once per Sessions.SightingGap per MAC. Errors are logged:
// a failed write must not stop a machine booting.
func (s *Server) sighting(ctx context.Context, mac string, ip net.IP, arch boot.Arch, uuid string) {
	ipStr := ""
	if ip != nil && !ip.IsUnspecified() {
		ipStr = ip.String()
	}
	if s.cfg.Store == nil || !s.cfg.Sessions.SightingDue(mac, ipStr) {
		return
	}
	sg := store.Sighting{MAC: mac, Arch: ptr(string(arch))}
	if ipStr != "" {
		sg.IP = &ipStr
	}
	if uuid != "" {
		sg.SMBIOSUUID = &uuid
	}
	c, created, err := s.cfg.Store.RecordSighting(ctx, sg)
	if err != nil {
		s.log.Error("dhcp could not record client", "mac", mac, "err", err)
		return
	}
	change := "seen"
	if created {
		change = "created"
		s.log.Info("new client discovered", "mac", mac, "client_id", c.ID, "arch", string(arch))
	}
	s.cfg.Events.Publish(events.Event{Type: events.ClientUpdated, ResourceID: c.ID,
		Data: map[string]string{"id": c.ID, "mac": mac, "change": change}})
}

func (s *Server) session(ip net.IP, mac string, arch boot.Arch, ans boot.Answer) {
	a, ok := netip.AddrFromSlice(ip.To4())
	if !ok || a.IsUnspecified() {
		return
	}
	s.cfg.Sessions.Record(boot.Session{Interface: s.cfg.Interface, IP: a, MAC: mac, Arch: arch, Answer: ans})
}

func macAllowed(mac string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(mac, p) {
			return true
		}
	}
	return false
}

func ptr[T any](v T) *T { return &v }

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

// client is what the request says about the firmware.
type client struct {
	arch        []iana.Arch
	vendorClass string
	uuid        string // SMBIOS UUID from option 97, if any
	uuidRaw     []byte // option 97 as received, echoed in proxy replies
}

func classify(req *dhcpv4.DHCPv4) client {
	c := client{arch: req.ClientArch(), vendorClass: req.ClassIdentifier()}
	if raw := req.GetOneOption(dhcpv4.OptionClientMachineIdentifier); len(raw) > 0 {
		c.uuidRaw = raw
		c.uuid = smbiosUUID(raw)
	}
	return c
}

func (c client) pxe() bool  { return strings.HasPrefix(c.vendorClass, "PXEClient") }
func (c client) http() bool { return strings.HasPrefix(c.vendorClass, "HTTPClient") }

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

// bootArch maps option 93 to the API's ClientArch.
func (c client) bootArch() (boot.Arch, bool) {
	switch {
	case c.http() && c.hasArch(iana.EFI_X86_64_HTTP):
		return boot.ArchUEFIX64HTTP, true
	case c.http() && c.hasArch(iana.EFI_ARM64_HTTP):
		return boot.ArchUEFIArm64HTTP, true
	case c.pxe() && c.hasArch(iana.EFI_X86_64, iana.EFI_BC):
		return boot.ArchUEFIX64, true
	case c.pxe() && c.hasArch(iana.EFI_ARM64):
		return boot.ArchUEFIArm64, true
	case c.pxe() && (c.hasArch(iana.INTEL_X86PC) || len(c.arch) == 0):
		return boot.ArchBIOS, true
	}
	return "", false
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

// smbiosUUID renders option 97 (type 0 + 16-byte GUID) as a UUID string.
// Firmware sends the SMBIOS UUID with its first three fields little-endian,
// as Windows displays it (Win32_ComputerSystemProduct.UUID). Untested against
// real firmware: compare with nd-win's UUID in the proxy lab run.
func smbiosUUID(raw []byte) string {
	if len(raw) != 17 || raw[0] != 0 {
		return ""
	}
	b := raw[1:]
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[3], b[2], b[1], b[0], b[5], b[4], b[7], b[6], b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func ipString(ip net.IP) string {
	if ip == nil || ip.IsUnspecified() {
		return "-"
	}
	return ip.String()
}
