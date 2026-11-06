package tftp

import (
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"testing"
)

// Two clients with different boot sessions get different BCDs from the same
// listener (ADR 0006); an unknown client gets the default content.
func TestClientRootSelectsContentPerClient(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs 127.0.0.0/8 on loopback")
	}
	def, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	writeFile(t, def, "Boot/BCD", []byte("default"))
	writeFile(t, a, "Boot/BCD", []byte("client-a"))
	writeFile(t, b, "Boot/BCD", []byte("client-b"))

	roots := map[netip.Addr]string{
		netip.MustParseAddr("127.0.0.2"): a,
		netip.MustParseAddr("127.0.0.3"): b,
	}
	addr := startServer(t, def, Config{ClientRoot: func(ip netip.Addr) (string, []any) {
		return roots[ip], nil
	}})

	for ip, want := range map[string]string{"127.0.0.2": "client-a", "127.0.0.3": "client-b", "127.0.0.4": "default"} {
		c := &client{localIP: net.ParseIP(ip)}
		res, err := c.get(addr, `\Boot\BCD`)
		if err != nil {
			t.Fatalf("%s: %v", ip, err)
		}
		if string(res.data) != want {
			t.Errorf("%s got %q, want %q", ip, res.data, want)
		}
	}
}

func TestRootBCDNeverServed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "BCD", []byte("stray"))
	writeFile(t, dir, "Boot/BCD", []byte("real"))
	addr := startServer(t, dir, Config{})

	_, err := (&client{}).get(addr, `\BCD`)
	var te *tftpError
	if !errors.As(err, &te) || te.code != errFileNotFound {
		t.Fatalf(`\BCD: %v, want file not found`, err)
	}
	if res, err := (&client{}).get(addr, `\Boot\BCD`); err != nil || string(res.data) != "real" {
		t.Fatalf(`\Boot\BCD: %q %v`, res.data, err)
	}
}

func TestOptionalMissesLoggedAtDebug(t *testing.T) {
	var logs syncBuffer
	addr := startServer(t, t.TempDir(), Config{
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	for _, p := range []string{`\EFI\Microsoft\Boot\Policies\SbcpFlightToken.p7b`, `\EFI\Microsoft\Boot\SiPolicy.p7b`,
		`\boot.stl`, `\EFI\Microsoft\Boot\boot.stl`, `\BCD`, `\EFI\Microsoft\Boot\Policies\UnlockToken.pol`, `\Boot\boot.wim`} {
		(&client{}).get(addr, p)
	}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if !strings.Contains(line, "tftp request refused") {
			continue
		}
		wantWarn := strings.Contains(line, "boot.wim")
		if isWarn := strings.Contains(line, "level=WARN"); isWarn != wantWarn {
			t.Errorf("wrong level: %s", line)
		}
	}
}

func TestOptionalFile(t *testing.T) {
	for p, want := range map[string]bool{
		`\BCD`: true, `\Boot\BCD`: false, `\EFI\Microsoft\Boot\SecureBootPolicy.p7b`: true,
		`\EFI\Microsoft\Boot\VbsSiPolicy.p7b`: true, `/efi/microsoft/boot/policies/x`: true,
		`\boot.stl`: true, `\Boot\boot.sdi`: false, `\bootmgfw.efi`: false,
	} {
		if got := optionalFile(p); got != want {
			t.Errorf("optionalFile(%q) = %v", p, got)
		}
	}
}
