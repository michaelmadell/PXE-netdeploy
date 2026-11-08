package bcd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestBcdedit has Windows itself read a store we wrote (elevated shell):
//
//	$env:NETDEPLOY_TEST_BCDEDIT = '1'; go test -run TestBcdedit -v ./internal/bcd
//
// NETDEPLOY_TEST_BCD_OUT=<file> also saves the store (e.g. for a lab boot).
func TestBcdedit(t *testing.T) {
	if out := os.Getenv("NETDEPLOY_TEST_BCD_OUT"); out != "" {
		b, err := RAMDisk{WIMPath: `\Boot\boot.wim`, SDIPath: `\Boot\boot.sdi`, TFTPBlockSize: 16384, TFTPWindowSize: 16,
			Description: "netdeploy WinPE", TimeoutSeconds: 5}.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", out)
	}
	if os.Getenv("NETDEPLOY_TEST_BCDEDIT") == "" || runtime.GOOS != "windows" {
		t.Skip("set NETDEPLOY_TEST_BCDEDIT=1 on Windows, elevated")
	}
	path := filepath.Join(t.TempDir(), "BCD")
	b, err := phase1Opts.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bcdedit", "/store", path, "/enum", "all", "/v").CombinedOutput()
	t.Logf("bcdedit /enum all /v:\n%s", out)
	if err != nil {
		t.Fatalf("bcdedit: %v", err)
	}
	text := strings.ToLower(string(out))
	for _, want := range []string{
		"identifier              {9dea862c-5cdd-4e70-acc1-f32b344d4795}",
		"default                 " + WinPEID,
		"identifier              " + WinPEID,
		`device                  ramdisk=[boot]\boot\boot.wim,{ae5534e0-a924-466c-b836-758539a3ee3a}`,
		`osdevice                ramdisk=[boot]\boot\boot.wim,{ae5534e0-a924-466c-b836-758539a3ee3a}`,
		`path                    \windows\system32\winload.efi`,
		"winpe                   yes",
		"detecthal               yes",
		"ramdisksdidevice        boot",
		`ramdisksdipath          \boot\boot.sdi`,
		"ramdisktftpblocksize    16384",
		"ramdisktftpwindowsize   16",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("bcdedit output lacks %q", want)
		}
	}
	// The registry's own hive checks (what reg load runs) must pass too.
	if out, err := exec.Command("reg", "load", `HKLM\netdeploy-bcd-test`, path).CombinedOutput(); err != nil {
		t.Errorf("reg load: %v: %s", err, out)
	} else {
		exec.Command("reg", "unload", `HKLM\netdeploy-bcd-test`).Run()
	}
}
