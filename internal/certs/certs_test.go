package certs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRenewGenerated(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := loadOrCreate(dir, start); err != nil {
		t.Fatal(err)
	}
	first := readCert(filepath.Join(dir, certFile))

	// A year on: kept.
	if _, err := loadOrCreate(dir, start.AddDate(1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if c := readCert(filepath.Join(dir, certFile)); !c.Equal(first) {
		t.Fatal("renewed a year before expiry")
	}
	// 20 days before expiry: renewed, the old pair kept.
	near := first.NotAfter.Add(-20 * 24 * time.Hour)
	if _, err := loadOrCreate(dir, near); err != nil {
		t.Fatal(err)
	}
	c := readCert(filepath.Join(dir, certFile))
	if c.Equal(first) || !c.NotAfter.After(near.AddDate(1, 0, 0)) {
		t.Fatalf("not renewed: expires %v", c.NotAfter)
	}
	if old := readCert(filepath.Join(dir, certFile+".old")); old == nil || !old.Equal(first) {
		t.Fatal("old certificate not kept")
	}
	if _, err := os.Stat(filepath.Join(dir, keyFile+".old")); err != nil {
		t.Fatal("old key not kept")
	}
}

// A certificate someone put there themselves is never replaced.
func TestKeepCustom(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, certFile), filepath.Join(dir, keyFile)
	if err := generateNamed(certPath, keyPath, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "lab.example"); err != nil {
		t.Fatal(err)
	}
	custom := readCert(certPath)
	if _, err := loadOrCreate(dir, custom.NotAfter.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !readCert(certPath).Equal(custom) {
		t.Fatal("a custom certificate was replaced")
	}
}
