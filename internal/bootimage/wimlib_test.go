package bootimage

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	gowim "github.com/nradchenko/go-wim"

	"github.com/michaelmadell/pxe-netdeploy/internal/media/wim"
)

// TestWimlibSetupImage runs the real wimlib-imagex (PATH, or
// NETDEPLOY_TEST_WIMLIB) on a synthetic two-image boot.wim whose boot index
// is 2, as on install media: export index 2, add our files, export again.
// The result must be one bootable image that is still the Setup image (the
// WinPE-Setup package, ADR 0022 amendment) and holds the added files. CI runs
// it on Ubuntu with the distribution's wimtools (ADR 0030); skipped where
// wimlib isn't installed.
func TestWimlibSetupImage(t *testing.T) {
	ctx := context.Background()
	h := Detect(ctx, os.Getenv("NETDEPLOY_TEST_WIMLIB"))
	if !h.Wimlib.Available {
		t.Skipf("wimlib-imagex not available: %s", h.Wimlib.Reason)
	}
	t.Logf("wimlib %s at %s", h.Wimlib.Version, h.Wimlib.Path)
	dir := t.TempDir()

	src := filepath.Join(dir, "boot.wim")
	var out writeSeeker
	w := gowim.NewWriter(&out, gowim.Options{Security: gowim.UniformSecurity(sd), Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	for i, files := range []fs.FS{peFiles, setupFiles()} {
		info := gowim.ImageInfo{Name: fmt.Sprintf("PE %d", i+1), Boot: i == 1,
			Windows: &gowim.WindowsInfo{Arch: 9, Version: gowim.Version{Major: 10, Build: 26100, SPBuild: 9457}}}
		if err := w.AddImage(ctx, files, info); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, out.b, 0o644); err != nil {
		t.Fatal(err)
	}

	tool := newTool(BuilderWimlib, h)
	exported := filepath.Join(dir, "export.wim")
	if err := tool.Export(ctx, src, 2, exported); err != nil {
		t.Fatalf("export: %v", err)
	}
	add := filepath.Join(dir, "add")
	if err := os.MkdirAll(filepath.Join(add, "netdeploy"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(add, "netdeploy", "config.json"), []byte(`{"serverUrls":["https://10.77.0.1"]}`), 0o644)
	os.MkdirAll(filepath.Join(add, "Windows", "System32"), 0o755)
	os.WriteFile(filepath.Join(add, filepath.FromSlash(winpeshlPath)), []byte(winpeshl), 0o644)
	built := filepath.Join(dir, "built.wim")
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := tool.Customize(ctx, Customization{WIM: exported, Out: built, Work: dir, Files: add, Log: quiet}); err != nil {
		t.Fatalf("customize: %v", err)
	}

	f, err := os.Open(built)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	info, err := wim.ReadInfo(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Images) != 1 || info.BootIndex != 1 {
		t.Errorf("%d images, boot index %d; want 1 and 1", len(info.Images), info.BootIndex)
	}
	rd, err := wim.Open(f, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	if !hasSetupPackage(rd) {
		t.Error("the Setup image lost its WinPE-Setup package")
	}
	if b, err := rd.ReadFile(1, "netdeploy/config.json"); err != nil || string(b) != `{"serverUrls":["https://10.77.0.1"]}` {
		t.Errorf("config.json: %q %v", b, err)
	}
	if b, err := rd.ReadFile(1, winpeshlPath); err != nil || string(b) != winpeshl {
		t.Errorf("winpeshl.ini: %q %v", b, err)
	}
	if _, err := rd.ReadFile(1, wimBootmgr2023); err != nil {
		t.Errorf("boot manager: %v", err)
	}
}
