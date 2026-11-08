package firmware

import (
	"encoding/binary"
	"fmt"
	"testing"
	"unicode/utf16"
)

type fakeVars map[string][]byte

func (f fakeVars) Get(n string) ([]byte, error) {
	if b, ok := f[n]; ok {
		return b, nil
	}
	return nil, ErrNotFound
}
func (f fakeVars) Set(n string, b []byte) error { f[n] = b; return nil }

// option builds an EFI_LOAD_OPTION with the given device path nodes.
func option(active bool, desc string, nodes ...[]byte) []byte {
	var path []byte
	for _, n := range nodes {
		path = append(path, n...)
	}
	path = append(path, 0x7F, 0xFF, 4, 0)
	b := make([]byte, 6)
	if active {
		b[0] = 1
	}
	binary.LittleEndian.PutUint16(b[4:], uint16(len(path)))
	for _, c := range utf16.Encode([]rune(desc)) {
		b = binary.LittleEndian.AppendUint16(b, c)
	}
	b = append(b, 0, 0)
	return append(b, path...)
}

func node(typ, sub byte, data []byte) []byte {
	b := []byte{typ, sub, 0, 0}
	binary.LittleEndian.PutUint16(b[2:], uint16(4+len(data)))
	return append(b, data...)
}

func filePath(p string) []byte {
	var d []byte
	for _, c := range utf16.Encode([]rune(p + "\x00")) {
		d = binary.LittleEndian.AppendUint16(d, c)
	}
	return node(4, 4, d)
}

var (
	hd  = node(4, 1, make([]byte, 38))
	mac = node(3, 11, make([]byte, 33))
	ip4 = node(3, 12, make([]byte, 23))
)

func vars() fakeVars {
	v := fakeVars{
		"Boot0000": option(true, "EFI SCSI Device", hd),
		"Boot0001": option(true, "EFI Network", mac, ip4),
		"Boot0002": option(true, "Windows Boot Manager", hd, filePath(`\EFI\Microsoft\Boot\bootmgfw.efi`)),
		"Boot0003": option(false, "Old Windows", hd),
	}
	v["BootOrder"] = encodeOrder([]uint16{3, 2, 1, 0})
	return v
}

func TestEntries(t *testing.T) {
	es, err := Entries(vars())
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 4 || es[1].Description != "Windows Boot Manager" || !es[1].Disk || es[1].File != `\EFI\Microsoft\Boot\bootmgfw.efi` ||
		!es[2].Network || es[2].Disk || es[0].Active {
		t.Fatalf("%+v", es)
	}
	if e, ok := LocalDisk(es); !ok || e.Number != 2 {
		t.Errorf("local disk: %+v %v", e, ok)
	}
	if _, ok := LocalDisk(es[2:3]); ok {
		t.Error("network entry taken as local disk")
	}
}

func TestNetworkFirstAndBootNext(t *testing.T) {
	v := vars()
	if _, err := NetworkFirst(v); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(must(bootOrder(v))); got != "[1 3 2 0]" {
		t.Errorf("order %s", got)
	}
	if err := SetBootNext(v, 2); err != nil || binary.LittleEndian.Uint16(v["BootNext"]) != 2 {
		t.Errorf("BootNext %v", v["BootNext"])
	}
	delete(v, "Boot0001")
	v["BootOrder"] = encodeOrder([]uint16{2, 0})
	if _, err := NetworkFirst(v); err == nil {
		t.Error("no network entry: no error")
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
