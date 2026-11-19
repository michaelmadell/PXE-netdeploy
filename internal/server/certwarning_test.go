package server

import (
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
)

func TestCertificateWarning(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	cert := func(days int, src api.CertificateInfoSource) *api.CertificateInfo {
		return &api.CertificateInfo{Source: src, NotAfter: now.Add(time.Duration(days) * 24 * time.Hour)}
	}
	if w := certificateWarning(cert(31, api.CertificateInfoSourceCustom), now); w != nil {
		t.Errorf("31 days: %+v", w)
	}
	if w := certificateWarning(cert(10, api.CertificateInfoSourceCustom), now); w == nil || !strings.Contains(w.Message, "in 10 days") {
		t.Errorf("10 days: %+v", w)
	}
	if w := certificateWarning(cert(-1, api.CertificateInfoSourceGenerated), now); w == nil ||
		!strings.Contains(w.Message, "expired") || !strings.Contains(w.Message, "Restart netdeployd") {
		t.Errorf("expired: %+v", w)
	}
	if certificateWarning(nil, now) != nil {
		t.Error("nil")
	}
}
