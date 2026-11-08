package udf

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// image loads a gzipped fixture (see testdata/README.md).
func image(t *testing.T, name string) *bytes.Reader {
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
	return bytes.NewReader(b)
}

func open(t *testing.T, name string) *FS {
	t.Helper()
	r := image(t, name)
	u, err := Open(r, r.Size())
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return u
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func TestFixtures(t *testing.T) {
	for _, name := range []string{"udf102", "udf201"} {
		t.Run(name, func(t *testing.T) {
			u := open(t, name)
			if want := "NDTEST_" + strings.ToUpper(name); u.Label() != want {
				t.Errorf("label %q, want %q", u.Label(), want)
			}
			want := map[string]string{
				"EFI/Boot/bootx64.efi":     "not really a boot manager",
				"empty.txt":                "",
				"Deep/a/b/c/d/leaf.txt":    "leaf",
				"sources/Ünïcode-文件.txt":   "unicode name",
				"sources/big.bin":          string(pattern(200<<10 + 5)),
				"sources/sxs/file-000.cab": "0",
				"sources/sxs/file-149.cab": "149",
			}
			for p, body := range want {
				got, err := fs.ReadFile(u, p)
				if err != nil {
					t.Errorf("%s: %v", p, err)
					continue
				}
				if string(got) != body {
					t.Errorf("%s: %d bytes, want %d", p, len(got), len(body))
				}
			}
			// Case-insensitive, as Windows clients expect.
			if got, err := fs.ReadFile(u, "efi/BOOT/BOOTX64.EFI"); err != nil || string(got) != "not really a boot manager" {
				t.Errorf("case-insensitive: %q %v", got, err)
			}
			// The multi-sector directory lists all 150 files.
			ents, err := fs.ReadDir(u, "sources/sxs")
			if err != nil || len(ents) != 150 {
				t.Fatalf("sxs: %d entries, %v", len(ents), err)
			}
			// Walk the whole tree.
			var files []string
			err = fs.WalkDir(u, ".", func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					files = append(files, p)
				}
				return nil
			})
			if err != nil || len(files) != 155 {
				t.Errorf("walk: %d files, %v", len(files), err)
			}
			// fs.FS conformance (expects exact-case names, which it gets).
			if err := fstest.TestFS(u, "EFI/Boot/bootx64.efi", "sources/big.bin", "Deep/a/b/c/d/leaf.txt"); err != nil {
				t.Error(err)
			}
			fi, _ := fs.Stat(u, "sources/big.bin")
			if fi.ModTime().Before(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)) || fi.Mode() != 0o444 {
				t.Errorf("big.bin info: %v %v", fi.ModTime(), fi.Mode())
			}
		})
	}
}

func TestReaderAtAcrossFile(t *testing.T) {
	u := open(t, "udf102")
	f, err := u.Open("sources/big.bin")
	if err != nil {
		t.Fatal(err)
	}
	ra := f.(io.ReaderAt)
	want := pattern(200<<10 + 5)
	buf := make([]byte, 5000)
	for _, off := range []int64{0, 2047, 2048, 100_000, int64(len(want)) - 5000} {
		if n, err := ra.ReadAt(buf, off); n != len(buf) || err != nil || !bytes.Equal(buf, want[off:off+5000]) {
			t.Errorf("ReadAt(%d): %d %v", off, n, err)
		}
	}
	if n, err := ra.ReadAt(buf, int64(len(want))-10); n != 10 || err != io.EOF {
		t.Errorf("ReadAt past end: %d %v", n, err)
	}
}

func TestRefusals(t *testing.T) {
	r := image(t, "udf250")
	if _, err := Open(r, r.Size()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("UDF 2.50: %v, want ErrUnsupported", err)
	}
	// ISO9660 only: a primary volume descriptor and nothing else.
	iso := make([]byte, 300*SectorSize)
	copy(iso[16*SectorSize:], "\x01CD001\x01")
	copy(iso[17*SectorSize:], "\xffCD001\x01")
	if _, err := Open(bytes.NewReader(iso), int64(len(iso))); !errors.Is(err, ErrNotUDF) {
		t.Errorf("ISO9660 only: %v, want ErrNotUDF", err)
	}
	if _, err := Open(bytes.NewReader(nil), 0); !errors.Is(err, ErrNotUDF) {
		t.Errorf("empty: %v", err)
	}
}

// Corrupting metadata bytes must give an error or a readable tree, never
// a panic or a hang. Flips bytes in every sector holding a descriptor.
func TestCorruptionDoesNotPanic(t *testing.T) {
	orig := image(t, "udf201")
	data := make([]byte, orig.Size())
	orig.ReadAt(data, 0)
	// Every volume-level descriptor, and a sample of the file entries and
	// directories (they share their parsing code).
	var sectors []int
	entries := 0
	for s := 16; s < len(data)/SectorSize; s++ {
		b := data[s*SectorSize:]
		if string(b[1:6]) == "BEA01" || string(b[1:6]) == "NSR03" {
			sectors = append(sectors, s)
			continue
		}
		id, err := checkTag(b, le32(b[12:]))
		if err != nil {
			continue
		}
		if id <= tagFSD || entries%15 == 0 {
			sectors = append(sectors, s)
		}
		if id > tagFSD {
			entries++
		}
	}
	if len(sectors) < 20 {
		t.Fatalf("only %d metadata sectors found", len(sectors))
	}
	// Keep the run to a few seconds: the volume descriptors come first in
	// the image, then an even sample of the rest.
	if limit := 60; len(sectors) > limit {
		head, rest := sectors[:20], sectors[20:]
		step := len(rest) / (limit - 20)
		sampled := append([]int(nil), head...)
		for i := 0; i < len(rest); i += step {
			sampled = append(sampled, rest[i])
		}
		sectors = sampled
	}
	flips := 0
	for _, s := range sectors {
		for i := s * SectorSize; i < s*SectorSize+512; i += 5 {
			data[i] ^= 0xa5
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("panic with byte %d flipped: %v", i, p)
					}
				}()
				u, err := Open(bytes.NewReader(data), int64(len(data)))
				if err != nil {
					return
				}
				fs.WalkDir(u, ".", func(p string, d fs.DirEntry, err error) error {
					if err == nil && !d.IsDir() {
						fs.ReadFile(u, p)
					}
					return nil
				})
			}()
			data[i] ^= 0xa5
			flips++
		}
	}
	t.Logf("%d metadata sectors, %d flips", len(sectors), flips)
}

func TestDString(t *testing.T) {
	if got := dstring([]byte{8, 'A', 0xe9}); got != "Aé" {
		t.Errorf("8-bit: %q", got)
	}
	if got := dstring([]byte{16, 0x65, 0x87, 0x4e, 0xf6}); got != "文件" {
		t.Errorf("16-bit: %q", got)
	}
	field := make([]byte, 32)
	copy(field, []byte{8, 'X', 'Y'})
	field[31] = 3
	if got := dstringField(field); got != "XY" {
		t.Errorf("field: %q", got)
	}
}

// TestRealISO reads real install media when NETDEPLOY_TEST_ISO names one,
// e.g. a Windows 11 ISO. Not run in CI.
func TestRealISO(t *testing.T) {
	p := os.Getenv("NETDEPLOY_TEST_ISO")
	if p == "" {
		t.Skip("set NETDEPLOY_TEST_ISO to a Windows ISO")
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	start := time.Now()
	u, err := Open(f, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	var files int
	var total int64
	var big []string
	fs.WalkDir(u, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Error(p, err)
			return nil
		}
		if !d.IsDir() {
			files++
			info, _ := d.Info()
			total += info.Size()
			if info.Size() > 1<<30 {
				big = append(big, fmt.Sprintf("%s (%d bytes)", p, info.Size()))
			}
		}
		return nil
	})
	sort.Strings(big)
	t.Logf("%s: label %q, %d files, %d bytes, walked in %v; over 1 GiB: %v", filepath.Base(p), u.Label(), files, total, time.Since(start), big)
	b, err := fs.ReadFile(u, "efi/boot/bootx64.efi")
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	t.Logf("efi/boot/bootx64.efi sha256 %s", hex.EncodeToString(s[:]))
	// The tail of a multi-extent file (install.wim is over 4 GiB).
	if w, err := u.Open("sources/install.wim"); err == nil {
		st, _ := w.Stat()
		tail := make([]byte, 4096)
		if n, err := w.(io.ReaderAt).ReadAt(tail, st.Size()-4096); n != 4096 || err != nil {
			t.Errorf("install.wim tail: %d %v", n, err)
		}
	}
}
