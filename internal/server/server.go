// Package server implements the admin and agent API defined in api/openapi.yaml.
package server

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
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

	mu       sync.Mutex
	services map[string]api.ServiceHealth
}

// New returns a Server. Network services start out disabled; the code that
// starts them reports their state with SetService.
func New(version string) *Server {
	notYet := "not implemented yet"
	notEnabled := "not enabled"
	return &Server{
		version: version,
		started: time.Now(),
		services: map[string]api.ServiceHealth{
			"admin":   {Status: api.ServiceHealthStatusOk},
			"dhcp":    {Status: api.ServiceHealthStatusDisabled, Detail: &notEnabled},
			"tftp":    {Status: api.ServiceHealthStatusDisabled, Detail: &notEnabled},
			"content": {Status: api.ServiceHealthStatusDisabled, Detail: &notYet},
		},
	}
}

// SetService records the state of a network service for GetHealth.
func (s *Server) SetService(name string, status api.ServiceHealthStatus, detail string) {
	h := api.ServiceHealth{Status: status}
	if detail != "" {
		h.Detail = &detail
	}
	s.mu.Lock()
	s.services[name] = h
	s.mu.Unlock()
}

// GetHealth reports per-service health. Overall status is "ok" unless a
// service is in error.
func (s *Server) GetHealth(_ context.Context, _ api.GetHealthRequestObject) (api.GetHealthResponseObject, error) {
	s.mu.Lock()
	services := maps.Clone(s.services)
	s.mu.Unlock()
	status := api.HealthStatusOk
	for _, h := range services {
		if h.Status == api.ServiceHealthStatusError {
			status = api.HealthStatusDegraded
		}
	}
	return api.GetHealth200JSONResponse{Status: status, Services: services}, nil
}
