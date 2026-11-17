// Package deploy runs the install steps of a deployment plan in WinPE
// (ADR 0025): partition, download and apply the image, add OS drivers,
// configure (unattend, WinRE), make the disk bootable.
package deploy

import (
	"context"
	"errors"
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
	// Stop ends when the server cancels the deployment (ADR 0026): the
	// command in progress finishes, nothing new starts, and the agent's own
	// transfers stop at once. Nil: never.
	Stop context.Context
}

// ErrCancelled: the deployment was cancelled on the server and stopped.
var ErrCancelled = errors.New("cancelled on the server")

func (e Env) stopped() bool { return e.Stop != nil && e.Stop.Err() != nil }

// run starts a command unless the deployment was cancelled. A command that
// has started always runs to the end: killing diskpart or DISM part-way
// is what a graceful stop avoids.
func (e Env) run(ctx context.Context, name string, args ...string) (string, error) {
	if e.stopped() {
		return "", ErrCancelled
	}
	return e.Run.Run(ctx, name, args...)
}

// transferCtx is ctx, also ended by a cancellation: for the agent's own
// downloads, which can stop at once.
func (e Env) transferCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	c, cancel := context.WithCancel(ctx)
	if e.Stop != nil {
		stop := context.AfterFunc(e.Stop, cancel)
		return c, func() { stop(); cancel() }
	}
	return c, cancel
}

// transferErr maps an error from a transfer ended by a cancellation.
func (e Env) transferErr(err error) error {
	if err != nil && e.stopped() {
		return ErrCancelled
	}
	return err
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
		if e.stopped() {
			return fmt.Errorf("before %s: %w", s.stage, ErrCancelled)
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
	if _, err := e.run(ctx, "diskpart.exe", "/s", script); err != nil {
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
	dctx, done := e.transferCtx(ctx)
	defer done()
	err := e.Client.Download(dctx, plan.Image, img, func(done, total int64) {
		if total > 0 {
			if pct := int(done * 60 / total); pct != last {
				last = pct
				e.Report(wire.StageApplyingImage, pct, fmt.Sprintf("Downloading the image: %d of %d MB", done>>20, total>>20))
			}
		}
	})
	if err = e.transferErr(err); err != nil {
		if errors.Is(err, ErrCancelled) {
			os.Remove(img)
		}
		return fmt.Errorf("download the image: %w", err)
	}
	e.Report(wire.StageApplyingImage, 60, fmt.Sprintf("Applying edition %d", plan.EditionIndex))
	if _, err := e.run(ctx, "dism.exe", "/English", "/Apply-Image", "/ImageFile:"+img,
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
		dctx, done := e.transferCtx(ctx)
		err := e.transferErr(e.Client.Download(dctx, f, dest, nil))
		done()
		if err != nil {
			if errors.Is(err, ErrCancelled) {
				os.RemoveAll(dir)
			}
			return fmt.Errorf("download %s: %w", f.Path, err)
		}
	}
	e.Report(wire.StageInjectingDrivers, 50, fmt.Sprintf("Adding %d driver files", len(plan.OSDrivers)))
	// In WinPE, DISM's scratch space is on the RAM disk (32 MB by default):
	// Microsoft's guidance is a scratch directory on the target. Its logs go
	// to the applied Windows, so they survive the restart after a failure.
	scratch := filepath.Join(w, "netdeploy", "scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return err
	}
	// DISM services an image with that image's own servicing binaries, which
	// WinPE's older DISM can't start (0x80004002 "No such interface", Phase 5
	// findings: 26H2 media's WinPE has DISM 26100.8972, its install image
	// 26100.9278). So the applied image's own DISM goes first; WinPE's is the
	// fallback.
	type dismTry struct{ exe, log, what string }
	var tries []dismTry
	if own := filepath.Join(w, "Windows", "System32", "Dism.exe"); fileExists(own) {
		tries = append(tries, dismTry{own, filepath.Join(w, "Windows", "Temp", "netdeploy-dism.log"), "the image's DISM"})
	}
	tries = append(tries, dismTry{"dism.exe", filepath.Join(w, "Windows", "Temp", "netdeploy-dism-winpe.log"), "WinPE's DISM"})
	var err error
	for i, t := range tries {
		_, err = e.run(ctx, t.exe, "/English", "/Image:"+w, "/Add-Driver", "/Driver:"+dir, "/Recurse", "/ScratchDir:"+scratch, "/LogPath:"+t.log)
		if err == nil {
			break
		}
		if errors.Is(err, ErrCancelled) {
			return err
		}
		for _, l := range DismErrors(t.log, 20) {
			e.Log.Error("dism.log", "dism", t.what, "line", l)
		}
		e.Log.Info("DISM's full log is on the target disk", "dism", t.what, "path", t.log)
		if i < len(tries)-1 {
			e.Log.Warn("adding drivers failed; trying again", "with", tries[i+1].what)
		}
	}
	if err != nil {
		return err
	}
	for _, d := range []string{dir, scratch} {
		if err := os.RemoveAll(d); err != nil {
			e.Log.Warn("remove the driver files", "dir", d, "err", err)
		}
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
		gctx, done := e.transferCtx(ctx)
		b, err := e.Client.Get(gctx, *plan.UnattendURL)
		done()
		if err = e.transferErr(err); err != nil {
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
		if _, err := e.run(ctx, reagentc, "/Setreimage", "/Path", reDir, "/Target", filepath.Join(w, "Windows")); err != nil {
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
		if _, err := e.run(ctx, "bcdboot.exe", append(args, "/bootex")...); err == nil {
			e.Report(wire.StageFinalizing, 100, "Boot files written (2023-signed boot manager)")
			return nil
		} else {
			e.Log.Warn("bcdboot /bootex failed; using the default boot manager", "err", err)
		}
	}
	if _, err := e.run(ctx, "bcdboot.exe", args...); err != nil {
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
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// DismErrors returns the last n error lines of a DISM log (", Error "
// entries), read from its last 1 MiB. Nothing if it can't be read.
func DismErrors(logPath string, n int) []string {
	f, err := os.Open(logPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	const window = 1 << 20
	if fi, err := f.Stat(); err == nil && fi.Size() > window {
		f.Seek(fi.Size()-window, io.SeekStart)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r", ""), "\n") {
		if strings.Contains(l, ", Error ") {
			out = append(out, strings.TrimSpace(l))
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func CopyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return copyFile(src, dst)
}
