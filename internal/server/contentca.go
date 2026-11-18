package server

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
)

// GetContentCaCertificate implements GET /settings/content-ca (ADR 0028):
// PEM, or DER with Accept: application/pkix-cert (for firmware enrolment).
func (s *Server) GetContentCaCertificate(ctx context.Context, _ api.GetContentCaCertificateRequestObject) (api.GetContentCaCertificateResponseObject, error) {
	var ca *x509.Certificate
	if s.ca != nil {
		ca = s.ca.CA()
	}
	if ca == nil {
		return api.GetContentCaCertificate404ApplicationProblemPlusJSONResponse(newProblem(http.StatusNotFound,
			"content_https_not_available", "Content HTTPS not available",
			"There is no content CA yet: enable HTTPS on a deployment interface in Settings to create it.", nil)), nil
	}
	if acc, _ := ctx.Value(ctxAccept).(string); strings.Contains(acc, "application/pkix-cert") {
		return api.GetContentCaCertificate200ApplicationpkixCertResponse{Body: bytes.NewReader(ca.Raw), ContentLength: int64(len(ca.Raw))}, nil
	}
	pem := s.ca.PEM()
	return api.GetContentCaCertificate200ApplicationxPemFileResponse{Body: bytes.NewReader(pem), ContentLength: int64(len(pem))}, nil
}

// contentCAWarning: content_ca_expiring within a year of the CA's expiry,
// and after it.
func contentCAWarning(ca *x509.Certificate, now time.Time) *warning {
	if ca == nil {
		return nil
	}
	day := ca.NotAfter.Format("2 January 2006")
	const advice = "Replacing it means rebuilding boot images with https:// server URLs and enrolling the new CA in firmware used for HTTPS boot."
	switch left := ca.NotAfter.Sub(now); {
	case left <= 0:
		return &warning{"content_ca_expiring", "The content CA expired on " + day + ": agents can't reach this server over HTTPS. " + advice}
	case left < 365*24*time.Hour:
		return &warning{"content_ca_expiring", fmt.Sprintf("The content CA expires in %d days (%s). %s", (left+24*time.Hour-1)/(24*time.Hour), day, advice)}
	}
	return nil
}
