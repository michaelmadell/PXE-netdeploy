package contentca

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/agent/client"
	"github.com/michaelmadell/pxe-netdeploy/internal/secrets"
)

func newAuthority(t *testing.T, dir string, keys *secrets.Keys, names *Names, now func() time.Time) *Authority {
	t.Helper()
	a := New(Options{DataDir: dir, Ring: keys.Ring, Names: func() Names { return *names }, Now: now})
	if err := a.Load(); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestEnsureLoadAndIssue(t *testing.T) {
	dir := t.TempDir()
	keys := secrets.NewKeys(dir)
	names := Names{IPs: []netip.Addr{netip.MustParseAddr("10.77.0.1")}, DNS: []string{"NetDeploy.Lab."}}
	a := newAuthority(t, dir, keys, &names, nil)
	if a.CA() != nil || a.Fingerprint() != "" {
		t.Fatal("a CA before Ensure")
	}
	if _, err := a.Certificate(); err != ErrNoCA {
		t.Fatalf("Certificate without a CA: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, secrets.FileName)); err == nil {
		t.Fatal("secrets key created before Ensure")
	}
	if err := a.Ensure(); err != nil {
		t.Fatal(err)
	}
	ca := a.CA()
	if !ca.IsCA || ca.PublicKeyAlgorithm != x509.RSA || ca.PublicKey.(interface{ Size() int }).Size()*8 != caBits {
		t.Fatalf("CA: isCA %v, %v", ca.IsCA, ca.PublicKeyAlgorithm)
	}
	if d := ca.NotAfter.Sub(ca.NotBefore); d < 9*365*24*time.Hour {
		t.Errorf("CA lifetime %v", d)
	}
	// The key on disk is sealed, not PEM.
	b, err := os.ReadFile(filepath.Join(dir, KeyFile))
	if err != nil || strings.Contains(string(b), "PRIVATE KEY") {
		t.Fatalf("key file: %v, plain %v", err, strings.Contains(string(b), "PRIVATE KEY"))
	}

	c, err := a.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	leaf := c.Leaf
	if !leaf.NotBefore.Equal(ca.NotBefore) {
		t.Errorf("leaf notBefore %v, CA %v", leaf.NotBefore, ca.NotBefore)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	for _, host := range []string{"10.77.0.1", "netdeploy.lab"} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: host}); err != nil {
			t.Errorf("verify for %s: %v", host, err)
		}
	}
	if err := leaf.VerifyHostname("10.77.0.2"); err == nil {
		t.Error("covers an address it shouldn't")
	}
	if c2, _ := a.Certificate(); c2 != c {
		t.Error("reissued without a change")
	}

	// Reopened: the same CA.
	b2 := newAuthority(t, dir, secrets.NewKeys(dir), &names, nil)
	if b2.Fingerprint() != a.Fingerprint() || b2.Lost() {
		t.Fatalf("reloaded fingerprint %q, want %q (lost %v)", b2.Fingerprint(), a.Fingerprint(), b2.Lost())
	}
	if err := b2.Ensure(); err != nil || b2.Fingerprint() != a.Fingerprint() {
		t.Fatal("Ensure replaced an existing CA")
	}
}

func TestNamesChangeReissues(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	clock := func() time.Time { return now }
	names := Names{IPs: []netip.Addr{netip.MustParseAddr("10.77.0.1")}}
	a := newAuthority(t, dir, secrets.NewKeys(dir), &names, clock)
	if err := a.Ensure(); err != nil {
		t.Fatal(err)
	}
	c1, _ := a.Certificate()
	names.IPs = append(names.IPs, netip.MustParseAddr("192.0.2.10"))
	if c, _ := a.Certificate(); c != c1 {
		t.Error("names re-read before their TTL")
	}
	now = now.Add(namesTTL)
	c2, _ := a.Certificate()
	if c2 == c1 || c2.Leaf.VerifyHostname("192.0.2.10") != nil {
		t.Error("not reissued for the new address")
	}
	// Close to expiry: reissued.
	now = c2.Leaf.NotAfter.Add(-leafRenew)
	if c3, _ := a.Certificate(); c3 == c2 {
		t.Error("not renewed near expiry")
	}
}

func TestRestoreWithoutKey(t *testing.T) {
	dir := t.TempDir()
	names := Names{}
	a := newAuthority(t, dir, secrets.NewKeys(dir), &names, nil)
	if err := a.Ensure(); err != nil {
		t.Fatal(err)
	}
	old := a.Fingerprint()
	// Another installation: the files, but not the secrets key.
	other := t.TempDir()
	for _, f := range []string{CertFile, KeyFile} {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		os.WriteFile(filepath.Join(other, f), b, 0o600)
	}
	b := newAuthority(t, other, secrets.NewKeys(other), &names, nil)
	if !b.Lost() || b.CA() != nil {
		t.Fatalf("lost %v, CA %v", b.Lost(), b.CA() != nil)
	}
	if err := b.Ensure(); err != nil {
		t.Fatal(err)
	}
	if b.Fingerprint() == old || b.Lost() {
		t.Error("no new CA")
	}
}

// TestAgentTrust: the agent's TLS config accepts the server certificate
// with its clock years behind, rejects another CA, and rejects an expired
// certificate.
func TestAgentTrust(t *testing.T) {
	dir := t.TempDir()
	names := Names{IPs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
	a := newAuthority(t, dir, secrets.NewKeys(dir), &names, nil)
	if err := a.Ensure(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	// httptest fills Certificates, which crypto/tls prefers to
	// GetCertificate when there is no SNI (an IP): serve ours directly.
	c, err := a.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{*c}}
	srv.StartTLS()
	defer srv.Close()

	get := func(caPEM []byte, now time.Time) error {
		cfg, err := client.TLSConfig(caPEM, func() time.Time { return now })
		if err != nil {
			return err
		}
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
		resp, err := c.Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	if err := get(a.PEM(), time.Now()); err != nil {
		t.Fatalf("now: %v", err)
	}
	if err := get(a.PEM(), time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Errorf("RTC in 2001: %v", err)
	}
	if err := get(a.PEM(), time.Now().Add(2*leafLifetime)); err == nil {
		t.Error("an expired certificate was accepted")
	}
	otherDir := t.TempDir()
	o := newAuthority(t, otherDir, secrets.NewKeys(otherDir), &names, nil)
	o.Ensure()
	if err := get(o.PEM(), time.Now()); err == nil {
		t.Error("another CA was accepted")
	}
}
