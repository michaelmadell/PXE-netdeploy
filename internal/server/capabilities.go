package server

import (
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/bootimage"
	"github.com/michaelmadell/pxe-netdeploy/internal/drivers/archive"
)

// capsTTL bounds how stale GET /system/capabilities can be: tools are
// probed (which runs them) at most this often, so installing one shows up
// without a restart.
const capsTTL = time.Minute

type capsCache struct {
	mu   sync.Mutex
	at   time.Time
	caps api.Capabilities
}

// GetCapabilities implements GET /system/capabilities.
func (s *Server) GetCapabilities(ctx context.Context, _ api.GetCapabilitiesRequestObject) (api.GetCapabilitiesResponseObject, error) {
	s.caps.mu.Lock()
	defer s.caps.mu.Unlock()
	if s.caps.at.IsZero() || time.Since(s.caps.at) > capsTTL {
		s.caps.caps = probeCapabilities(ctx, s.version, s.boot, "")
		s.caps.at = time.Now()
	}
	return api.GetCapabilities200JSONResponse(s.caps.caps), nil
}

func probeCapabilities(ctx context.Context, version string, boot *bootimage.Service, wimlibPath string) api.Capabilities {
	c := api.Capabilities{Version: version, HostOs: api.CapabilitiesHostOsLinux}
	if runtime.GOOS == "windows" {
		c.HostOs = api.CapabilitiesHostOsWindows
	}
	type builder = struct {
		Available bool            `json:"available"`
		Name      api.BuilderName `json:"name"`
		Path      *string         `json:"path,omitempty"`
		Reason    *string         `json:"reason,omitempty"`
		Version   *string         `json:"version,omitempty"`
	}
	str := func(s string) *string { return &s }
	opt := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	h := bootimage.Detect(ctx, wimlibPath)
	if boot != nil {
		h = boot.Host(ctx)
	}
	for _, t := range []bootimage.ToolStatus{h.DISM, h.Wimlib} {
		c.Builders = append(c.Builders, builder{Name: api.BuilderName(t.Name), Available: t.Available,
			Path: opt(t.Path), Reason: opt(t.Reason), Version: opt(t.Version)})
	}
	elevated := h.Elevated
	c.Elevated = &elevated
	agentVersion, arches := version, []api.Architecture{}
	if boot != nil && boot.Agents() != nil {
		agentVersion = boot.Agents().Version
		for _, a := range boot.Agents().Architectures() {
			arches = append(arches, api.Architecture(a))
		}
	}
	c.Agent = &struct {
		Architectures []api.Architecture `json:"architectures"`
		Version       string             `json:"version"`
	}{Architectures: arches, Version: agentVersion}

	// LZX CABs (ADR 0019).
	lzx := &struct {
		Available bool    `json:"available"`
		Reason    *string `json:"reason,omitempty"`
		Tool      *string `json:"tool"`
	}{}
	if t, err := archive.LookupLZXTool(); err != nil {
		lzx.Reason = str(err.Error())
	} else {
		lzx.Available, lzx.Tool = true, &t.Name
	}
	c.CabLzx = lzx
	return c
}
