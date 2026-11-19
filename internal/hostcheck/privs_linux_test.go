package hostcheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEffectiveCaps(t *testing.T) {
	dir := t.TempDir()
	status := filepath.Join(dir, "status")
	os.WriteFile(status, []byte("Name:\tnetdeployd\nCapInh:\t0000000000000000\nCapEff:\t0000000000002400\n"), 0o600)
	eff := effectiveCaps(status)
	if eff&(1<<bitNetBindService) == 0 || eff&(1<<bitNetRaw) == 0 {
		t.Errorf("CapEff %x", eff)
	}
	if effectiveCaps(filepath.Join(dir, "missing")) != 0 {
		t.Error("unreadable status")
	}
	port := filepath.Join(dir, "port")
	os.WriteFile(port, []byte("0\n"), 0o600)
	if unprivilegedPortStart(port) != 0 || unprivilegedPortStart(filepath.Join(dir, "x")) != 1024 {
		t.Error("ip_unprivileged_port_start")
	}
}
