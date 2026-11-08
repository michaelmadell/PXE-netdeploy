package bcd

import (
	"os"
	"testing"
)

func TestParsePhase1(t *testing.T) {
	b, err := os.ReadFile("testdata/phase1.bcd")
	if err != nil {
		t.Fatal(err)
	}
	h, err := ParseHive(b)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("version %d.%d, security %d bytes\n%s", h.Major, h.Minor, len(h.Security), Dump(h.Root))
}
