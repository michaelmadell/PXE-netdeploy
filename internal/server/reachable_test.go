package server

import (
	"net/netip"
	"slices"
	"testing"
)

func TestAgentReachable(t *testing.T) {
	eps := []netip.AddrPort{netip.MustParseAddrPort("10.77.0.1:8080"), netip.MustParseAddrPort("192.0.2.10:8080")}
	tls := []netip.AddrPort{netip.MustParseAddrPort("10.77.0.1:8443")}
	for _, c := range []struct {
		urls []string
		want bool
	}{
		{[]string{"http://10.77.0.1:8080"}, true},
		{[]string{"http://192.0.2.10:8080/"}, true}, // advertisedAddress
		{[]string{"http://10.77.0.1"}, false},       // port 80 isn't served
		{[]string{"http://10.77.0.2:8080"}, false},  // another address
		{[]string{"http://deploy.lab:8080"}, true},  // a name: the port decides
		{[]string{"https://10.77.0.1:8080"}, false}, // HTTP's port, not HTTPS's
		{[]string{"https://10.77.0.1:8443"}, true},
		{[]string{"https://deploy.lab:8443"}, true},
		{[]string{"https://10.77.0.1"}, false}, // 443 isn't served
		{[]string{"http://10.77.0.1:8443"}, false},
		{[]string{"http://10.77.0.2:8080", "http://10.77.0.1:8080"}, true},
		{nil, false},
	} {
		if got := agentReachable(c.urls, eps, tls); got != c.want {
			t.Errorf("%v: %v, want %v", c.urls, got, c.want)
		}
	}
	if agentReachable([]string{"http://10.77.0.1:8080"}, nil, nil) {
		t.Error("no endpoints")
	}
}

func TestAgentOverHTTP(t *testing.T) {
	tls := []netip.AddrPort{netip.MustParseAddrPort("10.77.0.1:443")}
	got := agentOverHTTP([]string{"http://10.77.0.1:8080", "http://10.77.0.2", "http://deploy.lab", "https://10.77.0.1"}, tls)
	if !slices.Equal(got, []string{"http://10.77.0.1:8080"}) {
		t.Errorf("got %v", got)
	}
	if got := agentOverHTTP([]string{"http://10.77.0.1:8080"}, nil); got != nil {
		t.Errorf("without HTTPS: %v", got)
	}
}
