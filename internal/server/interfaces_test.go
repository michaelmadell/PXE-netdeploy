package server

import (
	"net/http"
	"net/netip"
	"testing"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/settings"
)

type fakeHostLister []settings.HostInterface

func (f fakeHostLister) Interfaces() ([]settings.HostInterface, error) { return f, nil }

func TestListHostInterfaces(t *testing.T) {
	host := fakeHostLister{
		{Name: "Loopback Pseudo-Interface 1", Prefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/8")}, Up: true, Loopback: true},
		{Name: "vEthernet (NetDeployLab)", MAC: "00:15:5d:00:85:01", Up: true,
			Prefixes: []netip.Prefix{netip.MustParsePrefix("10.77.0.1/24"), netip.MustParsePrefix("10.77.1.1/24")}},
		{Name: "Wi-Fi", MAC: "a0:b1:c2:d3:e4:f5"},
	}
	mux := http.NewServeMux()
	strict := api.NewStrictHandlerWithOptions(New("test", Deps{Host: host}), nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: RequestError, ResponseErrorHandlerFunc: ResponseError,
	})
	api.HandlerWithOptions(strict, api.StdHTTPServerOptions{BaseURL: "/api/v1", BaseRouter: mux, ErrorHandlerFunc: RequestError})

	rec, page := call(Recover(mux), "GET", "/api/v1/system/interfaces", "")
	items, _ := page["items"].([]any)
	if rec.Code != http.StatusOK || len(items) != 3 {
		t.Fatalf("list: %d %v", rec.Code, page)
	}
	lo, lab, wifi := items[0].(map[string]any), items[1].(map[string]any), items[2].(map[string]any)
	if lo["loopback"] != true || lo["mac"] != nil {
		t.Errorf("loopback: %v", lo)
	}
	addrs, _ := lab["addresses"].([]any)
	if lab["name"] != "vEthernet (NetDeployLab)" || lab["mac"] != "00:15:5d:00:85:01" || lab["up"] != true || len(addrs) != 2 {
		t.Fatalf("lab interface: %v", lab)
	}
	if first := addrs[0].(map[string]any); first["address"] != "10.77.0.1" || first["prefixLength"] != float64(24) {
		t.Errorf("primary address: %v", first)
	}
	// No addresses is an empty list, not null: the UI maps over it.
	if a, ok := wifi["addresses"].([]any); !ok || len(a) != 0 || wifi["up"] != false {
		t.Errorf("disconnected interface: %v", wifi)
	}
}
