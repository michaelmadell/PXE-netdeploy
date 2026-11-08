//go:build windows

package disk

import "testing"

// The host has at least one disk, with a size and a bus type.
func TestListDisks(t *testing.T) {
	disks := List()
	if len(disks) == 0 {
		t.Fatal("no disks")
	}
	for _, d := range disks {
		t.Log(d)
		if d.SizeBytes <= 0 || d.Bus == "" {
			t.Errorf("incomplete: %+v", d)
		}
	}
}
