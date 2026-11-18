package server

import (
	"testing"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
)

// Without signing in, health says only how things are, not how they're set
// up (ADR 0029).
func TestPublicHealth(t *testing.T) {
	detail := "lab0 10.77.0.1:69"
	fails := []api.BindFailure{{Interface: "lab0", Message: "in use", Port: 67, Protocol: "udp"}}
	w := []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{{Code: "no_default_profile", Message: "x"}}
	h := api.Health{Status: api.HealthStatusDegraded, Warnings: &w, Storage: &api.StorageHealth{TotalBytes: 1},
		Services: map[string]api.ServiceHealth{
			"tftp": {Status: api.ServiceHealthStatusOk, Detail: &detail},
			"dhcp": {Status: api.ServiceHealthStatusError, Detail: &detail, BindFailures: &fails},
		}}
	p := publicHealth(h)
	if p.Status != api.HealthStatusDegraded || p.Warnings != nil || p.Storage != nil || p.Backup != nil {
		t.Errorf("public health: %+v", p)
	}
	for k, v := range p.Services {
		if v.Detail != nil || v.BindFailures != nil || v.Status != h.Services[k].Status {
			t.Errorf("%s: %+v", k, v)
		}
	}
}
