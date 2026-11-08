package bcd

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func phase1(t *testing.T) *Hive {
	t.Helper()
	b, err := os.ReadFile("testdata/phase1.bcd")
	if err != nil {
		t.Fatal(err)
	}
	h, err := ParseHive(b)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

var phase1Opts = RAMDisk{WIMPath: `\Boot\boot.wim`, SDIPath: `\Boot\boot.sdi`, TFTPBlockSize: 16384, TFTPWindowSize: 16,
	Description: "WinPE", TimeoutSeconds: 5}

// Our store reads back as the Phase 1 store (made by bcdedit, booted under
// Secure Boot), apart from the loader's GUID.
func TestMatchesPhase1(t *testing.T) {
	ref := phase1(t)
	const refLoader = "{0bca49e1-d951-11f1-8d01-9cc7d36320c7}"
	want := strings.ReplaceAll(Dump(ref.Root), refLoader, WinPEID)

	b, err := phase1Opts.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	h, err := ParseHive(b)
	if err != nil {
		t.Fatal(err)
	}
	// The reference lists objects in GUID order; ours too, but our loader
	// GUID sorts differently, so compare object by object.
	got := Dump(h.Root)
	for _, id := range []string{BootMgrID, WinPEID, RAMDiskOptionsID} {
		if g, w := objectDump(t, got, id), objectDump(t, want, id); g != w {
			t.Errorf("object %s:\n got:\n%s\nwant:\n%s", id, g, w)
		}
	}
	if g, w := section(got, "  [Description]"), section(want, "  [Description]"); g != w {
		t.Errorf("Description:\n got %s\nwant %s", g, w)
	}
	if !bytes.Equal(h.Security, DefaultSecurity) {
		t.Error("security descriptor not read back")
	}
}

// objectDump cuts one object's lines out of a Dump.
func objectDump(t *testing.T, dump, id string) string {
	t.Helper()
	s := section(dump, "    ["+id+"]")
	if s == "" {
		t.Fatalf("object %s missing", id)
	}
	return s
}

// section returns the line starting with head and the deeper-indented
// lines after it.
func section(dump, head string) string {
	lines := strings.Split(dump, "\n")
	for i, l := range lines {
		if l != head {
			continue
		}
		indent := len(l) - len(strings.TrimLeft(l, " "))
		out := []string{l}
		for _, m := range lines[i+1:] {
			if len(m)-len(strings.TrimLeft(m, " ")) <= indent {
				break
			}
			out = append(out, m)
		}
		return strings.Join(out, "\n")
	}
	return ""
}

func TestDeterministicAndOptions(t *testing.T) {
	a, _ := phase1Opts.Bytes()
	b, _ := phase1Opts.Bytes()
	if !bytes.Equal(a, b) {
		t.Fatal("same inputs, different bytes")
	}
	o := phase1Opts
	o.TFTPBlockSize, o.TFTPWindowSize = 1456, 4
	c, _ := o.Bytes()
	h, err := ParseHive(c)
	if err != nil {
		t.Fatal(err)
	}
	elems := h.Root.Subkey("Objects").Subkey(RAMDiskOptionsID).Subkey("Elements")
	for typ, want := range map[string][]byte{"35000007": {0xb0, 5, 0, 0, 0, 0, 0, 0}, "35000008": {4, 0, 0, 0, 0, 0, 0, 0}} {
		if v, _ := elems.Subkey(typ).Value("Element"); !bytes.Equal(v.Data, want) {
			t.Errorf("%s = % x", typ, v.Data)
		}
	}
	for _, bad := range []RAMDisk{
		{WIMPath: "Boot/boot.wim", SDIPath: `\Boot\boot.sdi`, TFTPBlockSize: 1456, TFTPWindowSize: 1},
		{WIMPath: `\Boot\boot.wim`, SDIPath: `\Boot\boot.sdi`, TFTPBlockSize: 100, TFTPWindowSize: 1},
	} {
		if _, err := bad.Bytes(); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestHiveRoundTrip(t *testing.T) {
	root := &Key{Name: "Root", Subkeys: []*Key{
		{Name: "b", Values: []Value{{Name: "empty", Type: RegBinary}, {Name: "small", Type: RegDWORD, Data: []byte{1, 2, 3, 4}}}},
		{Name: "A", Values: []Value{{Name: "big", Type: RegBinary, Data: bytes.Repeat([]byte{7}, 5000)}}},
		{Name: "Ünïcode✓", Subkeys: []*Key{{Name: "child"}}},
	}}
	b, err := WriteHive(root, WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h, err := ParseHive(b)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range h.Root.Subkeys {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "A,b,Ünïcode✓" {
		t.Errorf("subkey order %v", names)
	}
	if v, _ := h.Root.Subkey("A").Value("big"); len(v.Data) != 5000 {
		t.Errorf("big value: %d bytes", len(v.Data))
	}
	if v, _ := h.Root.Subkey("b").Value("small"); !bytes.Equal(v.Data, []byte{1, 2, 3, 4}) {
		t.Errorf("inline value %v", v.Data)
	}
	if h.Root.Subkey("Ünïcode✓").Subkey("child") == nil {
		t.Error("UTF-16 key name")
	}
	if _, err := WriteHive(&Key{Name: "r", Subkeys: []*Key{{Name: "x"}, {Name: "X"}}}, WriteOptions{}); err == nil {
		t.Error("duplicate subkeys accepted")
	}
	// A damaged checksum is caught.
	b[100] ^= 1
	if _, err := ParseHive(b); err == nil {
		t.Error("bad checksum accepted")
	}
}
