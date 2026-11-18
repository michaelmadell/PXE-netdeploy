package settings

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
)

// AdminCertificate describes the certificate the admin listener serves:
// the stored custom pair when it is valid, else the generated one (as
// plan chooses). Nil when there is neither (tests).
func (s *Service) AdminCertificate() *api.CertificateInfo {
	cfg, _ := s.Current()
	return s.adminCertificate(cfg)
}

func (s *Service) adminCertificate(cfg Config) *api.CertificateInfo {
	if c := cfg.Admin.Certificate; c.Custom() {
		if pair, err := tls.X509KeyPair([]byte(c.CertificatePEM), []byte(c.PrivateKeyPEM)); err == nil {
			return certificateInfo(api.CertificateInfoSourceCustom, &pair)
		}
	}
	return certificateInfo(api.CertificateInfoSourceGenerated, s.opts.GeneratedAdminCert)
}

func certificateInfo(source api.CertificateInfoSource, pair *tls.Certificate) *api.CertificateInfo {
	if pair == nil || len(pair.Certificate) == 0 {
		return nil
	}
	leaf := pair.Leaf
	if leaf == nil {
		var err error
		if leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return nil
		}
	}
	sum := sha256.Sum256(leaf.Raw)
	issuer := leaf.Issuer.String()
	info := &api.CertificateInfo{
		Source: source, Subject: leaf.Subject.String(), Issuer: &issuer,
		DnsNames: append([]string{}, leaf.DNSNames...), IpAddresses: []string{},
		NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(), Sha256: hex.EncodeToString(sum[:]),
	}
	for _, ip := range leaf.IPAddresses {
		info.IpAddresses = append(info.IpAddresses, ip.String())
	}
	return info
}
