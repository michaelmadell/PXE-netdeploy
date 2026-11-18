// Package contentca is the internal CA for the HTTPS content listener
// (ADR 0028): one CA per installation, its key sealed with the secrets data
// key, and a server certificate issued from it in memory for the names the
// content listeners answer to.
package contentca

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/secrets"
)

// Files in the data directory. Both are in backups (internal/backup).
const (
	CertFile = "content-ca.pem"
	KeyFile  = "content-ca-key.enc"
)

// Sizes and lifetimes (ADR 0028). RSA because firmware TLS for HTTPS boot
// often handles nothing else.
const (
	caBits       = 3072
	leafBits     = 2048
	caLifetime   = 10 * 365 * 24 * time.Hour
	leafLifetime = 365 * 24 * time.Hour
	leafRenew    = 30 * 24 * time.Hour // reissue when less is left
	namesTTL     = 10 * time.Second    // how long Names' answer is reused
)

// sealed binds the key's ciphertext to its purpose (secrets.Ring's
// associated data).
const sealID, sealKind, sealVersion = "content-ca", "content-ca", 1

// ErrNoCA is returned by operations that need a CA before Ensure made one.
var ErrNoCA = errors.New("contentca: no content CA yet")

// Names are what the server certificate must cover.
type Names struct {
	IPs []netip.Addr
	DNS []string
}

func (n Names) normalize() Names {
	out := Names{}
	for _, a := range n.IPs {
		if a.IsValid() {
			out.IPs = append(out.IPs, a.Unmap())
		}
	}
	for _, d := range n.DNS {
		if d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), ".")); d != "" {
			out.DNS = append(out.DNS, d)
		}
	}
	slices.SortFunc(out.IPs, func(a, b netip.Addr) int { return a.Compare(b) })
	out.IPs = slices.Compact(out.IPs)
	slices.Sort(out.DNS)
	out.DNS = slices.Compact(out.DNS)
	return out
}

func (n Names) equal(o Names) bool { return slices.Equal(n.IPs, o.IPs) && slices.Equal(n.DNS, o.DNS) }

// Options configure an Authority.
type Options struct {
	DataDir string
	// Ring returns the secrets data key, creating it when create is set
	// (secrets.Keys.Ring).
	Ring func(create bool) (*secrets.Ring, error)
	// Names returns what the server certificate must cover now. Nil: none
	// (only useful in tests).
	Names  func() Names
	Logger *slog.Logger
	Now    func() time.Time // nil: time.Now
}

// Authority holds the CA and the current server certificate.
type Authority struct {
	opts Options
	log  *slog.Logger

	mu      sync.Mutex
	ca      *x509.Certificate
	caKey   *rsa.PrivateKey
	caPEM   []byte
	lost    bool // files exist but the key couldn't be opened
	leaf    *tls.Certificate
	names   Names
	namesAt time.Time
}

// New returns an Authority; call Load before using it.
func New(opts Options) *Authority {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Authority{opts: opts, log: opts.Logger.With("svc", "content-ca")}
}

// Load reads the CA from the data directory if there is one. A CA whose key
// can't be opened (a restore without the secrets key export) is treated as
// missing: Ensure then makes a new one. Only unexpected I/O errors are
// returned.
func (a *Authority) Load() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	certPEM, err := os.ReadFile(filepath.Join(a.opts.DataDir, CertFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("contentca: %w", err)
	}
	sealed, err := os.ReadFile(filepath.Join(a.opts.DataDir, KeyFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("contentca: %w", err)
	}
	ca, key, err := a.open(certPEM, sealed)
	if err != nil {
		a.lost = true
		a.log.Error("the content CA can't be used; a new one will be made when HTTPS is enabled, and boot images with https:// server URLs will need rebuilding",
			"err", err)
		return nil
	}
	a.ca, a.caKey, a.caPEM = ca, key, certPEM
	return nil
}

func (a *Authority) open(certPEM, sealed []byte) (*x509.Certificate, *rsa.PrivateKey, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("%s holds no certificate", CertFile)
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", CertFile, err)
	}
	if len(sealed) == 0 {
		return nil, nil, fmt.Errorf("%s is missing", KeyFile)
	}
	ring, err := a.opts.Ring(false)
	if err != nil {
		return nil, nil, fmt.Errorf("secrets key: %w", err)
	}
	der, err := ring.Open(sealID, sealKind, sealVersion, sealed)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: not sealed with this installation's secrets key (restored without `netdeployd key import`?)", KeyFile)
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", KeyFile, err)
	}
	if !key.PublicKey.Equal(ca.PublicKey) {
		return nil, nil, fmt.Errorf("%s doesn't match %s", KeyFile, CertFile)
	}
	return ca, key, nil
}

// Ensure makes the CA if there is none (ADR 0028: the first time an
// interface enables HTTPS). It creates the secrets key if needed.
func (a *Authority) Ensure() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ca != nil {
		return nil
	}
	ring, err := a.opts.Ring(true)
	if err != nil {
		return fmt.Errorf("contentca: secrets key: %w", err)
	}
	key, err := rsa.GenerateKey(rand.Reader, caBits)
	if err != nil {
		return fmt.Errorf("contentca: %w", err)
	}
	now := a.opts.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "netdeploy content CA"},
		NotBefore:             now.Add(-time.Hour).UTC().Truncate(time.Second),
		NotAfter:              now.Add(caLifetime).UTC().Truncate(time.Second),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("contentca: %w", err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return fmt.Errorf("contentca: %w", err)
	}
	sealed, err := ring.Seal(sealID, sealKind, sealVersion, x509.MarshalPKCS1PrivateKey(key))
	if err != nil {
		return fmt.Errorf("contentca: seal key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	// Key first: a certificate without its key is reported as lost, never
	// used.
	if err := writeFile(filepath.Join(a.opts.DataDir, KeyFile), sealed, 0o600); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(a.opts.DataDir, CertFile), certPEM, 0o644); err != nil {
		return err
	}
	a.ca, a.caKey, a.caPEM, a.leaf, a.lost = ca, key, certPEM, nil, false
	a.log.Info("made a new content CA", "sha256", fingerprint(ca), "notAfter", ca.NotAfter)
	return nil
}

// CA returns the CA certificate, or nil if there is none.
func (a *Authority) CA() *x509.Certificate {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ca
}

// PEM returns the CA certificate as PEM, or nil.
func (a *Authority) PEM() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return bytes.Clone(a.caPEM)
}

// Fingerprint is the SHA-256 of the CA certificate (DER), lowercase hex;
// "" without a CA. A boot image build input (ADR 0028).
func (a *Authority) Fingerprint() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ca == nil {
		return ""
	}
	return fingerprint(a.ca)
}

// Lost reports whether a stored CA couldn't be opened at Load.
func (a *Authority) Lost() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lost
}

// GetCertificate serves the current server certificate (tls.Config).
func (a *Authority) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return a.Certificate()
}

// Certificate returns the server certificate, issuing a new one when the
// names it must cover changed or it is close to expiry.
func (a *Authority) Certificate() (*tls.Certificate, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ca == nil {
		return nil, ErrNoCA
	}
	now := a.opts.Now()
	if a.leaf == nil || now.Sub(a.namesAt) >= namesTTL {
		var n Names
		if a.opts.Names != nil {
			n = a.opts.Names()
		}
		n, a.namesAt = n.normalize(), now
		if a.leaf != nil && !n.equal(a.names) {
			a.log.Info("server names changed; issuing a new content certificate", "ips", n.IPs, "dns", n.DNS)
			a.leaf = nil
		}
		a.names = n
	}
	if a.leaf != nil && a.leaf.Leaf.NotAfter.Sub(now) > leafRenew {
		return a.leaf, nil
	}
	leaf, err := a.issue(a.names, now)
	if err != nil {
		return nil, err
	}
	a.leaf = leaf
	return leaf, nil
}

// issue makes a server certificate for n. Its notBefore is the CA's, so an
// agent whose clock is behind still accepts it (ADR 0028).
func (a *Authority) issue(n Names, now time.Time) (*tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, leafBits)
	if err != nil {
		return nil, fmt.Errorf("contentca: %w", err)
	}
	notAfter := now.Add(leafLifetime).UTC().Truncate(time.Second)
	if notAfter.After(a.ca.NotAfter) {
		notAfter = a.ca.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "netdeploy content"},
		NotBefore:    a.ca.NotBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     n.DNS,
	}
	for _, ip := range n.IPs {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.IP(ip.AsSlice()))
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.ca, &key.PublicKey, a.caKey)
	if err != nil {
		return nil, fmt.Errorf("contentca: issue: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("contentca: issue: %w", err)
	}
	return &tls.Certificate{Certificate: [][]byte{der, a.ca.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

func fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err) // crypto/rand doesn't fail
	}
	return n.Add(n, big.NewInt(1))
}

// writeFile replaces path atomically.
func writeFile(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	os.Remove(tmp)
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return fmt.Errorf("contentca: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("contentca: %w", err)
	}
	return nil
}
