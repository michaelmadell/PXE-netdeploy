package wim

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf16"

	"github.com/michaelmadell/pxe-netdeploy/internal/media/udf"
	gowim "github.com/nradchenko/go-wim"
)

// A minimal self-relative security descriptor (no owner, no DACL).
var sd = []byte{1, 0, 0x04, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}

type seekBuf struct {
	bytes.Buffer
	pos int64
}

// capture writes a two-image LZX WIM with go-wim.
func capture(t *testing.T) []byte {
	t.Helper()
	var out writeSeeker
	w := gowim.NewWriter(&out, gowim.Options{Security: gowim.UniformSecurity(sd), Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	pe := fstest.MapFS{
		"Windows/Boot/EFI/bootmgfw.efi":       {Data: []byte("pca2011 boot manager")},
		"Windows/Boot/EFI_EX/bootmgfw_EX.efi": {Data: bytes.Repeat([]byte("2023"), 50_000)},
	}
	setup := fstest.MapFS{"sources/setup.exe": {Data: []byte("setup")}}
	info := func(name string, boot bool) gowim.ImageInfo {
		return gowim.ImageInfo{Name: name, Boot: boot, Windows: &gowim.WindowsInfo{Arch: 9, Version: gowim.Version{Major: 10, Build: 26100, SPBuild: 9457}}}
	}
	if err := w.AddImage(context.Background(), pe, info("Microsoft Windows PE (amd64)", false)); err != nil {
		t.Fatal(err)
	}
	if err := w.AddImage(context.Background(), setup, info("Microsoft Windows Setup (amd64)", true)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.b
}

type writeSeeker struct {
	b   []byte
	pos int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	if end := w.pos + int64(len(p)); end > int64(len(w.b)) {
		w.b = append(w.b, make([]byte, end-int64(len(w.b)))...)
	}
	copy(w.b[w.pos:], p)
	w.pos += int64(len(p))
	return len(p), nil
}

func (w *writeSeeker) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		w.pos = off
	case io.SeekCurrent:
		w.pos += off
	case io.SeekEnd:
		w.pos = int64(len(w.b)) + off
	}
	return w.pos, nil
}

func TestInfoAndReadFile(t *testing.T) {
	b := capture(t)
	info, err := ReadInfo(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if info.Compression != "lzx" || len(info.Images) != 2 || info.BootIndex != 2 {
		t.Fatalf("info: %+v", info)
	}
	im := info.Images[0]
	if im.Index != 1 || im.Name != "Microsoft Windows PE (amd64)" || im.Arch != "x64" || im.Build != "26100.9457" {
		t.Errorf("image 1: %+v", im)
	}
	r, err := Open(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadFile(1, `\windows\boot\efi_ex\BOOTMGFW_EX.EFI`)
	if err != nil || !bytes.Equal(got, bytes.Repeat([]byte("2023"), 50_000)) {
		t.Fatalf("read: %d bytes, %v", len(got), err)
	}
	if _, err := r.ReadFile(2, "Windows/Boot/EFI_EX/bootmgfw_EX.efi"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: %v", err)
	}
	if _, err := r.ReadFile(3, "x"); err == nil {
		t.Error("image 3 opened")
	}
}

// fakeWIM builds a header plus an XML resource, as ESD and split WIMs carry
// (no file data needed for ReadInfo).
func fakeWIM(flags uint32, parts [2]uint16, xmlText string) []byte {
	u := utf16.Encode([]rune("\uFEFF" + xmlText))
	x := make([]byte, 2*len(u))
	for i, v := range u {
		binary.LittleEndian.PutUint16(x[2*i:], v)
	}
	h := make([]byte, 208)
	copy(h, "MSWIM\x00\x00\x00")
	binary.LittleEndian.PutUint32(h[8:], 208)
	binary.LittleEndian.PutUint32(h[12:], 0xe00)
	binary.LittleEndian.PutUint32(h[16:], flags)
	binary.LittleEndian.PutUint16(h[40:], parts[0])
	binary.LittleEndian.PutUint16(h[42:], parts[1])
	binary.LittleEndian.PutUint32(h[44:], 2)
	binary.LittleEndian.PutUint64(h[72:], uint64(len(x)))
	binary.LittleEndian.PutUint64(h[80:], 208)
	binary.LittleEndian.PutUint64(h[88:], uint64(len(x)))
	return append(h, x...)
}

const editionsXML = `<WIM><TOTALBYTES>1</TOTALBYTES>
<IMAGE INDEX="2"><NAME>Windows 11 Pro</NAME><DISPLAYNAME>Windows 11 Pro</DISPLAYNAME><TOTALBYTES>26800000000</TOTALBYTES>
<WINDOWS><ARCH>12</ARCH><EDITIONID>Professional</EDITIONID><VERSION><BUILD>26300</BUILD><SPBUILD>9457</SPBUILD></VERSION>
<LANGUAGES><LANGUAGE>en-GB</LANGUAGE><LANGUAGE>cy-GB</LANGUAGE><DEFAULT>en-GB</DEFAULT></LANGUAGES></WINDOWS></IMAGE>
<IMAGE INDEX="1"><NAME>Windows 11 Education</NAME><WINDOWS><ARCH>9</ARCH><EDITIONID>Education</EDITIONID></WINDOWS></IMAGE></WIM>`

func TestInfoFromESDAndSplit(t *testing.T) {
	esd := fakeWIM(flagCompression|flagLZMS, [2]uint16{1, 1}, editionsXML)
	info, err := ReadInfo(bytes.NewReader(esd))
	if err != nil {
		t.Fatal(err)
	}
	if info.Compression != "lzms" || len(info.Images) != 2 {
		t.Fatalf("esd: %+v", info)
	}
	pro := info.Images[1]
	if pro.Index != 2 || pro.EditionID != "Professional" || pro.Arch != "arm64" || pro.Build != "26300.9457" ||
		len(pro.Languages) != 2 || pro.DefaultLanguage != "en-GB" || pro.TotalBytes != 26800000000 || pro.DisplayName != "Windows 11 Pro" {
		t.Errorf("pro: %+v", pro)
	}
	if _, err := Open(bytes.NewReader(esd), int64(len(esd))); !errors.Is(err, ErrUnsupported) {
		t.Errorf("open esd: %v", err)
	}
	swm := fakeWIM(flagCompression|flagLZX, [2]uint16{1, 3}, editionsXML)
	if info, err := ReadInfo(bytes.NewReader(swm)); err != nil || info.TotalParts != 3 {
		t.Errorf("swm: %+v %v", info, err)
	}
	if _, err := Open(bytes.NewReader(swm), int64(len(swm))); !errors.Is(err, ErrUnsupported) {
		t.Errorf("open swm: %v", err)
	}
}

func TestMalformed(t *testing.T) {
	cases := map[string][]byte{
		"empty":     nil,
		"not a wim": bytes.Repeat([]byte("x"), 300),
		"bad xml":   fakeWIM(0, [2]uint16{1, 1}, "<WIM><IMAGE"),
		"count":     fakeWIM(0, [2]uint16{1, 1}, `<WIM><IMAGE INDEX="1"></IMAGE></WIM>`),
		"bad index": fakeWIM(0, [2]uint16{1, 1}, `<WIM><IMAGE INDEX="1"></IMAGE><IMAGE INDEX="7"></IMAGE></WIM>`),
	}
	for name, b := range cases {
		if _, err := ReadInfo(bytes.NewReader(b)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// XML resource pointing far beyond the file.
	b := fakeWIM(0, [2]uint16{1, 1}, editionsXML)
	binary.LittleEndian.PutUint64(b[72:], 1<<40)
	if _, err := ReadInfo(bytes.NewReader(b)); !errors.Is(err, ErrCorrupt) {
		t.Errorf("huge XML: %v", err)
	}
}

// TestRealISO reads boot.wim and install.wim in place from real media when
// NETDEPLOY_TEST_ISO is set.
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
	st, _ := f.Stat()
	u, err := udf.Open(f, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sources/boot.wim", "sources/install.wim"} {
		file, err := u.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		fi, _ := file.Stat()
		ra := file.(io.ReaderAt)
		info, err := ReadInfo(ra)
		if err != nil {
			t.Fatal(name, err)
		}
		t.Logf("%s: %s, %d images", name, info.Compression, len(info.Images))
		for _, im := range info.Images {
			t.Logf("  %d %q %s %s %s %v", im.Index, im.DisplayName, im.EditionID, im.Arch, im.Build, im.Languages)
		}
		if name == "sources/boot.wim" {
			r, err := Open(ra, fi.Size())
			if err != nil {
				t.Fatal(err)
			}
			b, err := r.ReadFile(2, "Windows/Boot/EFI_EX/bootmgfw_EX.efi")
			t.Logf("  bootmgfw_EX.efi: %d bytes, %v", len(b), err)
		}
	}
}
