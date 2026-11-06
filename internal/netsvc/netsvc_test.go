package netsvc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/dhcp"
	"github.com/michaelmadell/pxe-netdeploy/internal/tftp"
)

func testCert(t *testing.T) *tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "t"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// sockets records what the Manager opens and can make chosen binds fail.
// Plans use port 0 so every bind is a fresh loopback port.
type sockets struct {
	mu     sync.Mutex
	opened map[string]int // plan address -> times opened
	fail   map[string]bool
}

func newSockets() *sockets { return &sockets{opened: map[string]int{}, fail: map[string]bool{}} }

func (s *sockets) note(addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail[addr] {
		return errors.New("bind: address already in use")
	}
	s.opened[addr]++
	return nil
}

func (s *sockets) count(addr string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opened[addr]
}

func (s *sockets) options() Options {
	return Options{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		AdminGrace: 50 * time.Millisecond,
		ListenDHCP: func(iface string, addr netip.AddrPort) (net.PacketConn, error) {
			if err := s.note("dhcp " + iface + " " + addr.String()); err != nil {
				return nil, err
			}
			return net.ListenPacket("udp4", "127.0.0.1:0")
		},
		ListenUDP: func(addr netip.AddrPort) (net.PacketConn, error) {
			if err := s.note("udp " + addr.String()); err != nil {
				return nil, err
			}
			return net.ListenPacket("udp4", "127.0.0.1:0")
		},
		ListenTCP: func(addr netip.AddrPort) (net.Listener, error) {
			if err := s.note("tcp " + addr.String()); err != nil {
				return nil, err
			}
			return net.Listen("tcp", "127.0.0.1:0")
		},
	}
}

func tftpRoot(t *testing.T) string {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func basePlan(t *testing.T, cert *tls.Certificate, root string) Plan {
	return Plan{
		Admin: AdminPlan{Addr: netip.MustParseAddrPort("127.0.0.1:1"), Cert: cert, Field: "admin"},
		DHCP: []DHCPPlan{{
			Interface: "lab0", Bind: netip.MustParseAddrPort("0.0.0.0:67"), Field: "interfaces[0].dhcp",
			Config: dhcp.Config{
				Interface: "lab0", ServerIP: net.IPv4(10, 77, 0, 1).To4(), Netmask: net.CIDRMask(24, 32),
				PoolStart: net.IPv4(10, 77, 0, 100), PoolEnd: net.IPv4(10, 77, 0, 199),
			},
		}},
		TFTP: []TFTPPlan{{
			Interface: "lab0", Bind: netip.MustParseAddrPort("10.77.0.1:69"), Field: "interfaces[0].services.tftp",
			Config: tftp.Config{Root: root},
		}},
	}
}

func start(t *testing.T, s *sockets, plan Plan) *Manager {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })
	m := New(h, s.options())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); <-m.Done() })
	failures, err := m.Start(ctx, plan)
	if err != nil || len(failures) > 0 {
		t.Fatalf("Start: %v %v", failures, err)
	}
	return m
}

func get(t *testing.T, addr net.Addr) error {
	t.Helper()
	c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := c.Get("https://" + addr.String() + "/")
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func TestApplyReusesUnchangedSockets(t *testing.T) {
	s := newSockets()
	cert := testCert(t)
	plan := basePlan(t, cert, tftpRoot(t))
	m := start(t, s, plan)

	// Change everything except addresses: no socket is reopened.
	plan.DHCP[0].Config.LeaseTime = 10 * time.Minute
	plan.TFTP[0].Config.MaxWindowSize = 4
	committed := false
	if err := m.Apply(plan, func() error { committed = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !committed {
		t.Fatal("commit not called")
	}
	for _, k := range []string{"tcp 127.0.0.1:1", "dhcp lab0 0.0.0.0:67", "udp 10.77.0.1:69"} {
		if n := s.count(k); n != 1 {
			t.Errorf("%s opened %d times, want 1", k, n)
		}
	}
	st := m.Status()
	if len(st["dhcp"].Running) != 1 || len(st["tftp"].Running) != 1 {
		t.Errorf("status %+v", st)
	}
}

func TestApplyIsAtomicOnBindFailure(t *testing.T) {
	s := newSockets()
	plan := basePlan(t, testCert(t), tftpRoot(t))
	m := start(t, s, plan)

	next := plan
	next.TFTP = append([]TFTPPlan{}, plan.TFTP...)
	next.TFTP = append(next.TFTP, TFTPPlan{Interface: "lab1", Bind: netip.MustParseAddrPort("10.88.0.1:69"),
		Field: "interfaces[1].services.tftp", Config: tftp.Config{Root: plan.TFTP[0].Config.Root}})
	next.DHCP = nil // would stop DHCP if applied
	s.fail["udp 10.88.0.1:69"] = true

	committed := false
	err := m.Apply(next, func() error { committed = true; return nil })
	var be *BindError
	if !errors.As(err, &be) || len(be.Failures) != 1 || be.Failures[0].Field != "interfaces[1].services.tftp" {
		t.Fatalf("Apply = %v, want one BindError for interfaces[1]", err)
	}
	if committed {
		t.Error("commit ran despite the bind failure")
	}
	if st := m.Status(); len(st["dhcp"].Running) != 1 {
		t.Errorf("DHCP stopped although the apply failed: %+v", st["dhcp"])
	}
}

func TestApplyKeepsStateWhenCommitFails(t *testing.T) {
	s := newSockets()
	plan := basePlan(t, testCert(t), tftpRoot(t))
	m := start(t, s, plan)

	next := plan
	next.TFTP = nil
	if err := m.Apply(next, func() error { return errors.New("store says no") }); err == nil {
		t.Fatal("Apply succeeded with a failing commit")
	}
	if st := m.Status(); len(st["tftp"].Running) != 1 {
		t.Errorf("TFTP stopped although the commit failed: %+v", st["tftp"])
	}
}

func TestStartReportsFailuresButRuns(t *testing.T) {
	s := newSockets()
	s.fail["udp 10.77.0.1:69"] = true
	h := http.NotFoundHandler()
	m := New(h, s.options())
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-m.Done() }()

	failures, err := m.Start(ctx, basePlan(t, testCert(t), tftpRoot(t)))
	if err != nil {
		t.Fatalf("Start returned an error for a TFTP failure: %v", err)
	}
	if len(failures) != 1 || failures[0].Service != "tftp" {
		t.Fatalf("failures %+v", failures)
	}
	st := m.Status()
	if len(st["tftp"].Failures) != 1 || len(st["dhcp"].Running) != 1 {
		t.Errorf("status %+v", st)
	}
}

func TestStartFailsWithoutAdmin(t *testing.T) {
	s := newSockets()
	s.fail["tcp 127.0.0.1:1"] = true
	m := New(http.NotFoundHandler(), s.options())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := m.Start(ctx, basePlan(t, testCert(t), tftpRoot(t))); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("Start = %v, want an admin error", err)
	}
}

func TestAdminMoveKeepsOldListenerForGrace(t *testing.T) {
	s := newSockets()
	plan := basePlan(t, testCert(t), tftpRoot(t))
	m := start(t, s, plan)
	old := m.AdminAddr()
	if err := get(t, old); err != nil {
		t.Fatal(err)
	}

	plan.Admin.Addr = netip.MustParseAddrPort("127.0.0.1:2")
	if err := m.Apply(plan, nil); err != nil {
		t.Fatal(err)
	}
	if err := get(t, m.AdminAddr()); err != nil {
		t.Fatalf("new admin listener: %v", err)
	}
	if err := get(t, old); err != nil {
		t.Fatalf("old admin listener closed before the grace period: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for get(t, old) == nil {
		if time.Now().After(deadline) {
			t.Fatal("old admin listener still open after the grace period")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
