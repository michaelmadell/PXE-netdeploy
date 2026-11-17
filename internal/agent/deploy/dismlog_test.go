package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDismErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dism.log")
	log := strings.Join([]string{
		"2026-11-17 13:17:31, Info                  DISM   DISM Provider Store: PID=1 Getting the collection of providers",
		"2026-11-17 13:17:31, Warning               DISM   DISM Driver Manager: Failed to find the INF package in the driver store (hr = 0x80070490)",
		"2026-11-17 13:17:32, Error                 DISM   DISM Driver Manager: first (hr:0x80004002)",
		"2026-11-17 13:17:33, Error                 DISM   DISM.EXE: second",
		"2026-11-17 13:17:34, Error                 DISM   DISM.EXE: third",
	}, "\r\n")
	if err := os.WriteFile(p, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}
	got := DismErrors(p, 2)
	if len(got) != 2 || !strings.HasSuffix(got[0], "second") || !strings.HasSuffix(got[1], "third") {
		t.Errorf("%q", got)
	}
	if DismErrors(filepath.Join(t.TempDir(), "missing.log"), 5) != nil {
		t.Error("missing log: lines")
	}
}
