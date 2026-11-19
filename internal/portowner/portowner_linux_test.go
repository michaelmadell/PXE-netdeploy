package portowner

import (
	"os"
	"path/filepath"
	"testing"
)

// A fake /proc: dnsmasq holds UDP 67 (0x43); a TCP socket on 80 that isn't
// listening doesn't count.
func TestLookupProc(t *testing.T) {
	proc := t.TempDir()
	os.MkdirAll(filepath.Join(proc, "net"), 0o755)
	udp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n" +
		"  12: 00000000:0043 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 31337 2 0000000000000000 0\n"
	tcp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:0050 0100007F:9C40 01 00000000:00000000 00:00000000 00000000     0        0 4242 1 0000000000000000 20 4 30 10 -1\n"
	os.WriteFile(filepath.Join(proc, "net", "udp"), []byte(udp), 0o644)
	os.WriteFile(filepath.Join(proc, "net", "tcp"), []byte(tcp), 0o644)
	os.MkdirAll(filepath.Join(proc, "812", "fd"), 0o755)
	os.WriteFile(filepath.Join(proc, "812", "comm"), []byte("dnsmasq\n"), 0o644)
	os.Symlink("socket:[31337]", filepath.Join(proc, "812", "fd", "4"))
	os.MkdirAll(filepath.Join(proc, "900", "fd"), 0o755)
	os.Symlink("socket:[4242]", filepath.Join(proc, "900", "fd", "3"))

	if got := lookupIn(proc, "udp", 67); got != "dnsmasq (pid 812)" {
		t.Errorf("udp 67: %q", got)
	}
	if got := lookupIn(proc, "tcp", 80); got != "" {
		t.Errorf("tcp 80 not listening: %q", got)
	}
	if got := lookupIn(proc, "udp", 69); got != "" {
		t.Errorf("udp 69: %q", got)
	}
}
