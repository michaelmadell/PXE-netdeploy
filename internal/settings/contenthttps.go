package settings

import (
	"os"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/contentca"
)

// HTTPSOn reports whether any interface enables services.https.
func (c Config) HTTPSOn() bool {
	for _, in := range c.Interfaces {
		if in.Services.HTTPS {
			return true
		}
	}
	return false
}

// ensureContentCA makes the content CA the first time an interface enables
// HTTPS (ADR 0028).
func (s *Service) ensureContentCA(cfg Config) error {
	if !cfg.HTTPSOn() || s.opts.ContentCA == nil {
		return nil
	}
	return s.opts.ContentCA.Ensure()
}

// contentCertificates fills contentHttps.ca and .certificate (read-only).
func (s *Service) contentCertificates(cfg Config, out *api.Settings) {
	if s.opts.ContentCA == nil {
		return
	}
	if out.ContentHttps == nil {
		out.ContentHttps = &struct {
			Ca             *api.CertificateInfo `json:"ca"`
			Certificate    *api.CertificateInfo `json:"certificate"`
			CertificatePem *string              `json:"certificatePem"`
			PrivateKeyPem  *string              `json:"privateKeyPem"`
		}{}
	}
	if ca := s.opts.ContentCA.CA(); ca != nil {
		out.ContentHttps.Ca = x509Info(api.CertificateInfoSourceGenerated, ca)
	}
	if cfg.HTTPSOn() {
		if c, err := s.opts.ContentCA.Certificate(); err == nil {
			out.ContentHttps.Certificate = certificateInfo(api.CertificateInfoSourceGenerated, c)
		}
	}
}

// ContentNames are the addresses the HTTPS content certificate must cover
// from settings (ADR 0028): each HTTPS interface's primary address and
// advertisedAddress, and this host's name. Boot images' https:// hosts are
// added by the caller.
func (s *Service) ContentNames() contentca.Names {
	var n contentca.Names
	for _, e := range s.Endpoints(true) {
		n.IPs = append(n.IPs, e.Addr())
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		n.DNS = append(n.DNS, h)
	}
	return n
}
