// Package server implements the admin and agent API defined in api/openapi.yaml.
package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/backup"
	"github.com/michaelmadell/pxe-netdeploy/internal/bootimage"
	"github.com/michaelmadell/pxe-netdeploy/internal/cas"
	"github.com/michaelmadell/pxe-netdeploy/internal/events"
	"github.com/michaelmadell/pxe-netdeploy/internal/jobs"
	"github.com/michaelmadell/pxe-netdeploy/internal/netsvc"
	"github.com/michaelmadell/pxe-netdeploy/internal/secrets"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
	"github.com/michaelmadell/pxe-netdeploy/internal/uploads"
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
	events   *events.Bus
	content  *cas.Store
	jobs     *jobs.Runner
	uploads  *uploads.Manager
	vault    *secrets.Vault
	backup   *backup.Service
	boot     *bootimage.Service
	caps     capsCache
}

// Deps are the Server's dependencies. Any may be nil in tests of operations
// that don't use them.
type Deps struct {
	Settings *settings.Service
	Network  *netsvc.Manager
	Store    *store.Store
	Events   *events.Bus
	Content  *cas.Store
	Jobs     *jobs.Runner
	Uploads  *uploads.Manager
	Vault    *secrets.Vault
	Backup   *backup.Service
	Boot     *bootimage.Service
}

// New returns a Server.
func New(version string, d Deps) *Server {
	return &Server{version: version, started: time.Now(), settings: d.Settings, network: d.Network,
		store: d.Store, events: d.Events, content: d.Content, jobs: d.Jobs, uploads: d.Uploads, vault: d.Vault, backup: d.Backup, boot: d.Boot}
}

// GetHealth reports per-service health from the running listeners, plus
// configuration warnings. Overall status is "degraded" if any service is in
// error.
func (s *Server) GetHealth(ctx context.Context, _ api.GetHealthRequestObject) (api.GetHealthResponseObject, error) {
	return api.GetHealth200JSONResponse(s.health(ctx)), nil
}

// health builds the Health object (GET /system/health, health.updated).
func (s *Server) health(ctx context.Context) api.Health {
	services := map[string]api.ServiceHealth{}
	if s.store != nil {
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		err := s.store.Ping(pctx)
		cancel()
		if err != nil {
			detail := err.Error()
			services["store"] = api.ServiceHealth{Status: api.ServiceHealthStatusError, Detail: &detail}
		} else {
			services["store"] = api.ServiceHealth{Status: api.ServiceHealthStatusOk}
		}
	}
	var storage *api.StorageHealth
	if s.content != nil {
		if ok, detail := s.content.Health(); ok {
			services["storage"] = api.ServiceHealth{Status: api.ServiceHealthStatusOk}
		} else {
			services["storage"] = api.ServiceHealth{Status: api.ServiceHealthStatusError, Detail: &detail}
		}
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		sp, err := s.content.Space(pctx)
		cancel()
		if err == nil {
			storage = &api.StorageHealth{TotalBytes: sp.Total, FreeBytes: sp.Free, ContentBytes: sp.Content, ReserveBytes: sp.Reserve}
			if held, _, err := s.store.UploadBytes(ctx, ""); err == nil {
				storage.UploadBytes = held
			}
		}
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
	out := api.Health{Status: status, Services: services, Storage: storage}
	w := s.warnings()
	if storage != nil && storage.FreeBytes < 2*storage.ReserveBytes {
		w = append(w, warning{"storage_low", fmt.Sprintf("The content store has %s free; imports stop at %s.",
			humanBytes(storage.FreeBytes), humanBytes(storage.ReserveBytes))})
	}
	w = append(w, s.secretsWarnings(ctx)...)
	if bh, bw := s.backupHealth(ctx); bh != nil {
		out.Backup = bh
		w = append(w, bw...)
	}
	if len(w) > 0 {
		out.Warnings = &w
	}
	return out
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

// humanBytes renders n in binary units, e.g. "4.8 GiB".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
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
