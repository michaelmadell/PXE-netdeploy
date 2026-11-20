package settings

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michaelmadell/pxe-netdeploy/internal/ipxe"
)

// TestHTTPBootBase: HTTP Boot clients are sent to the interface's HTTP
// listener (HTTPS needs the content CA), default ports left out, and get
// nothing without the iPXE chain or a listener (ADR 0032).
func TestHTTPBootBase(t *testing.T) {
	dir := t.TempDir()
	for _, n := range ipxe.Bundled {
		p := filepath.Join(dir, "x64", n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := ipxe.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	ip := netip.MustParseAddr("10.77.0.1")
	s := &Service{opts: Options{IPXE: b}}
	for name, c := range map[string]struct {
		svc  Services
		want string
	}{
		"http default port": {Services{HTTP: true, HTTPPort: 80}, "http://10.77.0.1/content/boot/"},
		"http other port":   {Services{HTTP: true, HTTPPort: 8080}, "http://10.77.0.1:8080/content/boot/"},
		"https without CA":  {Services{HTTP: true, HTTPPort: 80, HTTPS: true, HTTPSPort: 443}, "http://10.77.0.1/content/boot/"},
		"no listener":       {Services{TFTP: true}, ""},
	} {
		got, why := s.httpBootBase(Interface{Name: "lab0", Services: c.svc}, ip)
		if got != c.want || (got == "") != (why != "") {
			t.Errorf("%s: %q (%q), want %q", name, got, why, c.want)
		}
	}
	none := &Service{}
	if got, why := none.httpBootBase(Interface{Services: Services{HTTP: true, HTTPPort: 80}}, ip); got != "" || !strings.Contains(why, "iPXE") {
		t.Errorf("no chain: %q %q", got, why)
	}
}
