package ipxe

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSet(t *testing.T, dir, arch string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, arch), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, arch, n), []byte(arch+"/"+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	writeSet(t, dir, "x64", Bundled...)
	writeSet(t, dir, "arm64", Shim, Loader) // no wimboot: left out
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Archs(); len(got) != 1 || got[0] != "x64" || b.Reason != "" {
		t.Fatalf("archs %v, reason %q", got, b.Reason)
	}
	if !b.Has("x64") || b.Has("arm64") || len(b.Files()) != 3 {
		t.Errorf("has x64 %v, arm64 %v, files %d", b.Has("x64"), b.Has("arm64"), len(b.Files()))
	}
	f, err := b.Open(ShimPath("x64"))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	if string(data) != "x64/ipxe-shim.efi" {
		t.Errorf("shim %q", data)
	}
	if _, ok := f.(io.ReadSeeker); !ok {
		t.Error("not seekable")
	}
	s, err := b.Open("ipxe/x64/autoexec.ipxe")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(s)
	for _, want := range []string{"#!ipxe", "kernel wimboot", "initrd /content/boot/Boot/boot.wim boot.wim", "exit 1"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("script lacks %q", want)
		}
	}
	if strings.Contains(string(data), "shell") {
		t.Error("script mustn't drop to a shell")
	}
	for _, p := range []string{"ipxe/arm64/ipxe-shim.efi", "ipxe/x64/../x64/ipxe.efi", "bootmgfw.efi"} {
		if _, err := b.Open(p); err == nil {
			t.Errorf("opened %s", p)
		}
	}
}

func TestLoadMissing(t *testing.T) {
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "none")} {
		b, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(b.Archs()) != 0 || b.Reason == "" {
			t.Errorf("%q: archs %v reason %q", dir, b.Archs(), b.Reason)
		}
	}
	var nilB *Bundle
	if nilB.Has("x64") || nilB.Archs() != nil {
		t.Error("nil bundle")
	}
	if _, err := nilB.Open("ipxe/x64/ipxe.efi"); err == nil {
		t.Error("nil bundle opened a file")
	}
}
