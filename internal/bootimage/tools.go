package bootimage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Tool edits WIMs for a build: one per builder (ADR 0005, 0022).
type Tool interface {
	Name() string
	// Export copies image index of src into a new single-image, bootable,
	// LZX-compressed WIM at dst.
	Export(ctx context.Context, src string, index int, dst string) error
	// Customize adds c.Files (and, for DISM, injects c.Drivers) into the
	// single image of c.WIM and leaves the result at c.Out.
	Customize(ctx context.Context, c Customization) error
}

// Customization is what Tool.Customize adds.
type Customization struct {
	WIM     string   // single-image WIM to change (may be consumed)
	Out     string   // where the finished WIM goes
	Work    string   // scratch directory for the tool (mount dir, logs)
	Files   string   // directory whose tree is added at the image root
	Drivers []string // INF files on disk to inject offline (DISM only)
	Log     *slog.Logger
}

// newTool returns the real tool for a builder.
func newTool(name string, h Host) Tool {
	if name == BuilderDISM {
		return dismTool{path: h.DISM.Path}
	}
	return wimlibTool{path: h.Wimlib.Path}
}

// ---------------------------------------------------------------- DISM

type dismTool struct{ path string }

func (dismTool) Name() string { return BuilderDISM }

// run runs one DISM command. DISM isn't killed on cancellation: a killed
// DISM leaves a mount behind, so each step finishes and the caller checks
// ctx between steps (ADR 0022).
func (d dismTool) run(logPath string, args ...string) error {
	full := append([]string{"/English", "/LogPath:" + logPath}, args...)
	out, err := exec.Command(d.path, full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("dism %s: %v: %s", args[0], err, tail(out, 12))
	}
	return nil
}

func (d dismTool) Export(ctx context.Context, src string, index int, dst string) error {
	return d.run(filepath.Join(filepath.Dir(dst), "dism-export.log"), "/Export-Image", "/SourceImageFile:"+src,
		"/SourceIndex:"+strconv.Itoa(index), "/DestinationImageFile:"+dst, "/Compress:max", "/Bootable")
}

func (d dismTool) Customize(ctx context.Context, c Customization) (err error) {
	logPath := filepath.Join(c.Work, "dism.log")
	mount := filepath.Join(c.Work, "mount")
	if err := os.MkdirAll(mount, 0o700); err != nil {
		return err
	}
	if err := d.run(logPath, "/Mount-Image", "/ImageFile:"+c.WIM, "/Index:1", "/MountDir:"+mount); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			if derr := d.run(logPath, "/Unmount-Image", "/MountDir:"+mount, "/Discard"); derr != nil && c.Log != nil {
				c.Log.Error("discard DISM mount", "mount", mount, "err", derr)
			}
		}
	}()
	for _, inf := range c.Drivers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.run(logPath, "/Image:"+mount, "/Add-Driver", "/Driver:"+inf); err != nil {
			return fmt.Errorf("add driver %s: %w", filepath.Base(inf), err)
		}
	}
	if err := copyTree(c.Files, mount); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.run(logPath, "/Unmount-Image", "/MountDir:"+mount, "/Commit"); err != nil {
		return err
	}
	committed = true
	// Export again: a commit appends, so this drops the dead space.
	return d.run(logPath, "/Export-Image", "/SourceImageFile:"+c.WIM, "/SourceIndex:1", "/DestinationImageFile:"+c.Out,
		"/Compress:max", "/Bootable")
}

// ---------------------------------------------------------------- wimlib

type wimlibTool struct{ path string }

func (wimlibTool) Name() string { return BuilderWimlib }

func (w wimlibTool) run(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, w.path, args...).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("wimlib-imagex %s: %v: %s", args[0], err, tail(out, 12))
	}
	return nil
}

func (w wimlibTool) Export(ctx context.Context, src string, index int, dst string) error {
	return w.run(ctx, "export", src, strconv.Itoa(index), dst, "--boot", "--compress=LZX")
}

func (w wimlibTool) Customize(ctx context.Context, c Customization) error {
	if len(c.Drivers) > 0 {
		return errors.New("wimlib can't inject drivers offline; they are staged for drvload")
	}
	args := []string{"update", c.WIM, "1", fmt.Sprintf(`--command=add "%s" /`, c.Files), "--rebuild"}
	if runtime.GOOS == "windows" {
		// Unelevated wimlib can't read every security descriptor; the added
		// files take the image's defaults (to verify in WinPE, ADR 0022).
		args = append(args, "--no-acls")
	}
	if err := w.run(ctx, args...); err != nil {
		return err
	}
	return os.Rename(c.WIM, c.Out)
}

// ---------------------------------------------------------------- helpers

// tail returns the last n non-empty lines of out.
func tail(out []byte, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// copyTree copies the files under src into dst, creating directories and
// replacing existing files.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(p, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// linkOrCopy hard-links src (a read-only content store blob) to dst, or
// copies it where the volume has no hard links.
func linkOrCopy(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}
