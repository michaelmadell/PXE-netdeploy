package disk

import (
	"strings"
	"testing"
)

func TestSelect(t *testing.T) {
	gb := int64(1 << 30)
	disks := []Disk{
		{Number: 0, SizeBytes: 64 * gb, Bus: "USB"},
		{Number: 1, SizeBytes: 16 * gb, Bus: "SATA"},
		{Number: 3, SizeBytes: 512 * gb, Bus: "NVMe"},
		{Number: 2, SizeBytes: 256 * gb, Bus: "RAID"},
	}
	if d, err := Select(disks, nil); err != nil || d.Number != 2 {
		t.Errorf("auto: %+v %v", d, err)
	}
	three := 3
	if d, err := Select(disks, &three); err != nil || d.Number != 3 {
		t.Errorf("explicit: %+v %v", d, err)
	}
	nine := 9
	if _, err := Select(disks, &nine); err == nil {
		t.Error("missing explicit disk accepted")
	}
	if _, err := Select(disks[:2], nil); err == nil || !strings.Contains(err.Error(), "disk 0") {
		t.Errorf("no candidate: %v", err)
	}
}

func TestScript(t *testing.T) {
	s := Script(2)
	for _, want := range []string{"select disk 2\r\n", "convert gpt", "create partition efi size=260", "create partition msr size=16",
		"shrink minimum=1024", `assign letter="W"`, `set id="de94bba4-06d1-4d40-a16a-bfd50179d6ac"`} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q", want)
		}
	}
}
