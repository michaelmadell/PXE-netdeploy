package iso9660

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func load(t testing.TB, name string) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name+".iso.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func open(t *testing.T, name string) *FS {
	t.Helper()
	b := load(t, name)
	f, err := Open(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return f
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

var longName = strings.Repeat("long-name-", 20) + ".txt"

func TestRockRidge(t *testing.T) {
	f := open(t, "rr-joliet")
	if f.Names() != RockRidge || f.Label() != "NDTEST_RRJOLIET" {
		t.Fatalf("names %s, label %q", f.Names(), f.Label())
	}
	want := map[string]string{
		"EFI/BOOT/BOOTX64.EFI":     "not really a boot manager",
		"empty.txt":                "",
		"Deep/a/b/c/d/leaf.txt":    "leaf",
		".disk/info":               `Ubuntu-Server 26.04.1 LTS "Resolute Raccoon" - Release amd64 (20260826)`,
		"casper/vmlinuz":           "kernel",
		"sources/Ünïcode-文件.txt":   "unicode name",
		"sources/big.bin":          string(pattern(200<<10 + 5)),
		"sources/sxs/file-000.cab": "0",
		"sources/sxs/file-149.cab": "149",
		longName:                   "long",
	}
	check(t, f, want)
	// Case-insensitive.
	if got, err := fs.ReadFile(f, "efi/boot/bootx64.efi"); err != nil || string(got) != want["EFI/BOOT/BOOTX64.EFI"] {
		t.Errorf("case-insensitive: %q %v", got, err)
	}
	// The symlink is reported, not followed.
	fi, err := fs.Stat(f, "sources/link-to-empty")
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 || fi.Size() != 0 {
		t.Errorf("symlink: %v %v", fi, err)
	}
	if n := walk(t, f); n != 159 {
		t.Errorf("walk: %d regular files, want 159", n)
	}
	if err := fstest.TestFS(f, "EFI/BOOT/BOOTX64.EFI", "sources/big.bin", "Deep/a/b/c/d/leaf.txt", ".disk/info"); err != nil {
		t.Error(err)
	}
}

func TestJoliet(t *testing.T) {
	f := open(t, "joliet")
	if f.Names() != Joliet {
		t.Fatalf("names %s", f.Names())
	}
	check(t, f, map[string]string{
		"EFI/BOOT/BOOTX64.EFI":   "not really a boot manager",
		".disk/info":             `Ubuntu-Server 26.04.1 LTS "Resolute Raccoon" - Release amd64 (20260826)`,
		"sources/Ünïcode-文件.txt": "unicode name",
		"sources/big.bin":        string(pattern(200<<10 + 5)),
		"Deep/a/b/c/d/leaf.txt":  "leaf",
	})
	// Joliet names are cut short (xorriso keeps 104 characters with the extension).
	ents, err := fs.ReadDir(f, ".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	found := false
	for _, e := range ents {
		names = append(names, e.Name())
		if strings.HasPrefix(e.Name(), "long-name-long-name-") && len(e.Name()) < len(longName) {
			found = true
		}
	}
	if !found {
		t.Errorf("truncated long name not found in %q", names)
	}
}

func TestPlain(t *testing.T) {
	f := open(t, "plain")
	if f.Names() != Plain {
		t.Fatalf("names %s", f.Names())
	}
	check(t, f, map[string]string{
		"EFI/BOOT/BOOTX64.EFI": "not really a boot manager",
		"casper/vmlinuz":       "kernel", // VMLINUZ.;1 → VMLINUZ, matched ignoring case
		"sources/big.bin":      string(pattern(200<<10 + 5)),
	})
	ents, _ := fs.ReadDir(f, "CASPER")
	for _, e := range ents {
		if strings.ContainsAny(e.Name(), ";") || strings.HasSuffix(e.Name(), ".") {
			t.Errorf("plain name not cleaned: %q", e.Name())
		}
	}
}

func check(t *testing.T, f fs.FS, want map[string]string) {
	t.Helper()
	for p, body := range want {
		got, err := fs.ReadFile(f, p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if string(got) != body {
			t.Errorf("%s: %d bytes, want %d", p, len(got), len(body))
		}
	}
}

func walk(t *testing.T, f fs.FS) int {
	t.Helper()
	n := 0
	err := fs.WalkDir(f, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReaderAt(t *testing.T) {
	f := open(t, "rr-joliet")
	file, err := f.Open("sources/big.bin")
	if err != nil {
		t.Fatal(err)
	}
	ra := file.(io.ReaderAt)
	want := pattern(200<<10 + 5)
	buf := make([]byte, 5000)
	for _, off := range []int64{0, 2047, 2048, 100_000, int64(len(want)) - 5000} {
		if n, err := ra.ReadAt(buf, off); n != len(buf) || err != nil || !bytes.Equal(buf, want[off:off+5000]) {
			t.Errorf("ReadAt(%d): %d %v", off, n, err)
		}
	}
	if n, err := ra.ReadAt(buf, int64(len(want))-10); n != 10 || err != io.EOF {
		t.Errorf("ReadAt past the end: %d %v", n, err)
	}
}

func TestNotISO(t *testing.T) {
	b := make([]byte, 40*BlockSize)
	if _, err := Open(bytes.NewReader(b), int64(len(b))); !errors.Is(err, ErrNotISO9660) {
		t.Errorf("zeros: %v", err)
	}
	if _, err := Open(bytes.NewReader(b[:100]), 100); !errors.Is(err, ErrNotISO9660) {
		t.Errorf("short: %v", err)
	}
}

// image builds a minimal plain ISO 9660 image: PVD at 16, terminator at 17,
// the root directory at 18 holding recs (after "." and ".."), data after.
func image(t *testing.T, recs [][]byte, data map[uint32][]byte) []byte {
	t.Helper()
	img := make([]byte, 40*BlockSize)
	pvd := img[16*BlockSize:]
	pvd[0], pvd[6] = 1, 1
	copy(pvd[1:], "CD001")
	copy(pvd[40:72], "SYNTH                           ")
	binary.LittleEndian.PutUint16(pvd[128:], BlockSize)
	copy(pvd[156:], rec("\x00", 18, BlockSize, 2, 0))
	term := img[17*BlockSize:]
	term[0], term[6] = 255, 1
	copy(term[1:], "CD001")
	dir := img[18*BlockSize : 19*BlockSize]
	off := copy(dir, rec("\x00", 18, BlockSize, 2, 0))
	off += copy(dir[off:], rec("\x01", 18, BlockSize, 2, 0))
	for _, r := range recs {
		off += copy(dir[off:], r)
	}
	for blk, b := range data {
		copy(img[int(blk)*BlockSize:], b)
	}
	return img
}

func rec(name string, block, length uint32, flags byte, _ int) []byte {
	n := 33 + len(name)
	if len(name)%2 == 0 {
		n++
	}
	r := make([]byte, n)
	r[0] = byte(n)
	binary.LittleEndian.PutUint32(r[2:], block)
	binary.BigEndian.PutUint32(r[6:], block)
	binary.LittleEndian.PutUint32(r[10:], length)
	binary.BigEndian.PutUint32(r[14:], length)
	r[25] = flags
	r[32] = byte(len(name))
	copy(r[33:], name)
	return r
}

func TestMultiExtent(t *testing.T) {
	a := bytes.Repeat([]byte("a"), BlockSize)
	b := []byte("tail")
	img := image(t, [][]byte{
		rec("BIG.BIN;1", 20, BlockSize, 0x80, 0),
		rec("BIG.BIN;1", 22, uint32(len(b)), 0, 0),
		rec("SMALL.TXT;1", 24, 3, 0, 0),
	}, map[uint32][]byte{20: a, 22: b, 24: []byte("abc")})
	f, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := fs.ReadFile(f, "BIG.BIN")
	if err != nil || !bytes.Equal(got, append(append([]byte{}, a...), b...)) {
		t.Fatalf("multi-extent: %d bytes, %v", len(got), err)
	}
	if got, _ := fs.ReadFile(f, "small.txt"); string(got) != "abc" {
		t.Errorf("small.txt: %q", got)
	}
}

func TestCorrupt(t *testing.T) {
	cases := map[string][][]byte{
		"extent outside":    {rec("X;1", 1_000_000, 10, 0, 0)},
		"cut multi-extent":  {rec("X;1", 20, BlockSize, 0x80, 0)},
		"multi-extent name": {rec("X;1", 20, BlockSize, 0x80, 0), rec("Y;1", 22, 1, 0, 0)},
		"interleaved":       {func() []byte { r := rec("X;1", 20, 1, 0, 0); r[26] = 1; return r }()},
	}
	for name, recs := range cases {
		img := image(t, recs, nil)
		f, err := Open(bytes.NewReader(img), int64(len(img)))
		if err != nil {
			t.Fatalf("%s: open: %v", name, err)
		}
		_, err = fs.ReadFile(f, "X")
		if name == "extent outside" {
			if err == nil {
				t.Errorf("%s: no error", name)
			}
			continue
		}
		if _, err := fs.ReadDir(f, "."); !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A directory that contains itself must not recurse forever.
func TestDirectoryLoop(t *testing.T) {
	img := image(t, [][]byte{rec("LOOP", 18, BlockSize, 2, 0)}, nil)
	f, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	err = fs.WalkDir(f, ".", func(p string, d fs.DirEntry, err error) error { return err })
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("walk: %v", err)
	}
}

// FuzzOpen: run with -fuzzminimizetime 0 (minimizing the 48 KiB seeds
// takes most of the time otherwise). 12 million inputs ran clean on
// 2026-11-23.
func FuzzOpen(f *testing.F) {
	for _, n := range []string{"rr-joliet", "joliet", "plain"} {
		b := load(f, n)
		f.Add(b[:24*BlockSize])
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		fsys, err := Open(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return
		}
		_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.Type().IsRegular() {
				_, _ = fs.ReadFile(fsys, p)
			}
			return nil
		})
	})
}

// Two entries naming the same directory would make a walk exponential.
func TestSharedDirectory(t *testing.T) {
	sub := make([]byte, BlockSize)
	off := copy(sub, rec("\x00", 20, BlockSize, 2, 0))
	copy(sub[off:], rec("\x01", 18, BlockSize, 2, 0))
	img := image(t, [][]byte{rec("A", 20, BlockSize, 2, 0), rec("B", 20, BlockSize, 2, 0)}, map[uint32][]byte{20: sub})
	f, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadDir(f, "."); !errors.Is(err, ErrCorrupt) {
		t.Errorf("readdir: %v", err)
	}
}
