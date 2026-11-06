// Package server implements the admin and agent API defined in api/openapi.yaml.
package server

import (
	"context"
	"strings"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/netsvc"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

// Server implements api.StrictServerInterface.
//
// Scaffolding note: the embedded interface is nil, so every operation that
// has no method on *Server yet panics when called. Recover turns that into a
// 500 and logs the operation. Implement operations one at a time by adding
// methods below; the compiler checks their signatures against the spec.
type Server struct {
	api.StrictServerInterface

	version  string
	started  time.Time
	settings *settings.Service
	network  *netsvc.Manager
	store    *store.Store
}

// New returns a Server. Dependencies may be nil in tests of operations that
// don't use them.
func New(version string, s *settings.Service, network *netsvc.Manager, st *store.Store) *Server {
	return &Server{version: version, started: time.Now(), settings: s, network: network, store: st}
}

// GetHealth reports per-service health from the running listeners, plus
// configuration warnings. Overall status is "degraded" if any service is in
// error.
func (s *Server) GetHealth(_ context.Context, _ api.GetHealthRequestObject) (api.GetHealthResponseObject, error) {
	services := map[string]api.ServiceHealth{
		"content": disabled("not implemented yet (Phase 2 step 5)"),
	}
	if s.network != nil {
		for name, st := range s.network.Status() {
			services[name] = serviceHealth(st)
		}
	}
	status := api.HealthStatusOk
	for _, h := range services {
		if h.Status == api.ServiceHealthStatusError {
			status = api.HealthStatusDegraded
		}
	}
	out := api.GetHealth200JSONResponse{Status: status, Services: services}
	if w := s.warnings(); len(w) > 0 {
		out.Warnings = &w
	}
	return out, nil
}

func serviceHealth(st netsvc.Status) api.ServiceHealth {
	if len(st.Failures) > 0 {
		fs := make([]api.BindFailure, len(st.Failures))
		msgs := make([]string, len(st.Failures))
		for i, f := range st.Failures {
			fs[i] = api.BindFailure{Interface: f.Interface, Message: f.Message, Port: f.Port, Protocol: api.BindFailureProtocol(f.Protocol)}
			msgs[i] = f.Message
		}
		detail := strings.Join(msgs, "; ")
		return api.ServiceHealth{Status: api.ServiceHealthStatusError, Detail: &detail, BindFailures: &fs}
	}
	if len(st.Running) > 0 {
		detail := strings.Join(st.Running, ", ")
		if st.Note != "" {
			detail += " (" + st.Note + ")"
		}
		return api.ServiceHealth{Status: api.ServiceHealthStatusOk, Detail: &detail}
	}
	if st.Note != "" {
		return disabled(st.Note)
	}
	return disabled("not configured on any interface")
}

func disabled(detail string) api.ServiceHealth {
	return api.ServiceHealth{Status: api.ServiceHealthStatusDisabled, Detail: &detail}
}

type warning = struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *Server) warnings() []warning {
	if s.settings == nil {
		return nil
	}
	cfg, _ := s.settings.Current()
	var w []warning
	if s.settings.AdminOverridden() {
		w = append(w, warning{"admin_address_overridden", "The -admin-addr flag replaces the stored admin address; remove it once settings are fixed."})
	}
	if !cfg.BootPrompt.RequirePassword {
		w = append(w, warning{"boot_prompt_password_disabled", "Debug mode: the boot prompt needs no password, so anyone at a network-booted machine can re-image it."})
	} else if cfg.BootPrompt.PasswordHash == "" {
		w = append(w, warning{"boot_prompt_no_password", "No boot prompt password is set: only deployments scheduled through the API can run."})
	}
	if len(cfg.Interfaces) > 0 && cfg.BootPrompt.DefaultProfileID == "" {
		w = append(w, warning{"no_default_profile", "No default profile: clients without a profile of their own are not answered."})
	}
	return w
}
