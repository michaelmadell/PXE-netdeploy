package bootimage

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/agentbin"
	"github.com/michaelmadell/pxe-netdeploy/internal/media/udf"
	"github.com/michaelmadell/pxe-netdeploy/internal/media/wim"
)

// TestRealTools runs the real builder on the boot.wim of a Windows ISO:
//
//	NETDEPLOY_TEST_ISO=C:\path\Windows-11.iso go test -run TestRealTools -v ./internal/bootimage
//
// It uses DISM when elevated on Windows, else wimlib-imagex (PATH or
// NETDEPLOY_TEST_WIMLIB); NETDEPLOY_TEST_BUILDER=dism|wimlib forces one.
// NETDEPLOY_TEST_AGENT puts a real agent.exe in the image (default: a
// placeholder), and NETDEPLOY_TEST_OUT writes the boot files, with the BCD
// from internal/bcd, there as a TFTP root for a lab boot.
func TestRealTools(t *testing.T) {
	iso := os.Getenv("NETDEPLOY_TEST_ISO")
	if iso == "" {
		t.Skip("NETDEPLOY_TEST_ISO not set")
	}
	ctx := context.Background()
	h := Detect(ctx, os.Getenv("NETDEPLOY_TEST_WIMLIB"))
	name := os.Getenv("NETDEPLOY_TEST_BUILDER")
	if name == "" {
		name = BuilderWimlib
		if h.DISM.Available {
			name = BuilderDISM
		}
	}
	if ts := h.Tool(name); !ts.Available {
		t.Skipf("%s unavailable: %s", name, ts.Reason)
	}
	t.Logf("builder %s %s (%s)", name, h.Tool(name).Version, h.Tool(name).Path)

	dir := t.TempDir()
	src := filepath.Join(dir, "source-boot.wim")
	f, err := os.Open(iso)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	u, err := udf.Open(f, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	in, err := u.Open("sources/boot.wim")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.Create(src)
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	out.Close()

	tool := newTool(name, h)
	work := filepath.Join(dir, "work")
	add := filepath.Join(work, "add")
	os.MkdirAll(filepath.Join(add, agentDir), 0o700)
	agentBytes := []byte("MZ test agent")
	if p := os.Getenv("NETDEPLOY_TEST_AGENT"); p != "" {
		if agentBytes, err = os.ReadFile(p); err != nil {
			t.Fatal(err)
		}
	}
	agent := agentbin.ForTest("test", map[string][]byte{"x64": agentBytes})
	a, _ := agent.Get("x64")
	if err := writeAgentFiles(add, a, AgentConfig{ServerURLs: []string{"http://10.77.0.1:8080"}, InputHash: "test"}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	exported := filepath.Join(work, "export.wim")
	if err := tool.Export(ctx, src, 1, exported); err != nil {
		t.Fatal(err)
	}
	t.Logf("export: %s", time.Since(start).Round(time.Millisecond))
	start = time.Now()
	built := filepath.Join(work, "boot.wim")
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := tool.Customize(ctx, Customization{WIM: exported, Out: built, Work: work, Files: add, Log: quiet}); err != nil {
		t.Fatal(err)
	}
	bi, _ := os.Stat(built)
	t.Logf("customize: %s, boot.wim %d bytes", time.Since(start).Round(time.Millisecond), bi.Size())

	svc := New(Options{Logger: quiet})
	bf, err := svc.readBootFiles(built, "auto")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("boot manager %s (%s, %d bytes), boot.sdi %d bytes, %d fonts", bf.signer, bf.source, len(bf.bootmgr), len(bf.sdi), len(bf.fonts))
	if bf.signer != "windows-uefi-ca-2023" {
		t.Errorf("auto chose %s", bf.signer)
	}
	bw, _ := os.Open(built)
	defer bw.Close()
	info, err := wim.ReadInfo(bw)
	if err != nil || len(info.Images) != 1 || info.BootIndex != 1 || info.Compression != "lzx" {
		t.Fatalf("built WIM: %+v %v", info, err)
	}
	rd, err := wim.Open(bw, bi.Size())
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{`netdeploy\agent.exe`: string(agentBytes), winpeshlPath: winpeshl} {
		b, err := rd.ReadFile(1, p)
		if err != nil || string(b) != want {
			t.Errorf("%s: %q %v", p, b, err)
		}
	}
	if _, err := rd.ReadFile(1, `netdeploy\config.json`); err != nil {
		t.Error(err)
	}

	if out := os.Getenv("NETDEPLOY_TEST_OUT"); out != "" {
		bw.Close()
		bcdBytes, err := builtinBCD{}.WriteBCD(ctx, work, BCDOptions{Architecture: "x64", TFTPBlockSize: DefaultTFTPBlockSize, TFTPWindowSize: DefaultTFTPWindowSize})
		if err != nil {
			t.Fatal(err)
		}
		files := append([]treeFile{{TreeBootmgr, bf.bootmgr}, {TreeSDI, bf.sdi}, {TreeBCD, bcdBytes}}, bf.fonts...)
		for _, f := range files {
			p := filepath.Join(out, filepath.FromSlash(f.path))
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, f.data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := copyFile(built, filepath.Join(out, filepath.FromSlash(TreeWIM))); err != nil {
			t.Fatal(err)
		}
		t.Logf("boot files written to %s", out)
	}
}
