// Package server implements the admin and agent API defined in api/openapi.yaml.
package server

import (
	"context"
	"time"

	"github.com/your-org/netdeploy/internal/api"
)

// Server implements api.StrictServerInterface.
//
// Scaffolding note: the embedded interface is nil, so every operation that
// has no method on *Server yet panics when called. Recover turns that into a
// 500 and logs the operation. Implement operations one at a time by adding
// methods below; the compiler checks their signatures against the spec.
type Server struct {
	api.StrictServerInterface

	version string
	started time.Time
}

// New returns a Server.
func New(version string) *Server {
	return &Server{version: version, started: time.Now()}
}

// GetHealth reports per-service health. Network services are listed as
// disabled until their phases land, so the UI can already render them.
func (s *Server) GetHealth(_ context.Context, _ api.GetHealthRequestObject) (api.GetHealthResponseObject, error) {
	notYet := "not implemented yet"
	return api.GetHealth200JSONResponse{
		Status: api.HealthStatusOk,
		Services: map[string]api.ServiceHealth{
			"admin":   {Status: api.ServiceHealthStatusOk},
			"dhcp":    {Status: api.ServiceHealthStatusDisabled, Detail: &notYet},
			"tftp":    {Status: api.ServiceHealthStatusDisabled, Detail: &notYet},
			"content": {Status: api.ServiceHealthStatusDisabled, Detail: &notYet},
		},
	}, nil
}
