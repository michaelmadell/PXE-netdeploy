package imports

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/drivers/archive"
	"github.com/michaelmadell/pxe-netdeploy/internal/drivers/catalog"
	"github.com/michaelmadell/pxe-netdeploy/internal/store"
)

const nicINF = "[Version]\r\nSignature=\"$WINDOWS NT$\"\r\nClass=Net\r\nClassGuid={4d36e972-e325-11ce-bfc1-08002be10318}\r\n" +
	"Provider=%Mfg%\r\nDriverVer=01/02/2024,1.2.3.4\r\nCatalogFile=nic.cat\r\n\r\n" +
	"[Manufacturer]\r\n%Mfg%=Models,NTamd64\r\n\r\n[Models.NTamd64]\r\n%Dev%=Install, PCI\\VEN_8086&DEV_1234\r\n\r\n" +
	"[SourceDisksNames]\r\n1=%Disk%\r\n\r\n[SourceDisksFiles]\r\nnic.sys=1\r\n\r\n[Strings]\r\nMfg=\"Test Corp\"\r\nDev=\"Test NIC\"\r\nDisk=\"Disk\"\r\n"

// An x86-only storage driver: imported but disabled.
const oldINF = "[Version]\r\nSignature=\"$Windows NT$\"\r\nClass=SCSIAdapter\r\nProvider=Old\r\n\r\n" +
	"[Manufacturer]\r\nOld=Models\r\n\r\n[Models]\r\nOld Disk=Install, PCI\\VEN_1000&DEV_0001\r\n"

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (h *harness) importDriver(t *testing.T, u store.Upload, want string, timeout time.Duration) (store.DriverPackage, store.Job) {
	t.Helper()
	ctx := context.Background()
	pkg, job, err := h.st.CreateDriverImport(ctx, "test", u.ID, KindDriverImport)
	if err != nil {
		t.Fatal(err)
	}
	h.runner.Wake()
	deadline := time.Now().Add(timeout)
	for {
		j, _ := h.st.Job(ctx, job.ID)
		if j.Finished() {
			if j.Status != want {
				t.Fatalf("job %s: %s, want %s (error %s)", j.ID, j.Status, want, j.Error)
			}
			pkg, _ = h.st.DriverPackage(ctx, pkg.ID)
			return pkg, j
		}
		if time.Now().After(deadline) {
			t.Fatalf("job still %s", j.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDriverImport(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u := h.upload(t, "nic.zip", zipOf(t, map[string]string{
		"Nic/nic.inf":     nicINF,
		"Nic/NIC.SYS":     "not really a driver",
		"Old/old.inf":     oldINF,
		"autorun.inf":     "[autorun]\r\nopen=setup.exe\r\n",
		"Docs/readme.txt": "hello",
	}))
	pkg, job := h.importDriver(t, u, store.JobSucceeded, 30*time.Second)
	if pkg.Status != "ready" || pkg.TreeID == nil || *pkg.SourceFilename != "nic.zip" || pkg.SHA256 != *u.SHA256 || job.Percent != 100 {
		t.Fatalf("package: %+v", pkg)
	}
	if job.Message != "Imported 2 INFs from 5 files" {
		t.Errorf("job message: %q", job.Message)
	}
	if len(pkg.INFs) != 2 {
		t.Fatalf("INFs: %+v", pkg.INFs)
	}
	byPath := map[string]store.DriverINF{}
	for _, i := range pkg.INFs {
		byPath[i.Path] = i
	}
	nic, old := byPath["Nic/nic.inf"], byPath["Old/old.inf"]
	if nic.Class != "Net" || nic.Role != "network" || !nic.Enabled || strings.Join(nic.Architectures, ",") != "x64" ||
		nic.Provider == nil || *nic.Provider != "Test Corp" || nic.Version == nil || *nic.Version != "1.2.3.4" ||
		nic.DriverDate == nil || *nic.DriverDate != "2024-01-02" ||
		len(nic.HardwareIDs) != 1 || nic.HardwareIDs[0] != `PCI\VEN_8086&DEV_1234` {
		t.Errorf("nic: %+v", nic)
	}
	// nic.cat isn't in the archive.
	if nic.Signature != catalog.Unsigned || nic.SignatureDetail == nil || !strings.Contains(*nic.SignatureDetail, "missing") {
		t.Errorf("nic signature: %s %v", nic.Signature, nic.SignatureDetail)
	}
	if old.Enabled || old.Role != "storage" || len(old.Architectures) != 0 || old.Signature != catalog.Unsigned {
		t.Errorf("old: %+v", old)
	}
	if len(pkg.Warnings) != 1 || !strings.Contains(pkg.Warnings[0], "Old/old.inf supports neither") {
		t.Errorf("warnings: %q", pkg.Warnings)
	}
	// The whole archive is in the tree (case-insensitively).
	tree := h.content.Tree(*pkg.TreeID)
	if b, err := fs.ReadFile(tree, "nic/nic.sys"); err != nil || string(b) != "not really a driver" {
		t.Errorf("tree: %q %v", b, err)
	}
	if b, _ := fs.ReadFile(tree, "Docs/readme.txt"); string(b) != "hello" {
		t.Error("tree: readme missing")
	}
	// The upload is kept until deleted.
	if u2, _ := h.st.Upload(ctx, u.ID); !u2.Imported || u2.InUse || u2.ExpiresAt != nil {
		t.Errorf("upload after import: %+v", u2)
	}
}

func TestDriverImportDuplicate(t *testing.T) {
	h := newHarness(t)
	b := zipOf(t, map[string]string{"a/nic.inf": nicINF})
	first, _ := h.importDriver(t, h.upload(t, "a.zip", b), store.JobSucceeded, 30*time.Second)
	_, _, err := h.st.CreateDriverImport(context.Background(), "again", h.upload(t, "b.zip", b).ID, KindDriverImport)
	var dup *store.DuplicatePackageError
	if !errorsAs(err, &dup) || dup.Package.ID != first.ID {
		t.Fatalf("second import: %v", err)
	}
}

func TestDriverImportFailures(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		body       []byte
	}{
		{"no-inf.zip", "no_drivers", nil}, // filled in below
		{"junk.bin", "unsupported_archive", bytes.Repeat([]byte("junk "), 1000)},
		{"setup.exe", "unsupported_archive", append([]byte("MZ"), make([]byte, 4096)...)},
		{"broken.zip", "invalid_archive", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			body := tc.body
			switch tc.name {
			case "no-inf.zip":
				body = zipOf(t, map[string]string{"readme.txt": "no drivers here", "autorun.inf": "[autorun]\r\n"})
			case "broken.zip":
				body = zipOf(t, map[string]string{"a/nic.inf": strings.Repeat(nicINF, 50)})
				body = body[:len(body)/2] // truncated: no central directory
			}
			u := h.upload(t, tc.name, body)
			pkg, job := h.importDriver(t, u, store.JobFailed, 30*time.Second)
			var p struct{ Code, Detail string }
			json.Unmarshal(job.Error, &p)
			if p.Code != tc.code || !strings.Contains(p.Detail, "kept until") {
				t.Errorf("job error: %s", job.Error)
			}
			if pkg.Status != "failed" || !strings.Contains(string(pkg.Error), tc.code) {
				t.Errorf("package: %+v", pkg)
			}
			if u2, _ := h.st.Upload(ctx, u.ID); u2.InUse || u2.Imported || u2.ExpiresAt == nil {
				t.Errorf("upload after failure: %+v", u2)
			}
			if n, _ := h.st.ContentBytes(ctx); n != 0 {
				t.Errorf("%d bytes stored by a failed import", n)
			}
		})
	}
}

// TestDriverImportLZX imports the LZX CAB fixture when this host can read it.
func TestDriverImportLZX(t *testing.T) {
	if _, err := archive.LookupLZXTool(); err != nil {
		t.Skip(err)
	}
	b, err := os.ReadFile(filepath.Join("..", "drivers", "archive", "testdata", "test-lzx.cab"))
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	pkg, _ := h.importDriver(t, h.upload(t, "test-lzx.cab", b), store.JobSucceeded, 60*time.Second)
	if len(pkg.INFs) != 1 || pkg.INFs[0].Enabled {
		t.Fatalf("INFs: %+v", pkg.INFs)
	}
	sys, err := fs.ReadFile(h.content.Tree(*pkg.TreeID), "drv/x64/test.sys")
	if err != nil || len(sys) != 100<<10 || sys[1000] != byte(1000*13%251) {
		t.Errorf("test.sys: %d bytes, %v", len(sys), err)
	}
}

// TestDriverStorePackage zips one signed driver from this machine's
// DriverStore (NETDEPLOY_TEST_DRIVERSTORE=1, Windows only) and imports it.
func TestDriverStorePackage(t *testing.T) {
	if os.Getenv("NETDEPLOY_TEST_DRIVERSTORE") == "" {
		t.Skip("set NETDEPLOY_TEST_DRIVERSTORE=1 on Windows")
	}
	root := filepath.Join(os.Getenv("SystemRoot"), "System32", "DriverStore", "FileRepository")
	dirs, _ := filepath.Glob(filepath.Join(root, "netrtwlane*"))
	if more, _ := filepath.Glob(filepath.Join(root, "*")); len(dirs) == 0 {
		dirs = more
	}
	h := newHarness(t)
	for _, dir := range dirs {
		cats, _ := filepath.Glob(filepath.Join(dir, "*.cat"))
		if len(cats) == 0 {
			continue
		}
		files := map[string]string{}
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if b, err := os.ReadFile(p); err == nil {
					rel, _ := filepath.Rel(dir, p)
					files[filepath.ToSlash(filepath.Join(filepath.Base(dir), rel))] = string(b)
				}
			}
			return nil
		})
		pkg, _ := h.importDriver(t, h.upload(t, filepath.Base(dir)+".zip", zipOf(t, files)), store.JobSucceeded, 2*time.Minute)
		for _, i := range pkg.INFs {
			t.Logf("%s: %s %s %v %s %v", i.Path, i.Class, i.Signature, i.Architectures, deref(i.Signer), deref(i.SignatureDetail))
			if i.Signature != catalog.WHQL {
				t.Errorf("%s: %s", i.Path, i.Signature)
			}
		}
		t.Logf("warnings: %q", pkg.Warnings)
		return
	}
	t.Skip("no driver with a catalog found")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
