package hostcheck

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProbe(t *testing.T) {
	p := Probe()
	if p.ICMP != "raw" && p.ICMP != "datagram" && p.ICMP != "none" {
		t.Errorf("icmp %q", p.ICMP)
	}
	t.Logf("%s: %+v", runtime.GOOS, p)
	if !p.Port(8443) {
		t.Error("high ports always allowed")
	}
}

func TestDataDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "secrets.key"), []byte("k"), 0o600)
	if got := DataDir(dir); len(got) != 0 {
		t.Errorf("private: %v", got)
	}
	os.Chmod(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "admin-key.pem"), []byte("k"), 0o644)
	os.WriteFile(filepath.Join(dir, "netdeploy.db"), []byte("k"), 0o644)
	got := strings.Join(DataDir(dir), "\n")
	if !strings.Contains(got, "the data directory") || !strings.Contains(got, "admin-key.pem") || !strings.Contains(got, "netdeploy.db") || strings.Contains(got, "secrets.key") {
		t.Errorf("open: %s", got)
	}
}
