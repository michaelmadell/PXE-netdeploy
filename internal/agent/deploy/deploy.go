// Package deploy runs the install steps of a deployment plan in WinPE
// (ADR 0025): partition, download and apply the image, add OS drivers,
// configure (unattend, WinRE), make the disk bootable.
package deploy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/michaelmadell/pxe-netdeploy/internal/agent/client"
	"github.com/michaelmadell/pxe-netdeploy/internal/agent/disk"
	"github.com/michaelmadell/pxe-netdeploy/internal/agent/wire"
)

// Runner runs a command and returns its combined output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// Exec runs real commands, logging each.
type Exec struct{ Log *slog.Logger }

// Run implements Runner.
func (e Exec) Run(ctx context.Context, name string, args ...string) (string, error) {
	e.Log.Info("run", "cmd", name+" "+strings.Join(args, " "))
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	text := strings.TrimSpace(strings.ReplaceAll(string(out), "\r", ""))
	if err != nil {
		e.Log.Error("command failed", "cmd", name, "err", err, "output", tail(text, 20))
		return text, fmt.Errorf("%s: %v: %s", name, err, tail(text, 8))
	}
	e.Log.Debug("command output", "cmd", name, "output", tail(text, 20))
	return text, nil
}

func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	var keep []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			keep = append(keep, l)
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, " | ")
}

// Env is what the steps use.
type Env struct {
	Client *client.Client
	Run    Runner
	Log    *slog.Logger
	// Report shows and sends progress (stage, percent, message).
	Report func(stage string, percent int, message string)
	// Work is the agent's own directory (X:\netdeploy).
	Work string
	// Drive maps a letter to its root, e.g. "W" → `W:\` (tests redirect it).
	Drive func(letter string) string
}

func (e Env) drive(l string) string {
	if e.Drive != nil {
		return e.Drive(l)
	}
	return l + `:\`
}

// Install runs the plan on target.
func Install(ctx context.Context, e Env, plan wire.Plan, target disk.Disk) error {
	if plan.InstallMethod != "agent" {
		return fmt.Errorf("install method %q isn't supported by this agent", plan.InstallMethod)
	}
	steps := []struct {
		stage string
		fn    func(context.Context, Env, wire.Plan, disk.Disk) error
	}{
		{wire.StagePartitioning, partition},
		{wire.StageApplyingImage, applyImage},
		{wire.StageInjectingDrivers, addDrivers},
		{wire.StageConfiguring, configure},
		{wire.StageFinalizing, finalize},
	}
	for _, s := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.fn(ctx, e, plan, target); err != nil {
			return fmt.Errorf("%s: %w", s.stage, err)
		}
	}
	return nil
}

func partition(ctx context.Context, e Env, _ wire.Plan, target disk.Disk) error {
	e.Report(wire.StagePartitioning, 0, "Partitioning "+target.String())
	script := filepath.Join(e.Work, "diskpart.txt")
	if err := os.WriteFile(script, []byte(disk.Script(target.Number)), 0o644); err != nil {
		return err
	}
	if _, err := e.Run.Run(ctx, "diskpart.exe", "/s", script); err != nil {
		return err
	}
	e.Report(wire.StagePartitioning, 100, "Partitioned")
	return nil
}

// fileName is the name a plan URL suggests (?name=), else def.
func fileName(contentURL, def string) string {
	if u, err := url.Parse(contentURL); err == nil {
		if n := path.Base(u.Query().Get("name")); n != "" && n != "." && n != "/" {
			return n
		}
	}
	return def
}

func applyImage(ctx context.Context, e Env, plan wire.Plan, _ disk.Disk) error {
	w := e.drive(disk.WindowsLetter)
	stage := filepath.Join(w, "netdeploy")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	img := filepath.Join(stage, fileName(plan.Image.ContentURL, "install.wim"))
	e.Report(wire.StageApplyingImage, 0, "Downloading the image")
	last := -1
	err := e.Client.Download(ctx, plan.Image, img, func(done, total int64) {
		if total > 0 {
			if pct := int(done * 60 / total); pct != last {
				last = pct
				e.Report(wire.StageApplyingImage, pct, fmt.Sprintf("Downloading the image: %d of %d MB", done>>20, total>>20))
			}
		}
	})
	if err != nil {
		return fmt.Errorf("download the image: %w", err)
	}
	e.Report(wire.StageApplyingImage, 60, fmt.Sprintf("Applying edition %d", plan.EditionIndex))
	if _, err := e.Run.Run(ctx, "dism.exe", "/English", "/Apply-Image", "/ImageFile:"+img,
		"/Index:"+strconv.Itoa(plan.EditionIndex), "/ApplyDir:"+w); err != nil {
		return err
	}
	if err := os.Remove(img); err != nil {
		e.Log.Warn("remove the downloaded image", "err", err)
	}
	e.Report(wire.StageApplyingImage, 100, "Image applied")
	return nil
}

func addDrivers(ctx context.Context, e Env, plan wire.Plan, _ disk.Disk) error {
	if len(plan.OSDrivers) == 0 {
		e.Report(wire.StageInjectingDrivers, 100, "No OS drivers")
		return nil
	}
	w := e.drive(disk.WindowsLetter)
	dir := filepath.Join(w, "netdeploy", "drivers")
	for i, f := range plan.OSDrivers {
		rel := filepath.FromSlash(path.Clean("/" + f.Path))[1:] // no escaping the folder
		dest := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		e.Report(wire.StageInjectingDrivers, i*50/len(plan.OSDrivers), "Downloading "+f.Path)
		if err := e.Client.Download(ctx, f, dest, nil); err != nil {
			return fmt.Errorf("download %s: %w", f.Path, err)
		}
	}
	e.Report(wire.StageInjectingDrivers, 50, fmt.Sprintf("Adding %d driver files", len(plan.OSDrivers)))
	if _, err := e.Run.Run(ctx, "dism.exe", "/English", "/Image:"+w, "/Add-Driver", "/Driver:"+dir, "/Recurse"); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		e.Log.Warn("remove the driver files", "err", err)
	}
	e.Report(wire.StageInjectingDrivers, 100, "Drivers added")
	return nil
}

// setupComplete deletes the unattend file after setup (ADR 0008).
const setupComplete = "@echo off\r\ndel /q /f \"%WINDIR%\\Panther\\unattend.xml\" >nul 2>&1\r\n"

func configure(ctx context.Context, e Env, plan wire.Plan, _ disk.Disk) error {
	w := e.drive(disk.WindowsLetter)
	if plan.UnattendURL != nil {
		e.Report(wire.StageConfiguring, 10, "Writing the unattend file")
		dest := filepath.Join(w, "Windows", "Panther", "unattend.xml")
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		b, err := e.Client.Get(ctx, *plan.UnattendURL)
		if err != nil {
			return fmt.Errorf("fetch the unattend file: %w", err)
		}
		if err := os.WriteFile(dest, b, 0o600); err != nil {
			return err
		}
		if plan.Cleanup == nil || plan.Cleanup.RemoveUnattend == nil || *plan.Cleanup.RemoveUnattend {
			sc := filepath.Join(w, "Windows", "Setup", "Scripts", "SetupComplete.cmd")
			if err := os.MkdirAll(filepath.Dir(sc), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(sc, []byte(setupComplete), 0o644); err != nil {
				return err
			}
		}
	}
	// WinRE on the recovery partition.
	e.Report(wire.StageConfiguring, 50, "Setting up Windows RE")
	src := filepath.Join(w, "Windows", "System32", "Recovery", "Winre.wim")
	reDir := filepath.Join(e.drive(disk.RecoveryLetter), "Recovery", "WindowsRE")
	if _, err := os.Stat(src); err != nil {
		e.Log.Warn("no Winre.wim in the image: skipping Windows RE", "err", err)
	} else {
		if err := os.MkdirAll(reDir, 0o755); err != nil {
			return err
		}
		if err := copyFile(src, filepath.Join(reDir, "Winre.wim")); err != nil {
			return fmt.Errorf("copy Winre.wim: %w", err)
		}
		reagentc := filepath.Join(w, "Windows", "System32", "Reagentc.exe")
		if _, err := e.Run.Run(ctx, reagentc, "/Setreimage", "/Path", reDir, "/Target", filepath.Join(w, "Windows")); err != nil {
			e.Log.Warn("register Windows RE (the OS boots without it)", "err", err)
		}
	}
	e.Report(wire.StageConfiguring, 100, "Configured")
	return nil
}

func finalize(ctx context.Context, e Env, plan wire.Plan, _ disk.Disk) error {
	e.Report(wire.StageFinalizing, 0, "Writing the boot files")
	win := filepath.Join(e.drive(disk.WindowsLetter), "Windows")
	args := []string{win, "/s", disk.SystemLetter + ":", "/f", "UEFI"}
	if plan.BootManager == "windows-uefi-ca-2023" {
		// The 2023-signed boot manager, as the boot image used (ADR 0025).
		if _, err := e.Run.Run(ctx, "bcdboot.exe", append(args, "/bootex")...); err == nil {
			e.Report(wire.StageFinalizing, 100, "Boot files written (2023-signed boot manager)")
			return nil
		} else {
			e.Log.Warn("bcdboot /bootex failed; using the default boot manager", "err", err)
		}
	}
	if _, err := e.Run.Run(ctx, "bcdboot.exe", args...); err != nil {
		return err
	}
	e.Report(wire.StageFinalizing, 100, "Boot files written")
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// CopyFile copies a file (the agent log at the end).
func CopyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return copyFile(src, dst)
}
