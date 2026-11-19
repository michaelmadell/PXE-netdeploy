// Package certs manages the admin listener's TLS certificate.
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	certFile = "admin-cert.pem"
	keyFile  = "admin-key.pem"
)

// generatedCN marks a certificate this package made, so only those are renewed.
const generatedCN = "netdeploy admin"

// renewBefore: a generated certificate this close to expiry is replaced at
// start (ADR 0031); health warns from the same point (admin_certificate_expiring).
const renewBefore = 30 * 24 * time.Hour

// LoadOrCreateAdminTLS loads the admin certificate from dataDir, generating a
// self-signed one (localhost, 127.0.0.1, ::1, this host's name) on first run,
// and a new one when the generated one is within 30 days of expiry (the old
// pair is kept as *.old). Replace the files to use your own certificate;
// that one is never renewed.
func LoadOrCreateAdminTLS(dataDir string) (*tls.Config, error) {
	return loadOrCreate(dataDir, time.Now())
}

func loadOrCreate(dataDir string, now time.Time) (*tls.Config, error) {
	certPath := filepath.Join(dataDir, certFile)
	keyPath := filepath.Join(dataDir, keyFile)

	if _, err := os.Stat(certPath); errors.Is(err, os.ErrNotExist) {
		if err := generate(certPath, keyPath, now); err != nil {
			return nil, fmt.Errorf("generate admin certificate: %w", err)
		}
		slog.Info("generated self-signed admin certificate", "path", certPath)
	} else if c := readCert(certPath); c != nil && c.Subject.CommonName == generatedCN && c.NotAfter.Sub(now) < renewBefore {
		for _, p := range []string{certPath, keyPath} {
			if err := os.Rename(p, p+".old"); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("renew admin certificate: %w", err)
			}
		}
		if err := generate(certPath, keyPath, now); err != nil {
			return nil, fmt.Errorf("renew admin certificate: %w", err)
		}
		slog.Info("renewed the generated admin certificate", "path", certPath, "old_expiry", c.NotAfter)
	}

	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load admin certificate: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, nil
}

// readCert parses the first certificate in a PEM file; nil if it can't.
func readCert(path string) *x509.Certificate {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		return nil
	}
	return c
}

func generate(certPath, keyPath string, now time.Time) error {
	return generateNamed(certPath, keyPath, now, generatedCN)
}

func generateNamed(certPath, keyPath string, now time.Time, cn string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}

	dnsNames := []string{"localhost"}
	if host, err := os.Hostname(); err == nil && host != "" {
		dnsNames = append(dnsNames, host)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(2, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}

	if err := writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return err
	}
	return writePEM(certPath, "CERTIFICATE", der, 0o644)
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
