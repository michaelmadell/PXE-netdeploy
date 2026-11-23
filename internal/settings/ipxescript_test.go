package settings

import (
	"context"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/michaelmadell/pxe-netdeploy/internal/boot"
)

// linuxBooter answers every client with a Linux profile (armed for
// 10.77.0.150's session).
type linuxBooter struct{}

func (linuxBooter) Boot(context.Context, boot.Request) (boot.Answer, bool, string) {
	return boot.Answer{File: "ipxe/x64/ipxe-shim.efi", Linux: &boot.LinuxAnswer{ImageID: "IMG"}}, true, ""
}

func (linuxBooter) Default(context.Context, boot.Arch) (boot.Answer, bool, string) {
	return boot.Answer{File: "ipxe/x64/ipxe-shim.efi", Linux: &boot.LinuxAnswer{ImageID: "IMG"}}, true, ""
}

// TestIPXEScript: Linux profiles get the generated gate (armed or not, from
// the session or the default profile); Windows ones the static script
// (ADR 0034).
func TestIPXEScript(t *testing.T) {
	svc := testService(t, t.TempDir(), func(o *Options) { o.Booter = linuxBooter{} })
	r := httptest.NewRequest("GET", "/content/boot/ipxe/x64/autoexec.ipxe", nil)
	script := svc.ipxeScript("lab0")

	// No session, no lease: the default profile, a Linux one.
	body, attrs, ok := script(r, netip.MustParseAddr("10.77.0.140"), "x64")
	if !ok || !strings.Contains(string(body), "Press any key within 30 s to deploy") || !hasAttr(attrs, "boot_session", "none") {
		t.Fatalf("default:\n%s %v", body, attrs)
	}
	// An armed session.
	ip := netip.MustParseAddr("10.77.0.150")
	svc.Sessions().Record(boot.Session{Interface: "lab0", IP: ip, MAC: "00:15:5d:00:00:01",
		Answer: boot.Answer{Linux: &boot.LinuxAnswer{ImageID: "IMG", Armed: true}}})
	if body, _, ok := script(r, ip, "x64"); !ok || !strings.Contains(string(body), "param armed true") {
		t.Errorf("armed:\n%s", body)
	}
	// A Windows session: the static script.
	win := netip.MustParseAddr("10.77.0.160")
	svc.Sessions().Record(boot.Session{Interface: "lab0", IP: win, MAC: "00:15:5d:00:00:02", Answer: boot.Answer{File: boot.PXEBootFile}})
	if _, _, ok := script(r, win, "x64"); ok {
		t.Error("Windows client got a generated script")
	}
}
