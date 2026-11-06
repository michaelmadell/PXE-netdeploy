package server

import (
	"context"
	"net/http"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
)

// GetContentCaCertificate implements GET /settings/content-ca. Content HTTPS
// (and its generated CA) is planned for Phase 6.
func (s *Server) GetContentCaCertificate(context.Context, api.GetContentCaCertificateRequestObject) (api.GetContentCaCertificateResponseObject, error) {
	return api.GetContentCaCertificate404ApplicationProblemPlusJSONResponse(newProblem(http.StatusNotFound,
		"content_https_not_available", "Content HTTPS not available", "Content HTTPS is planned for Phase 6.", nil)), nil
}
