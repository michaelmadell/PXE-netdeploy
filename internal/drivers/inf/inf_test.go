package inf

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := []byte{0xff, 0xfe}
	for _, v := range u {
		b = binary.LittleEndian.AppendUint16(b, v)
	}
	return b
}

const i210 = `; Intel I210 (abridged, as vendors write them)
[Version]
Signature   = "$WINDOWS NT$"
Class       = Net
ClassGUID   = {4d36e972-e325-11ce-bfc1-08002be10318}
Provider    = %Intel%
CatalogFile = e1r68x64.cat
DriverVer   = 06/21/2024,13.0.14.0

[Manufacturer]
%Intel%     = Intel, NTamd64.10.0...16299, NTamd64.10.0...22000, NTx86

[Intel.NTamd64.10.0...16299]
%E1533NC.DeviceDesc% = E1533.10.0.1.16299, PCI\VEN_8086&DEV_1533, \
                       PCI\VEN_8086&DEV_1533&SUBSYS_00008086
[Intel.NTamd64.10.0...22000]
%E1533NC.DeviceDesc% = E1533.10.0.1.22000, PCI\VEN_8086&DEV_1533
%E157BNC.DeviceDesc% = E157B.10.0.1.22000, PCI\VEN_8086&DEV_157B ; comment, with a comma
[Intel.NTx86]
%E1533NC.DeviceDesc% = E1533, PCI\VEN_8086&DEV_1533&SUBSYS_X86ONLY

[SourceDisksNames.amd64]
1 = %DiskName%,,,

[SourceDisksFiles.amd64]
e1r68x64.sys = 1,,
e1rmsg.dll   = 1,sub

[Strings]
Intel              = "Intel"
E1533NC.DeviceDesc = "Intel(R) Ethernet Controller I210, ""Gigabit"""
DiskName           = "Intel; Driver Disk"
[Strings.0409]
Intel              = "Intel Corporation"
`

func TestReadUTF16(t *testing.T) {
	d, f, err := Read(utf16le(i210))
	if err != nil {
		t.Fatal(err)
	}
	if d.Class != "Net" || d.Provider != "Intel Corporation" || d.Version != "13.0.14.0" ||
		d.Date == nil || !d.Date.Equal(time.Date(2024, 6, 21, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("version: %+v", d)
	}
	if !slices.Equal(d.Architectures, []string{"x64"}) || !slices.Equal(d.TargetOSVersions, []string{"10.0...16299", "10.0...22000"}) {
		t.Errorf("archs %v, os %v", d.Architectures, d.TargetOSVersions)
	}
	want := []string{`PCI\VEN_8086&DEV_1533`, `PCI\VEN_8086&DEV_1533&SUBSYS_00008086`, `PCI\VEN_8086&DEV_157B`}
	if !slices.Equal(d.HardwareIDs, want) {
		t.Errorf("hardware IDs %q", d.HardwareIDs)
	}
	if d.Catalogs["x64"] != "e1r68x64.cat" || d.Catalogs["arm64"] != "e1r68x64.cat" {
		t.Errorf("catalogs %v", d.Catalogs)
	}
	if !slices.Equal(d.Files, []string{"e1r68x64.sys", "sub/e1rmsg.dll"}) {
		t.Errorf("files %v", d.Files)
	}
	// Quoting: "" inside quotes, and a ; inside quotes isn't a comment.
	if got := f.Value("Strings", "E1533NC.DeviceDesc"); got != `Intel(R) Ethernet Controller I210, "Gigabit"` {
		t.Errorf("quoted string %q", got)
	}
	if got := f.Value("Strings", "DiskName"); got != "Intel; Driver Disk" {
		t.Errorf("quoted semicolon %q", got)
	}
}

func TestArchitectures(t *testing.T) {
	cases := map[string][]string{
		"%M% = M, NTamd64, NTarm64.10.0...22000": {"arm64", "x64"},
		"%M% = M, NTx86":                         nil,
		"%M% = M":                                nil, // undecorated: x86 only
		"%M% = M, NT":                            nil,
		"%M% = M, ntAMD64":                       {"x64"},
	}
	for mfg, want := range cases {
		text := "[Version]\nSignature=\"$Windows NT$\"\nCatalogFile.NTarm64=arm.cat\n[Manufacturer]\n" + mfg +
			"\n[M.NTamd64]\nx = s, ID1\n[M.NTarm64.10.0...22000]\nx = s, ID2\n[M]\nx = s, ID3\n[Strings]\nM=\"Maker\"\n"
		d, _, err := Read([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(d.Architectures, want) {
			t.Errorf("%s: %v, want %v", mfg, d.Architectures, want)
		}
		if slices.Contains(d.HardwareIDs, "ID3") {
			t.Errorf("%s: took IDs from the undecorated section", mfg)
		}
		if d.Catalogs["arm64"] != "arm.cat" || d.Catalogs["x64"] != "" {
			t.Errorf("%s: catalogs %v", mfg, d.Catalogs)
		}
	}
}

func TestEncodings(t *testing.T) {
	ansi := []byte("[Version]\r\nSignature=\"$Chicago$\"\r\nProvider=Acme\xae\r\n")
	d, _, err := Read(ansi)
	if err != nil || d.Provider != "Acme®" {
		t.Errorf("ansi: %q %v", d.Provider, err)
	}
	bom := append([]byte{0xef, 0xbb, 0xbf}, []byte("[Version]\nSignature=$Windows NT$\nProvider=Ünï\n")...)
	if d, _, err := Read(bom); err != nil || d.Provider != "Ünï" {
		t.Errorf("utf8 bom: %q %v", d.Provider, err)
	}
	if _, _, err := Read([]byte("[Strings]\nx=y\n")); !errors.Is(err, ErrNotINF) {
		t.Errorf("no signature: %v", err)
	}
	if _, _, err := Read([]byte("\x00\x01binary junk")); !errors.Is(err, ErrNotINF) {
		t.Errorf("junk: %v", err)
	}
}

// TestDriverStore parses every INF in a Windows DriverStore when
// NETDEPLOY_TEST_DRIVERSTORE is set (e.g. to
// C:\Windows\System32\DriverStore\FileRepository).
func TestDriverStore(t *testing.T) {
	root := os.Getenv("NETDEPLOY_TEST_DRIVERSTORE")
	if root == "" {
		t.Skip("set NETDEPLOY_TEST_DRIVERSTORE to a DriverStore FileRepository")
	}
	infs, _ := filepath.Glob(filepath.Join(root, "*", "*.inf"))
	var parsed, x64, arm64, ids, catalogs, files int
	var missing []string
	for _, p := range infs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		d, _, err := Read(b)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		parsed++
		if slices.Contains(d.Architectures, "x64") {
			x64++
		}
		if slices.Contains(d.Architectures, "arm64") {
			arm64++
		}
		ids += len(d.HardwareIDs)
		if d.Catalogs["x64"] != "" {
			catalogs++
		}
		// Every file the INF lists should be next to it in the store.
		for _, f := range d.Files {
			files++
			if _, err := os.Stat(filepath.Join(filepath.Dir(p), filepath.FromSlash(f))); err != nil && len(missing) < 5 {
				missing = append(missing, filepath.Base(filepath.Dir(p))+"/"+f)
			}
		}
	}
	t.Logf("%d INFs: %d x64, %d arm64, %d hardware IDs, %d with an x64 catalog, %d listed files", parsed, x64, arm64, ids, catalogs, files)
	if len(missing) > 0 {
		t.Logf("listed but not present (first 5): %s", strings.Join(missing, ", "))
	}
}
