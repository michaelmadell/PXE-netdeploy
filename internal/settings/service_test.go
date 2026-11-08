package settings

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/boot"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/netsvc"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

func generatedCert(t *testing.T) *tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "t"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// testService runs a Service on a real store, with every socket a loopback
// port 0 so nothing privileged is needed.
func testService(t *testing.T, dir string) *Service {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(ctx, dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	net0 := func() (net.PacketConn, error) { return net.ListenPacket("udp4", "127.0.0.1:0") }
	mgr := netsvc.New(http.NotFoundHandler(), netsvc.Options{
		Logger:     quiet,
		ListenDHCP: func(string, netip.AddrPort) (net.PacketConn, error) { return net0() },
		ListenUDP:  func(netip.AddrPort) (net.PacketConn, error) { return net0() },
		ListenTCP:  func(netip.AddrPort) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
	})
	svc := NewService(Options{Store: st, Network: mgr, Host: lab, Logger: quiet,
		GeneratedAdminCert: generatedCert(t), Dev: Dev{TFTPRoot: t.TempDir(), BootFile: "bootmgfw.efi"}})
	if err := svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); <-mgr.Done(); st.Close() })
	return svc
}

// A document stored before a field existed gets that field's default.
func TestStartFillsFieldsAddedLater(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	b, _ := json.Marshal(Defaults())
	json.Unmarshal(b, &doc)
	delete(doc, "storage") // as stored by Phase 2
	b, _ = json.Marshal(doc)
	if _, err := st.InitSettings(ctx, b); err != nil {
		t.Fatal(err)
	}
	st.Close()

	svc := testService(t, dir)
	if cfg, _ := svc.Current(); cfg.Storage.ReserveBytes != DefaultReserveBytes || cfg.Storage.UploadExpiryHours != 24 {
		t.Errorf("storage = %+v, want the defaults", cfg.Storage)
	}
}

func TestServiceReplace(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	svc := testService(t, dir)

	cur, tag, err := svc.Get()
	if err != nil || tag != `"1"` || len(cur.Interfaces) != 0 {
		t.Fatalf("first run: %q %v %+v", tag, err, cur.Interfaces)
	}

	in := apiSettings(t, `{"admin":{"bindAddress":"127.0.0.1","port":8443},
		"interfaces":[{"name":"lab0","dhcp":{"mode":"full","pool":{"start":"10.77.0.100","end":"10.77.0.199","subnetMask":"255.255.255.0"}},"services":{"http":false}}],
		"bootPrompt":{"password":"correct horse"}}`)
	if _, _, err := svc.Replace(ctx, in, `"7"`); !errors.Is(err, ErrPreconditionFailed) {
		t.Fatalf("stale If-Match = %v", err)
	}
	bad := apiSettings(t, `{"admin":{"bindAddress":"127.0.0.1","port":8443},"interfaces":[{"name":"eth9","dhcp":{"mode":"off"},"services":{}}]}`)
	var verr *ValidationError
	if _, _, err := svc.Replace(ctx, bad, `"1"`); !errors.As(err, &verr) {
		t.Fatalf("unknown interface = %v, want ValidationError", err)
	}

	out, tag, err := svc.Replace(ctx, in, `"1"`)
	if err != nil || tag != `"2"` || !*out.BootPrompt.HasPassword || out.BootPrompt.Password != nil {
		t.Fatalf("Replace = %q %v", tag, err)
	}
	st := svc.opts.Network.Status()
	if len(st["dhcp"].Running) != 1 || len(st["tftp"].Running) != 1 {
		t.Errorf("listeners not running: %+v", st)
	}

	// Persisted: the stored document carries the hash, never the password.
	doc, err := svc.opts.Store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var stored Config
	if err := json.Unmarshal(doc.Doc, &stored); err != nil {
		t.Fatal(err)
	}
	if ok, _ := VerifyPassword(stored.BootPrompt.PasswordHash, "correct horse"); !ok || doc.Version != 2 {
		t.Errorf("stored version %d, password verifies %v", doc.Version, ok)
	}
}

func TestClientRoot(t *testing.T) {
	ctx := context.Background()
	svc := testService(t, t.TempDir())
	resolve := svc.clientRoot("lab0")

	// A DHCP boot session wins.
	sessIP := netip.MustParseAddr("10.77.0.140")
	a := cas.DirFS("/content/a")
	svc.Sessions().Record(boot.Session{Interface: "lab0", IP: sessIP, MAC: "00:15:5d:00:00:01", Answer: boot.Answer{Root: a}})
	if root, attrs := resolve(sessIP); root != a || !hasAttr(attrs, "boot_session", "dhcp") {
		t.Errorf("session: %v %v", root, attrs)
	}
	// Same IP on another interface: no session there.
	if root, _ := svc.clientRoot("lab1")(sessIP); root != nil {
		t.Errorf("session leaked across interfaces: %v", root)
	}

	// No session, but a lease (full mode after a restart): the client's content.
	leaseIP := netip.MustParseAddr("10.77.0.150")
	if err := svc.opts.Store.PutLease(ctx, store.Lease{Interface: "lab0", IP: leaseIP, MAC: "00:15:5d:00:00:02", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if root, attrs := resolve(leaseIP); root != cas.DirFS(svc.opts.Dev.TFTPRoot) || !hasAttr(attrs, "boot_session", "lease") {
		t.Errorf("lease: %v %v", root, attrs)
	}

	// Neither (DHCP off): the default content.
	if root, attrs := resolve(netip.MustParseAddr("10.77.0.160")); root != nil || !hasAttr(attrs, "boot_session", "none") {
		t.Errorf("unknown: %v %v", root, attrs)
	}
}

func hasAttr(attrs []any, k, v string) bool {
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i] == k && attrs[i+1] == v {
			return true
		}
	}
	return false
}
