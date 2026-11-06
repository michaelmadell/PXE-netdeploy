package dhcp

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"

	"github.com/michaelmadell/pxe-netdeploy/internal/boot"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// uuidOpt is option 97 as UEFI firmware sends it: type 0 + 16-byte GUID.
var uuidOpt = append([]byte{0}, 0x44, 0x45, 0x4c, 0x4c, 0x10, 0x00, 0x34, 0x80, 0x80, 0x31, 0xb7, 0xc0, 0x4f, 0x32, 0x32, 0x32)

func withUUID() dhcpv4.Modifier {
	return dhcpv4.WithOption(dhcpv4.OptGeneric(dhcpv4.OptionClientMachineIdentifier, uuidOpt))
}

func proxyServer(t *testing.T, mod func(*Config)) *Server {
	t.Helper()
	s, _ := testServer(t, func(c *Config) {
		c.Mode = ModeProxy
		c.PoolStart, c.PoolEnd = nil, nil
		if mod != nil {
			mod(c)
		}
	})
	return s
}

func respond(s *Server, req *dhcpv4.DHCPv4, port int, peer *net.UDPAddr) (*dhcpv4.DHCPv4, *net.UDPAddr) {
	return s.Respond(context.Background(), req, peer, port)
}

func TestProxyOffer(t *testing.T) {
	s := proxyServer(t, nil)
	req := discover(t, mac(1), append(pxeClient(iana.EFI_X86_64), withUUID())...)
	offer, dst := respond(s, req, 67, nil)
	if offer == nil {
		t.Fatal("no proxy offer")
	}
	if offer.MessageType() != dhcpv4.MessageTypeOffer || !offer.YourIPAddr.IsUnspecified() {
		t.Errorf("type %v yiaddr %v: want an OFFER without an address", offer.MessageType(), offer.YourIPAddr)
	}
	if !offer.ServerIPAddr.Equal(serverIP) || !offer.ServerIdentifier().Equal(serverIP) {
		t.Errorf("siaddr %v server id %v", offer.ServerIPAddr, offer.ServerIdentifier())
	}
	if offer.ClassIdentifier() != "PXEClient" || offer.BootFileName != "bootmgfw.efi" {
		t.Errorf("class %q file %q", offer.ClassIdentifier(), offer.BootFileName)
	}
	if got := offer.GetOneOption(dhcpv4.OptionVendorSpecificInformation); !bytes.Equal(got, []byte{6, 1, 8, 255}) {
		t.Errorf("option 43 = %x, want PXE_DISCOVERY_CONTROL=8", got)
	}
	if got := offer.GetOneOption(dhcpv4.OptionClientMachineIdentifier); !bytes.Equal(got, uuidOpt) {
		t.Errorf("option 97 not echoed: %x", got)
	}
	if offer.Options.Has(dhcpv4.OptionIPAddressLeaseTime) || offer.Options.Has(dhcpv4.OptionSubnetMask) {
		t.Error("proxy offer carries addressing options")
	}
	if !dst.IP.Equal(net.IPv4bcast) || dst.Port != dhcpv4.ClientPort {
		t.Errorf("sent to %v, want broadcast:68", dst)
	}
}

func TestProxyIgnoresWhatIsntItsBusiness(t *testing.T) {
	s := proxyServer(t, nil)
	// A non-PXE client (e.g. WinPE's own DHCP, a printer): the real server's job.
	if r, _ := respond(s, discover(t, mac(1)), 67, nil); r != nil {
		t.Error("answered a non-PXE DISCOVER")
	}
	// A DISCOVER on 4011 isn't a boot server request.
	if r, _ := respond(s, discover(t, mac(1), pxeClient(iana.EFI_X86_64)...), ProxyPort, nil); r != nil {
		t.Error("answered a DISCOVER on 4011")
	}
}

func TestProxyLearnsAddressFromRealServerRequest(t *testing.T) {
	sessions := &boot.Sessions{}
	s := proxyServer(t, func(c *Config) { c.Sessions = sessions })
	req, err := dhcpv4.New(dhcpv4.WithHwAddr(mac(1)), dhcpv4.WithMessageType(dhcpv4.MessageTypeRequest),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(net.IPv4(10, 77, 0, 150))),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.IPv4(10, 77, 0, 254))))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range pxeClient(iana.EFI_X86_64) {
		m(req)
	}
	if r, _ := respond(s, req, 67, nil); r != nil {
		t.Fatal("answered a REQUEST meant for the real DHCP server")
	}
	sess, ok := sessions.Lookup("lab0", netip.MustParseAddr("10.77.0.150"))
	if !ok || sess.MAC != mac(1).String() || sess.Answer.File != "bootmgfw.efi" {
		t.Fatalf("session = %+v, %v", sess, ok)
	}
}

func TestProxyBootServerAck(t *testing.T) {
	s := proxyServer(t, nil)
	req, err := dhcpv4.New(dhcpv4.WithHwAddr(mac(1)), dhcpv4.WithMessageType(dhcpv4.MessageTypeRequest),
		dhcpv4.WithClientIP(net.IPv4(10, 77, 0, 150)))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range pxeClient(iana.EFI_X86_64) {
		m(req)
	}
	peer := &net.UDPAddr{IP: net.IPv4(10, 77, 0, 150), Port: ProxyPort}
	ack, dst := respond(s, req, ProxyPort, peer)
	if ack == nil || ack.MessageType() != dhcpv4.MessageTypeAck || ack.BootFileName != "bootmgfw.efi" {
		t.Fatalf("ack = %v", ack)
	}
	if dst.String() != peer.String() {
		t.Errorf("sent to %v, want the requester %v", dst, peer)
	}
}

func TestProxyHTTPBoot(t *testing.T) {
	url := "http://10.77.0.1/boot.iso"
	s := proxyServer(t, func(c *Config) { c.HTTPBootURL = url })
	req := discover(t, mac(1),
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier("HTTPClient:Arch:00016:UNDI:003001")),
		dhcpv4.WithOption(dhcpv4.OptClientArch(iana.EFI_X86_64_HTTP)))
	offer, _ := respond(s, req, 67, nil)
	if offer == nil || offer.ClassIdentifier() != "HTTPClient" || offer.BootFileName != url {
		t.Fatalf("offer = %v", offer)
	}
	if offer.Options.Has(dhcpv4.OptionVendorSpecificInformation) {
		t.Error("HTTP Boot offer carries PXE vendor options")
	}
}

func TestFilters(t *testing.T) {
	noX64 := &Archs{UEFIArm64: true}
	pxe := pxeClient(iana.EFI_X86_64)

	if r, _ := respond(proxyServer(t, func(c *Config) { c.Archs = noX64 }), discover(t, mac(1), pxe...), 67, nil); r != nil {
		t.Error("proxy answered a disabled architecture")
	}
	if r, _ := respond(proxyServer(t, func(c *Config) { c.MACPrefixes = []string{"00:1b:21"} }), discover(t, mac(1), pxe...), 67, nil); r != nil {
		t.Error("proxy answered a MAC outside macPrefixes")
	}
	if r, _ := respond(proxyServer(t, func(c *Config) { c.MACPrefixes = []string{"00:15:5d"} }), discover(t, mac(1), pxe...), 67, nil); r == nil {
		t.Error("proxy ignored a MAC inside macPrefixes")
	}

	// Full mode: filtered clients still get an address, just no boot file.
	s, _ := testServer(t, func(c *Config) { c.MACPrefixes = []string{"00:1b:21"} })
	offer := s.Handle(discover(t, mac(1), pxe...))
	if offer == nil || offer.YourIPAddr.IsUnspecified() || offer.BootFileName != "" {
		t.Fatalf("full mode filtered client: %v", offer)
	}
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestFullModeLeasesSurviveRestart(t *testing.T) {
	st := testStore(t)
	s, _ := testServer(t, func(c *Config) { c.Store = st })
	s.now = time.Now // the store judges expiry by the real clock
	offer := s.Handle(discover(t, mac(1)))
	if ack := s.Handle(request(t, offer)); ack == nil || ack.MessageType() != dhcpv4.MessageTypeAck {
		t.Fatalf("ack = %v", ack)
	}
	leases, err := st.Leases(context.Background(), "lab0")
	if err != nil || len(leases) != 1 || leases[0].MAC != mac(1).String() {
		t.Fatalf("stored leases %+v, %v", leases, err)
	}

	// A new server (restart) loads the lease: another client can't have it,
	// and the first one gets it back.
	s2, _ := testServer(t, func(c *Config) { c.Store = st })
	s2.now = time.Now
	if err := s2.loadLeases(); err != nil {
		t.Fatal(err)
	}
	if o := s2.Handle(discover(t, mac(2))); o.YourIPAddr.Equal(offer.YourIPAddr) {
		t.Errorf("restarted server offered %v to another client", o.YourIPAddr)
	}
	if o := s2.Handle(discover(t, mac(1))); !o.YourIPAddr.Equal(offer.YourIPAddr) {
		t.Errorf("restarted server offered %v to the lease holder, want %v", o.YourIPAddr, offer.YourIPAddr)
	}
}

func TestFullModeNoAckWithoutStoredLease(t *testing.T) {
	st := testStore(t)
	s, _ := testServer(t, func(c *Config) { c.Store = st })
	offer := s.Handle(discover(t, mac(1)))
	st.Close() // every write now fails
	if ack := s.Handle(request(t, offer)); ack != nil {
		t.Fatalf("acknowledged a lease the store didn't record: %v", ack)
	}
}

func TestReservation(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	reserved := "10.77.0.50" // outside the pool (100-102), inside the subnet
	if _, err := st.CreateClient(ctx, store.ClientInput{MAC: mac(1).String(), ReservedIP: &reserved}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateClient(ctx, store.ClientInput{MAC: mac(9).String(), ReservedIP: ptr("10.77.0.100")}); err != nil {
		t.Fatal(err)
	}
	s, _ := testServer(t, func(c *Config) { c.Store = st })

	offer := s.Handle(discover(t, mac(1)))
	if offer.YourIPAddr.String() != reserved {
		t.Fatalf("offered %v, want the reservation %s", offer.YourIPAddr, reserved)
	}
	if ack := s.Handle(request(t, offer)); ack == nil || ack.MessageType() != dhcpv4.MessageTypeAck {
		t.Fatalf("reservation not acknowledged: %v", ack)
	}
	// 10.77.0.100 is reserved for mac(9): nobody else is offered it.
	if o := s.Handle(discover(t, mac(2))); o.YourIPAddr.String() == "10.77.0.100" {
		t.Error("offered another client's reserved address")
	}
}

type fakePinger struct {
	alive map[string]bool
	err   error
	calls int
}

func (f *fakePinger) Ping(_ context.Context, ip net.IP, _ time.Duration) (bool, error) {
	f.calls++
	return f.alive[ip.String()], f.err
}

func TestPingCheck(t *testing.T) {
	p := &fakePinger{alive: map[string]bool{"10.77.0.100": true}}
	s, _ := testServer(t, func(c *Config) { c.PingCheck, c.Pinger = true, p })
	offer := s.Handle(discover(t, mac(1)))
	if offer.YourIPAddr.String() != "10.77.0.101" {
		t.Fatalf("offered %v, want .101 (.100 answers ping)", offer.YourIPAddr)
	}
	// The same client asking again is not re-pinged (no longer fresh).
	calls := p.calls
	s.Handle(discover(t, mac(1)))
	if p.calls != calls {
		t.Error("re-pinged an address already offered")
	}

	// Without ICMP the check is skipped, not fatal.
	broken := &fakePinger{err: errors.New("operation not permitted")}
	s2, _ := testServer(t, func(c *Config) { c.PingCheck, c.Pinger = true, broken })
	if o := s2.Handle(discover(t, mac(1))); o == nil || o.YourIPAddr.String() != "10.77.0.100" {
		t.Fatalf("offer without ICMP: %v", o)
	}
}

func TestSightingCreatesClientOnce(t *testing.T) {
	st := testStore(t)
	s := proxyServer(t, func(c *Config) { c.Store = st })
	req := discover(t, mac(1), append(pxeClient(iana.EFI_X86_64), withUUID())...)
	respond(s, req, 67, nil)
	respond(s, req, 67, nil) // retransmit: throttled, no second write

	c, err := st.ClientByMAC(context.Background(), mac(1).String())
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != store.SourceDiscovered || *c.LastArch != "uefi-x64" {
		t.Errorf("client %+v", c)
	}
	if c.SMBIOSUUID == nil || *c.SMBIOSUUID != "4c4c4544-0010-8034-8031-b7c04f323232" {
		t.Errorf("smbios uuid %v", c.SMBIOSUUID)
	}
	// Non-PXE traffic never creates clients.
	full, _ := testServer(t, func(c *Config) { c.Store = st })
	full.Handle(discover(t, mac(7)))
	if _, err := st.ClientByMAC(context.Background(), mac(7).String()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a non-PXE DHCP client was recorded: %v", err)
	}
}

// The lab run (2026-11-06): DISCOVER, then the REQUEST to the real server
// 4 s later. The address from the REQUEST must reach the client record.
func TestProxySightingKeepsAddressFromRequest(t *testing.T) {
	st := testStore(t)
	s := proxyServer(t, func(c *Config) { c.Store = st })
	respond(s, discover(t, mac(1), pxeClient(iana.EFI_X86_64)...), 67, nil)

	req, err := dhcpv4.New(dhcpv4.WithHwAddr(mac(1)), dhcpv4.WithMessageType(dhcpv4.MessageTypeRequest),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(net.IPv4(10, 77, 0, 140))),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.IPv4(10, 77, 0, 2))))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range pxeClient(iana.EFI_X86_64) {
		m(req)
	}
	respond(s, req, 67, nil)

	c, err := st.ClientByMAC(context.Background(), mac(1).String())
	if err != nil {
		t.Fatal(err)
	}
	if c.LastIP == nil || *c.LastIP != "10.77.0.140" {
		t.Fatalf("lastIp = %v, want 10.77.0.140", c.LastIP)
	}
}
