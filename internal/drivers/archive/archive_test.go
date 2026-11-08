package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

var (
	wantINF = "[Version]\r\nSignature=\"$Windows NT$\"\r\n"
	wantSys = func() []byte {
		b := make([]byte, 100<<10)
		for i := range b {
			b[i] = byte(i * 13 % 251)
		}
		return b
	}()
)

func collect(t *testing.T, path string, opt Options) (map[string][]byte, error) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]byte{}
	err = Extract(context.Background(), path, fi.Size(), opt, func(name string, r io.Reader) error {
		b, err := io.ReadAll(r)
		got[name] = b
		return err
	})
	return got, err
}

func checkTree(t *testing.T, name string, got map[string][]byte) {
	t.Helper()
	if len(got) != 2 || string(got["drv/test.inf"]) != wantINF || !bytes.Equal(got["drv/x64/test.sys"], wantSys) {
		names := []string{}
		for k := range got {
			names = append(names, k)
		}
		t.Errorf("%s: got %v", name, names)
	}
}

func writeZip(t *testing.T, files map[string][]byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.zip")
	f, _ := os.Create(p)
	z := zip.NewWriter(f)
	for name, b := range files {
		w, _ := z.Create(name)
		w.Write(b)
	}
	z.Create("drv/emptydir/")
	z.Close()
	f.Close()
	return p
}

func TestFormats(t *testing.T) {
	opt := Options{Limits: DefaultLimits(1 << 20), TempDir: t.TempDir()}
	got, err := collect(t, writeZip(t, map[string][]byte{"drv/test.inf": []byte(wantINF), "drv/x64/test.sys": wantSys}), opt)
	if err != nil {
		t.Fatal(err)
	}
	checkTree(t, "zip", got)
	for _, name := range []string{"test.7z", "test-mszip.cab"} {
		got, err := collect(t, filepath.Join("testdata", name), opt)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		checkTree(t, name, got)
	}
}

func TestLZXCab(t *testing.T) {
	tool, err := LookupLZXTool()
	if err != nil {
		t.Skip(err)
	}
	t.Logf("using %s", tool.Path)
	got, err := collect(t, filepath.Join("testdata", "test-lzx.cab"), Options{Limits: DefaultLimits(1 << 20), TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	checkTree(t, "lzx cab", got)
	// Without a tool, LZX CABs are refused clearly.
	_, err = collect(t, filepath.Join("testdata", "test-lzx.cab"),
		Options{TempDir: t.TempDir(), LZXTool: &Tool{Name: "cabextract", Path: filepath.Join(t.TempDir(), "missing")}})
	if !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrUnsupported) {
		t.Errorf("broken tool: %v", err)
	}
}

func TestLimits(t *testing.T) {
	bomb := writeZip(t, map[string][]byte{"zeros.bin": make([]byte, 10<<20)})
	if _, err := collect(t, bomb, Options{Limits: Limits{MaxBytes: 1 << 20}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("zip bomb: %v", err)
	}
	many := map[string][]byte{}
	for i := range 20 {
		many[string(rune('a'+i))+".txt"] = []byte("x")
	}
	if _, err := collect(t, writeZip(t, many), Options{Limits: Limits{MaxFiles: 10}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("too many files: %v", err)
	}
	// A CAB that declares more than the limit is refused before extracting.
	if _, err := collect(t, filepath.Join("testdata", "test-lzx.cab"), Options{Limits: Limits{MaxBytes: 1000}, TempDir: t.TempDir()}); !errors.Is(err, ErrInvalid) {
		t.Errorf("cab declared size: %v", err)
	}
}

func TestDetect(t *testing.T) {
	cases := map[string]error{
		"MZ\x90\x00":                       ErrInstaller,
		"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1": ErrInstaller, // .msi
		"Rar!\x1a\x07":                     ErrUnsupported,
		"":                                 ErrUnsupported,
	}
	for head, want := range cases {
		if _, err := Detect([]byte(head)); !errors.Is(err, want) {
			t.Errorf("%q: %v, want %v", head, err, want)
		}
	}
	for head, want := range map[string]Format{"PK\x03\x04": Zip, "7z\xbc\xaf\x27\x1c": SevenZip, "MSCF\x00": Cab} {
		if f, err := Detect([]byte(head)); err != nil || f != want {
			t.Errorf("%q: %s %v", head, f, err)
		}
	}
	// Truncated or corrupt archives are invalid, not crashes.
	for _, name := range []string{"test.7z", "test-mszip.cab"} {
		b, _ := os.ReadFile(filepath.Join("testdata", name))
		p := filepath.Join(t.TempDir(), name)
		os.WriteFile(p, b[:len(b)/2], 0o600)
		if _, err := collect(t, p, Options{Limits: DefaultLimits(1 << 20), TempDir: t.TempDir()}); err == nil {
			t.Errorf("truncated %s extracted", name)
		}
	}
}
