package disk

import (
	"encoding/binary"
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

func TestParseLayout(t *testing.T) {
	le := binary.LittleEndian
	b := make([]byte, 48+3*144)
	le.PutUint32(b[0:], 1) // GPT
	le.PutUint32(b[4:], 3)
	for i, first := range []byte{0xAA, 0, 0xBB} { // the middle entry has a zero GUID
		e := b[48+i*144:]
		le.PutUint32(e[0:], 1)
		e[48] = first
	}
	got := parseLayout(b)
	if len(got) != 2 || got[0][0] != 0xAA || got[1][0] != 0xBB {
		t.Fatalf("%v", got)
	}
	le.PutUint32(b[0:], 0) // MBR
	if got := parseLayout(b); got != nil {
		t.Errorf("MBR: %v", got)
	}
}
