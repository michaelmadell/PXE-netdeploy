package portowner

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Our own listeners are always visible: the lookup finds this test process.
func TestLookupOwnSockets(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("no lookup on this OS")
	}
	exe, _ := os.Executable()
	want := strings.TrimSuffix(filepath.Base(exe), ".exe")
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if got := Lookup("tcp", port); !strings.Contains(got, "(pid ") || (runtime.GOOS == "windows" && !strings.HasPrefix(got, want)) {
		t.Errorf("tcp %d: %q (want %s)", port, got, want)
	}
	u, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	uport := u.LocalAddr().(*net.UDPAddr).Port
	if got := Lookup("udp", uport); !strings.Contains(got, "(pid ") {
		t.Errorf("udp %d: %q", uport, got)
	}
	if got := Lookup("udp", 1); got != "" && !strings.Contains(got, "(pid ") {
		t.Errorf("unused port: %q", got)
	}
}
