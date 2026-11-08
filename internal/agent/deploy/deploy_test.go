package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michaelmadell/pxe-netdeploy/internal/agent/client"
	"github.com/michaelmadell/pxe-netdeploy/internal/agent/disk"
	"github.com/michaelmadell/pxe-netdeploy/internal/agent/wire"
)

type fakeRunner struct {
	cmds []string
	fail map[string]bool // command name → fail
	// onDism creates the applied Windows tree on dism /Apply-Image.
	onApply func()
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	cmd := filepath.Base(name) + " " + strings.Join(args, " ")
	f.cmds = append(f.cmds, cmd)
	if strings.Contains(cmd, "/Apply-Image") && f.onApply != nil {
		f.onApply()
	}
	for k := range f.fail {
		if strings.Contains(cmd, k) {
			return "boom", errFake
		}
	}
	return "ok", nil
}

var errFake = io.ErrUnexpectedEOF

func file(t *testing.T, data string) (string, wire.File) {
	sum := sha256.Sum256([]byte(data))
	sha := hex.EncodeToString(sum[:])
	return sha, wire.File{SHA256: sha, SizeBytes: int64(len(data))}
}

func TestInstall(t *testing.T) {
	files := map[string]string{}
	add := func(data string) wire.File {
		sha, f := file(t, data)
		files[sha] = data
		f.ContentURL = "/api/v1/agent/files/" + sha
		return f
	}
	img := add("WIM")
	img.ContentURL += "?name=install.esd"
	inf := add("[Version]")
	inf.Path = "1/e1r.inf"
	sys := add("sys")
	sys.Path = "1/sub/e1r.sys"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/unattend" {
			w.Write([]byte("<unattend/>"))
			return
		}
		w.Write([]byte(files[filepath.Base(r.URL.Path)]))
	}))
	defer srv.Close()
	c, _ := client.New(srv.URL)

	root := t.TempDir()
	drive := func(l string) string { return filepath.Join(root, l) }
	run := &fakeRunner{fail: map[string]bool{}, onApply: func() {
		re := filepath.Join(drive("W"), "Windows", "System32", "Recovery")
		os.MkdirAll(re, 0o755)
		os.WriteFile(filepath.Join(re, "Winre.wim"), []byte("re"), 0o644)
	}}
	var stages []string
	unattend := "/unattend"
	plan := wire.Plan{InstallMethod: "agent", Image: img, EditionIndex: 6, OSDrivers: []wire.File{inf, sys},
		UnattendURL: &unattend, BootManager: "windows-uefi-ca-2023"}
	e := Env{Client: c, Run: run, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Work: t.TempDir(), Drive: drive,
		Report: func(stage string, pct int, msg string) {
			if len(stages) == 0 || stages[len(stages)-1] != stage {
				stages = append(stages, stage)
			}
		}}
	if err := Install(context.Background(), e, plan, disk.Disk{Number: 2}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"diskpart.exe /s " + filepath.Join(e.Work, "diskpart.txt"),
		"dism.exe /English /Apply-Image /ImageFile:" + filepath.Join(drive("W"), "netdeploy", "install.esd") + " /Index:6 /ApplyDir:" + drive("W"),
		"dism.exe /English /Image:" + drive("W") + " /Add-Driver /Driver:" + filepath.Join(drive("W"), "netdeploy", "drivers") + " /Recurse",
		"Reagentc.exe /Setreimage /Path " + filepath.Join(drive("R"), "Recovery", "WindowsRE") + " /Target " + filepath.Join(drive("W"), "Windows"),
		"bcdboot.exe " + filepath.Join(drive("W"), "Windows") + " /s S: /f UEFI /bootex",
	}
	if strings.Join(run.cmds, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n%s\nwant:\n%s", strings.Join(run.cmds, "\n"), strings.Join(want, "\n"))
	}
	if b, _ := os.ReadFile(filepath.Join(e.Work, "diskpart.txt")); !strings.HasPrefix(string(b), "select disk 2") {
		t.Errorf("diskpart script %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(drive("W"), "Windows", "Panther", "unattend.xml")); string(b) != "<unattend/>" {
		t.Errorf("unattend %q", b)
	}
	if _, err := os.Stat(filepath.Join(drive("W"), "Windows", "Setup", "Scripts", "SetupComplete.cmd")); err != nil {
		t.Error("no SetupComplete.cmd")
	}
	if _, err := os.Stat(filepath.Join(drive("R"), "Recovery", "WindowsRE", "Winre.wim")); err != nil {
		t.Error("Winre.wim not copied")
	}
	if _, err := os.Stat(filepath.Join(drive("W"), "netdeploy", "install.esd")); err == nil {
		t.Error("downloaded image left behind")
	}
	if strings.Join(stages, ",") != "partitioning,applying-image,injecting-drivers,configuring,finalizing" {
		t.Errorf("stages %v", stages)
	}

	// bcdboot /bootex unsupported: falls back to the default boot manager.
	run.cmds, run.fail = nil, map[string]bool{"/bootex": true}
	if err := finalize(context.Background(), e, plan, disk.Disk{}); err != nil {
		t.Fatal(err)
	}
	if len(run.cmds) != 2 || strings.Contains(run.cmds[1], "/bootex") {
		t.Errorf("fallback: %v", run.cmds)
	}
	// A failing step stops the install and names the stage.
	run.cmds, run.fail = nil, map[string]bool{"diskpart": true}
	if err := Install(context.Background(), e, plan, disk.Disk{}); err == nil || !strings.HasPrefix(err.Error(), "partitioning:") {
		t.Errorf("failure: %v", err)
	}
}
