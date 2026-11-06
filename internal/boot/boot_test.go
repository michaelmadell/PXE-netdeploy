package boot

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

func TestSessions(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s := &Sessions{Now: func() time.Time { return now }}
	ip := netip.MustParseAddr("10.77.0.100")
	s.Record(Session{Interface: "lab0", IP: ip, MAC: "00:15:5d:00:00:01"})

	if got, ok := s.Lookup("lab0", ip); !ok || got.MAC != "00:15:5d:00:00:01" {
		t.Fatalf("Lookup = %+v, %v", got, ok)
	}
	if _, ok := s.Lookup("lab1", ip); ok {
		t.Error("session leaked across interfaces")
	}
	now = now.Add(11 * time.Minute)
	if _, ok := s.Lookup("lab0", ip); ok {
		t.Error("session outlived its TTL")
	}

	if !s.SightingDue("m", "") || s.SightingDue("m", "") {
		t.Error("second sighting inside the gap was not throttled")
	}
	// DISCOVER then REQUEST seconds later: the newly known address is written.
	if !s.SightingDue("m", "10.77.0.140") {
		t.Error("first sighting with an address was throttled")
	}
	if s.SightingDue("m", "10.77.0.140") || s.SightingDue("m", "") {
		t.Error("repeat sighting inside the gap was not throttled")
	}
	now = now.Add(31 * time.Second)
	if !s.SightingDue("m", "") {
		t.Error("sighting after the gap was throttled")
	}
}

func TestStatic(t *testing.T) {
	b := Static{File: "bootmgfw.efi"}
	if a, ok, _ := b.Boot(context.Background(), Request{Arch: ArchUEFIX64}); !ok || a.File != "bootmgfw.efi" {
		t.Errorf("uefi-x64: %+v %v", a, ok)
	}
	for _, arch := range []Arch{ArchUEFIX64HTTP, ArchUEFIArm64, ArchBIOS} {
		if _, ok, why := b.Boot(context.Background(), Request{Arch: arch}); ok || why == "" {
			t.Errorf("%s: answered without content", arch)
		}
	}
}
