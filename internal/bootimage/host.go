package bootimage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Builder names.
const (
	BuilderDISM   = "dism"
	BuilderWimlib = "wimlib"
)

// ToolStatus is whether one builder can run on this host.
type ToolStatus struct {
	Name      string
	Available bool
	Version   string // DISM: file version (e.g. 10.0.26100.9549); wimlib: e.g. 1.14.4
	Path      string
	Reason    string // why not available
}

// Host is what this host can build with.
type Host struct {
	OS       string
	Elevated bool
	DISM     ToolStatus
	Wimlib   ToolStatus
}

// Tool returns the status of the named builder.
func (h Host) Tool(name string) ToolStatus {
	if name == BuilderDISM {
		return h.DISM
	}
	return h.Wimlib
}

// Detect probes the builders. wimlibPath overrides the PATH lookup of
// wimlib-imagex (the -wimlib flag).
func Detect(ctx context.Context, wimlibPath string) Host {
	h := Host{OS: runtime.GOOS, Elevated: elevated()}

	h.DISM = ToolStatus{Name: BuilderDISM}
	switch {
	case runtime.GOOS != "windows":
		h.DISM.Reason = "DISM is only available on Windows hosts."
	default:
		p := system32("dism.exe")
		v := ""
		if p != "" {
			v = peVersion(p) // read from the file: dism.exe won't even print /? unelevated
		}
		switch {
		case p == "":
			h.DISM.Reason = "dism.exe not found in System32."
		case v == "":
			h.DISM.Reason = "dism.exe has no version resource."
		case !h.Elevated:
			h.DISM.Path, h.DISM.Version = p, v
			h.DISM.Reason = "DISM needs elevation: run netdeployd as a service or from an elevated shell."
		default:
			h.DISM.Available, h.DISM.Path, h.DISM.Version = true, p, v
		}
	}

	h.Wimlib = ToolStatus{Name: BuilderWimlib}
	p := wimlibPath
	if p == "" {
		var err error
		if p, err = exec.LookPath("wimlib-imagex"); err != nil {
			h.Wimlib.Reason = "wimlib-imagex not found on PATH (Linux: install wimtools; or pass -wimlib)."
			return h
		}
	}
	h.Wimlib.Path = p
	if _, err := os.Stat(p); err != nil {
		h.Wimlib.Reason = "wimlib-imagex not found at " + p + "."
		return h
	}
	h.Wimlib.Version = toolVersion(ctx, regexp.MustCompile(`wimlib-imagex ([0-9.]+)`), p, "--version")
	if h.Wimlib.Version == "" {
		h.Wimlib.Reason = p + " didn't report a wimlib-imagex version."
		return h
	}
	h.Wimlib.Available = true
	return h
}

// dismBuild returns the build number in a DISM version (10.0.26100.x →
// 26100), or 0.
func dismBuild(v string) int {
	parts := strings.Split(v, ".")
	if len(parts) < 3 {
		return 0
	}
	n, _ := strconv.Atoi(parts[2])
	return n
}

// imageBuild returns the build number of a WIM build string (26100.9457).
func imageBuild(v string) int {
	n, _ := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	return n
}

func system32(name string) string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		return ""
	}
	p := filepath.Join(root, "System32", name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// toolVersion runs a tool briefly and returns the first match of re.
func toolVersion(ctx context.Context, re *regexp.Regexp, path string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, path, args...).Output()
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if m := re.FindStringSubmatch(sc.Text()); m != nil {
			return m[1]
		}
	}
	return ""
}

// peVersion returns a PE file's product version from its VS_FIXEDFILEINFO
// (found by its signature), e.g. "10.0.26100.9549". The binary field, not
// the version string, which can be stale (Phase 3 findings).
func peVersion(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sig := []byte{0xbd, 0x04, 0xef, 0xfe} // 0xFEEF04BD
	i := bytes.Index(b, sig)
	if i < 0 || i+24 > len(b) {
		return ""
	}
	ms := binary.LittleEndian.Uint32(b[i+16:])
	ls := binary.LittleEndian.Uint32(b[i+20:])
	return fmt.Sprintf("%d.%d.%d.%d", ms>>16, ms&0xffff, ls>>16, ls&0xffff)
}
