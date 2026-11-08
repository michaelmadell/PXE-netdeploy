package sysinfo

import "testing"

func TestParseSMBIOS(t *testing.T) {
	// A type 0 structure, then type 1, then the end.
	t0 := append([]byte{0, 4, 0, 0}, "BIOS\x00\x00"...)
	t1 := []byte{1, 0x1B, 1, 0, 1, 2, 0, 3,
		0x61, 0x02, 0x6c, 0x8b, 0x7a, 0x50, 0xc0, 0x4b, 0x9d, 0x52, 0x6b, 0x1c, 0x2e, 0x3f, 0x4d, 0x5a, // UUID
		6, 0, 0}
	t1 = append(t1, "Microsoft Corporation\x00Virtual Machine\x00 1234-5678 \x00\x00"...)
	end := []byte{127, 4, 0, 0, 0, 0}
	s, ok := ParseSMBIOS(append(append(t0, t1...), end...))
	if !ok || s.Manufacturer != "Microsoft Corporation" || s.Model != "Virtual Machine" || s.Serial != "1234-5678" ||
		s.UUID != "8b6c0261-507a-4bc0-9d52-6b1c2e3f4d5a" {
		t.Fatalf("%+v %v", s, ok)
	}
}
