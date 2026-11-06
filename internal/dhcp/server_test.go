package dhcp

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/iana"
)

var (
	serverIP = net.IPv4(10, 77, 0, 1).To4()
	mask24   = net.CIDRMask(24, 32)
)

func testServer(t *testing.T, mod func(*Config)) (*Server, *time.Time) {
	t.Helper()
	cfg := Config{
		Interface:    "lab0",
		ServerIP:     serverIP,
		Netmask:      mask24,
		PoolStart:    net.IPv4(10, 77, 0, 100),
		PoolEnd:      net.IPv4(10, 77, 0, 102),
		BootFileUEFI: "bootmgfw.efi",
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mod != nil {
		mod(&cfg)
	}
	s, err := newServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, &now
}

func mac(n byte) net.HardwareAddr { return net.HardwareAddr{0x00, 0x15, 0x5d, 0, 0, n} }

// pxeClient returns modifiers for a UEFI firmware PXE request.
func pxeClient(arch iana.Arch) []dhcpv4.Modifier {
	return []dhcpv4.Modifier{
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier("PXEClient:Arch:00007:UNDI:003016")),
		dhcpv4.WithOption(dhcpv4.OptClientArch(arch)),
	}
}

func discover(t *testing.T, hw net.HardwareAddr, mods ...dhcpv4.Modifier) *dhcpv4.DHCPv4 {
	t.Helper()
	d, err := dhcpv4.NewDiscovery(hw, mods...)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func request(t *testing.T, offer *dhcpv4.DHCPv4, mods ...dhcpv4.Modifier) *dhcpv4.DHCPv4 {
	t.Helper()
	r, err := dhcpv4.NewRequestFromOffer(offer, mods...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPXEClientGetsBootFile(t *testing.T) {
	for _, arch := range []iana.Arch{iana.EFI_X86_64, iana.EFI_BC} {
		s, _ := testServer(t, nil)
		offer := s.Handle(discover(t, mac(1), pxeClient(arch)...))
		if offer == nil || offer.MessageType() != dhcpv4.MessageTypeOffer {
			t.Fatalf("arch %v: no offer", arch)
		}
		if got := offer.YourIPAddr.String(); got != "10.77.0.100" {
			t.Errorf("yiaddr = %s", got)
		}
		if offer.BootFileName != "bootmgfw.efi" || offer.ServerIPAddr.String() != "10.77.0.1" {
			t.Errorf("arch %v: file=%q siaddr=%v", arch, offer.BootFileName, offer.ServerIPAddr)
		}
		if got := offer.ServerIdentifier(); !got.Equal(serverIP) {
			t.Errorf("server id = %v", got)
		}
		if offer.ClassIdentifier() != "" {
			t.Errorf("unexpected option 60 %q", offer.ClassIdentifier())
		}

		ack := s.Handle(request(t, offer, pxeClient(arch)...))
		if ack == nil || ack.MessageType() != dhcpv4.MessageTypeAck {
			t.Fatalf("arch %v: no ack", arch)
		}
		if !ack.YourIPAddr.Equal(offer.YourIPAddr) || ack.BootFileName != "bootmgfw.efi" {
			t.Errorf("ack yiaddr=%v file=%q", ack.YourIPAddr, ack.BootFileName)
		}
		if lt := ack.IPAddressLeaseTime(0); lt != DefaultLeaseTime {
			t.Errorf("lease time = %v", lt)
		}
	}
}

func TestPXEClientClassToggle(t *testing.T) {
	s, _ := testServer(t, func(c *Config) { c.PXEClientClass = true })
	offer := s.Handle(discover(t, mac(1), pxeClient(iana.EFI_X86_64)...))
	if offer.ClassIdentifier() != "PXEClient" {
		t.Errorf("option 60 = %q", offer.ClassIdentifier())
	}
}

// WinPE's own DHCP client needs an address but no boot file. So do BIOS
// PXE clients, which this spike doesn't boot.
func TestNonUEFIClientsGetAddressOnly(t *testing.T) {
	cases := map[string][]dhcpv4.Modifier{
		"winpe": nil,
		"bios-pxe": {
			dhcpv4.WithOption(dhcpv4.OptClassIdentifier("PXEClient:Arch:00000:UNDI:002001")),
			dhcpv4.WithOption(dhcpv4.OptClientArch(iana.INTEL_X86PC)),
		},
		"arch-without-pxe-class": {dhcpv4.WithOption(dhcpv4.OptClientArch(iana.EFI_X86_64))},
	}
	for name, mods := range cases {
		s, _ := testServer(t, nil)
		offer := s.Handle(discover(t, mac(2), mods...))
		if offer == nil || offer.YourIPAddr.String() != "10.77.0.100" {
			t.Fatalf("%s: no address offered", name)
		}
		if offer.BootFileName != "" || offer.Options.Has(dhcpv4.OptionBootfileName) {
			t.Errorf("%s: unexpected boot file %q", name, offer.BootFileName)
		}
	}
}

func TestHTTPBootClient(t *testing.T) {
	httpClient := []dhcpv4.Modifier{
		dhcpv4.WithOption(dhcpv4.OptClassIdentifier("HTTPClient:Arch:00016:UNDI:003001")),
		dhcpv4.WithOption(dhcpv4.OptClientArch(iana.EFI_X86_64_HTTP)),
	}
	s, _ := testServer(t, nil)
	if offer := s.Handle(discover(t, mac(3), httpClient...)); offer.BootFileName != "" {
		t.Errorf("no URL configured, got file %q", offer.BootFileName)
	}

	url := "http://10.77.0.1/boot/winpe.iso"
	s, _ = testServer(t, func(c *Config) { c.HTTPBootURL = url })
	offer := s.Handle(discover(t, mac(3), httpClient...))
	if offer.BootFileName != url || offer.ClassIdentifier() != "HTTPClient" {
		t.Errorf("file=%q class=%q", offer.BootFileName, offer.ClassIdentifier())
	}
}

func TestSameClientKeepsAddress(t *testing.T) {
	s, _ := testServer(t, nil)
	o1 := s.Handle(discover(t, mac(1)))
	s.Handle(request(t, o1))
	o2 := s.Handle(discover(t, mac(2)))
	if o2.YourIPAddr.Equal(o1.YourIPAddr) {
		t.Fatal("second client got the first client's address")
	}
	again := s.Handle(discover(t, mac(1)))
	if !again.YourIPAddr.Equal(o1.YourIPAddr) {
		t.Errorf("client 1 moved from %v to %v", o1.YourIPAddr, again.YourIPAddr)
	}
}

func TestPoolExhaustionAndOfferExpiry(t *testing.T) {
	s, now := testServer(t, nil)
	for i := byte(1); i <= 3; i++ {
		if s.Handle(discover(t, mac(i))) == nil {
			t.Fatalf("client %d: no offer", i)
		}
	}
	if s.Handle(discover(t, mac(4))) != nil {
		t.Fatal("offer from an exhausted pool")
	}
	*now = now.Add(offerHold + time.Second)
	if s.Handle(discover(t, mac(4))) == nil {
		t.Fatal("expired offers were not reclaimed")
	}
}

func TestRequestForOtherServerReleasesOffer(t *testing.T) {
	s, _ := testServer(t, nil)
	offer := s.Handle(discover(t, mac(1)))
	other := request(t, offer, dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.IPv4(10, 77, 0, 2))))
	if s.Handle(other) != nil {
		t.Fatal("answered a request addressed to another server")
	}
	if o := s.Handle(discover(t, mac(2))); !o.YourIPAddr.Equal(offer.YourIPAddr) {
		t.Errorf("released address not reused: got %v", o.YourIPAddr)
	}
}

func TestRequestNAKs(t *testing.T) {
	s, _ := testServer(t, nil)
	o1 := s.Handle(discover(t, mac(1)))
	s.Handle(request(t, o1))

	// Another client asks for client 1's address (INIT-REBOOT, no server id).
	steal, _ := dhcpv4.New(dhcpv4.WithHwAddr(mac(2)),
		dhcpv4.WithMessageType(dhcpv4.MessageTypeRequest),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(o1.YourIPAddr)))
	if r := s.Handle(steal); r == nil || r.MessageType() != dhcpv4.MessageTypeNak {
		t.Errorf("stealing an address: got %v, want NAK", r)
	}

	// A client returning from another network.
	foreign, _ := dhcpv4.New(dhcpv4.WithHwAddr(mac(3)),
		dhcpv4.WithMessageType(dhcpv4.MessageTypeRequest),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(net.IPv4(192, 168, 1, 50))))
	r := s.Handle(foreign)
	if r == nil || r.MessageType() != dhcpv4.MessageTypeNak {
		t.Fatalf("foreign address: got %v, want NAK", r)
	}
	if dst := replyAddr(foreign, r); !dst.IP.Equal(net.IPv4bcast) {
		t.Errorf("NAK sent to %v, want broadcast", dst)
	}
}

// After a server restart WinPE may INIT-REBOOT with an address we have no
// record of. If it is free, confirm it rather than making the client wait.
func TestInitRebootUnknownButFree(t *testing.T) {
	s, _ := testServer(t, nil)
	r, _ := dhcpv4.New(dhcpv4.WithHwAddr(mac(5)),
		dhcpv4.WithMessageType(dhcpv4.MessageTypeRequest),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(net.IPv4(10, 77, 0, 101))))
	if a := s.Handle(r); a == nil || a.MessageType() != dhcpv4.MessageTypeAck {
		t.Fatalf("got %v, want ACK", a)
	}
}

func TestRenewUnicast(t *testing.T) {
	s, _ := testServer(t, nil)
	offer := s.Handle(discover(t, mac(1)))
	ack := s.Handle(request(t, offer))
	renew, err := dhcpv4.NewRenewFromAck(ack)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Handle(renew)
	if r == nil || r.MessageType() != dhcpv4.MessageTypeAck {
		t.Fatalf("renew: got %v", r)
	}
	if dst := replyAddr(renew, r); !dst.IP.Equal(ack.YourIPAddr) || dst.Port != 68 {
		t.Errorf("renew reply to %v, want %v:68", dst, ack.YourIPAddr)
	}
}

func TestDeclineQuarantinesAddress(t *testing.T) {
	s, _ := testServer(t, nil)
	offer := s.Handle(discover(t, mac(1)))
	s.Handle(request(t, offer))
	dec, _ := dhcpv4.New(dhcpv4.WithHwAddr(mac(1)),
		dhcpv4.WithMessageType(dhcpv4.MessageTypeDecline),
		dhcpv4.WithOption(dhcpv4.OptRequestedIPAddress(offer.YourIPAddr)))
	s.Handle(dec)
	if o := s.Handle(discover(t, mac(1))); o.YourIPAddr.Equal(offer.YourIPAddr) {
		t.Errorf("declined address %v offered again", o.YourIPAddr)
	}
}

func TestReplyBroadcastByDefault(t *testing.T) {
	s, _ := testServer(t, nil)
	d := discover(t, mac(1))
	if dst := replyAddr(d, s.Handle(d)); !dst.IP.Equal(net.IPv4bcast) || dst.Port != 68 {
		t.Errorf("offer sent to %v", dst)
	}
}

func TestConfigValidation(t *testing.T) {
	bad := map[string]func(*Config){
		"pool outside subnet":  func(c *Config) { c.PoolEnd = net.IPv4(10, 77, 1, 5) },
		"pool reversed":        func(c *Config) { c.PoolStart, c.PoolEnd = c.PoolEnd, c.PoolStart },
		"pool includes server": func(c *Config) { c.PoolStart = net.IPv4(10, 77, 0, 1) },
		"pool includes bcast":  func(c *Config) { c.PoolEnd = net.IPv4(10, 77, 0, 255) },
		"no pool":              func(c *Config) { c.PoolStart = nil },
		"ipv6 server":          func(c *Config) { c.ServerIP = net.ParseIP("fe80::1") },
	}
	for name, mod := range bad {
		cfg := Config{
			ServerIP: serverIP, Netmask: mask24,
			PoolStart: net.IPv4(10, 77, 0, 100), PoolEnd: net.IPv4(10, 77, 0, 200),
		}
		mod(&cfg)
		if _, err := newServer(cfg); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestTakeLeasesKeepsBindingsAcrossReconfigure(t *testing.T) {
	old, now := testServer(t, nil)
	mac := net.HardwareAddr{0, 0x1b, 0x21, 1, 2, 3}
	old.mu.Lock()
	ip, ok := old.pool.offer(mac.String(), nil, *now)
	if !ok || !old.pool.bind(mac.String(), ip, time.Hour, *now) {
		t.Fatal("setup: no lease")
	}
	old.mu.Unlock()

	same, _ := testServer(t, func(c *Config) { c.LeaseTime = 10 * time.Minute })
	if !same.TakeLeases(old) {
		t.Fatal("same pool range: leases not taken")
	}
	same.mu.Lock()
	other := net.HardwareAddr{0, 0x1b, 0x21, 9, 9, 9}
	got, _ := same.pool.offer(other.String(), ip, *now)
	same.mu.Unlock()
	if got.Equal(ip) {
		t.Fatalf("new server offered %v, which %v still holds", ip, mac)
	}

	moved, _ := testServer(t, func(c *Config) { c.PoolEnd = net.IPv4(10, 77, 0, 101) })
	if moved.TakeLeases(old) {
		t.Error("different pool range: leases should not carry over")
	}
}
