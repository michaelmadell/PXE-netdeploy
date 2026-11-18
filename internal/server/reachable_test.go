package server

import (
	"net/netip"
	"testing"
)

func TestAgentReachable(t *testing.T) {
	eps := []netip.AddrPort{netip.MustParseAddrPort("10.77.0.1:8080"), netip.MustParseAddrPort("192.0.2.10:8080")}
	for _, c := range []struct {
		urls []string
		want bool
	}{
		{[]string{"http://10.77.0.1:8080"}, true},
		{[]string{"http://192.0.2.10:8080/"}, true}, // advertisedAddress
		{[]string{"http://10.77.0.1"}, false},       // port 80 isn't served
		{[]string{"http://10.77.0.2:8080"}, false},  // another address
		{[]string{"http://deploy.lab:8080"}, true},  // a name: the port decides
		{[]string{"https://10.77.0.1:8080"}, false}, // no HTTPS yet
		{[]string{"http://10.77.0.2:8080", "http://10.77.0.1:8080"}, true},
		{nil, false},
	} {
		if got := agentReachable(c.urls, eps); got != c.want {
			t.Errorf("%v: %v, want %v", c.urls, got, c.want)
		}
	}
	if agentReachable([]string{"http://10.77.0.1:8080"}, nil) {
		t.Error("no endpoints")
	}
}
