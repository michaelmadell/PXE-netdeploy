package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSealOpen(t *testing.T) {
	r, err := NewRing()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := r.Seal("s1", "password", 1, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("hunter2")) {
		t.Fatal("plaintext visible")
	}
	if p, err := r.Open("s1", "password", 1, sealed); err != nil || string(p) != "hunter2" {
		t.Fatalf("open: %q %v", p, err)
	}
	// Bound to id, kind and version.
	for _, c := range []struct {
		id, kind string
		v        int64
	}{{"s2", "password", 1}, {"s1", "product-key", 1}, {"s1", "password", 2}} {
		if _, err := r.Open(c.id, c.kind, c.v, sealed); !errors.Is(err, ErrDecrypt) {
			t.Errorf("%v: %v", c, err)
		}
	}
	other, _ := NewRing()
	if _, err := other.Open("s1", "password", 1, sealed); !errors.Is(err, ErrDecrypt) {
		t.Errorf("other key: %v", err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := r.Open("s1", "password", 1, sealed); !errors.Is(err, ErrDecrypt) {
		t.Errorf("tampered: %v", err)
	}
}

func TestKeyFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); !errors.Is(err, ErrNoKey) {
		t.Fatalf("empty dir: %v", err)
	}
	k := NewKeys(dir)
	if _, err := k.Ring(false); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no create: %v", err)
	}
	r, err := k.Ring(true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, FileName))
	if runtime.GOOS == "windows" && bytes.Contains(raw, r.key) {
		t.Error("raw key in a DPAPI key file")
	}
	if fi, _ := os.Stat(filepath.Join(dir, FileName)); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	r2, err := Load(dir)
	if err != nil || r2.Fingerprint() != r.Fingerprint() {
		t.Fatalf("reload: %v", err)
	}
	if err := r2.Save(dir, false); err == nil {
		t.Error("Save replaced an existing key")
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte("garbage"), 0o600)
	if _, err := Load(dir); err == nil {
		t.Error("garbage key file loaded")
	}
}

func TestExportImport(t *testing.T) {
	r, _ := NewRing()
	if _, err := r.Export([]byte("short")); err == nil {
		t.Error("short passphrase accepted")
	}
	b, err := r.Export([]byte("correct horse battery"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, r.key) {
		t.Fatal("raw key in export")
	}
	if _, err := Import(b, []byte("wrong passphrase!!")); !errors.Is(err, ErrPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	r2, err := Import(b, []byte("correct horse battery"))
	if err != nil || r2.Fingerprint() != r.Fingerprint() {
		t.Fatalf("import: %v", err)
	}
	if _, err := Import([]byte(`{"format":"x"}`), []byte("correct horse battery")); err == nil {
		t.Error("junk imported")
	}
}
