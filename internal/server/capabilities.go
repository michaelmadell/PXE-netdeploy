package server

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
	"sync"
	"time"

	"github.com/michaelmadell/pxe-netdeploy/internal/api"
	"github.com/michaelmadell/pxe-netdeploy/internal/drivers/archive"
)

// capsTTL bounds how stale GET /system/capabilities can be: tools are
// probed (which runs them) at most this often, so installing one shows up
// without a restart.
const capsTTL = time.Minute

type capsCache struct {
	mu   sync.Mutex
	at   time.Time
	caps api.Capabilities
}

// GetCapabilities implements GET /system/capabilities.
func (s *Server) GetCapabilities(ctx context.Context, _ api.GetCapabilitiesRequestObject) (api.GetCapabilitiesResponseObject, error) {
	s.caps.mu.Lock()
	defer s.caps.mu.Unlock()
	if s.caps.at.IsZero() || time.Since(s.caps.at) > capsTTL {
		s.caps.caps = probeCapabilities(ctx, s.version)
		s.caps.at = time.Now()
	}
	return api.GetCapabilities200JSONResponse(s.caps.caps), nil
}

func probeCapabilities(ctx context.Context, version string) api.Capabilities {
	c := api.Capabilities{Version: version, HostOs: api.CapabilitiesHostOsLinux}
	if runtime.GOOS == "windows" {
		c.HostOs = api.CapabilitiesHostOsWindows
	}
	type builder = struct {
		Available bool            `json:"available"`
		Name      api.BuilderName `json:"name"`
		Reason    *string         `json:"reason,omitempty"`
		Version   *string         `json:"version,omitempty"`
	}
	str := func(s string) *string { return &s }

	// DISM (Windows only).
	dism := builder{Name: api.BuilderNameDism}
	if runtime.GOOS != "windows" {
		dism.Reason = str("DISM is only available on Windows hosts.")
	} else if p := system32("dism.exe"); p == "" {
		dism.Reason = str("dism.exe not found in System32.")
	} else if v := peVersion(p); v == "" {
		// Read from the file: dism.exe won't even print /? unelevated.
		dism.Reason = str("dism.exe has no version resource.")
	} else {
		dism.Available, dism.Version = true, &v
	}

	// wimlib (wherever wimlib-imagex is on PATH).
	wimlib := builder{Name: api.BuilderNameWimlib}
	if p, err := exec.LookPath("wimlib-imagex"); err != nil {
		wimlib.Reason = str("wimlib-imagex not found on PATH.")
	} else {
		wimlib.Available = true
		if v := toolVersion(ctx, regexp.MustCompile(`wimlib-imagex ([0-9.]+)`), p, "--version"); v != "" {
			wimlib.Version = &v
		}
	}
	c.Builders = []builder{dism, wimlib}

	// BCD editing: bcdedit on Windows, hivex on Linux.
	if runtime.GOOS == "windows" {
		c.BcdTool.Name = api.CapabilitiesBcdToolNameBcdedit
		if system32("bcdedit.exe") != "" {
			c.BcdTool.Available = true
		} else {
			c.BcdTool.Reason = str("bcdedit.exe not found in System32.")
		}
	} else {
		c.BcdTool.Name = api.CapabilitiesBcdToolNameHivex
		if _, err := exec.LookPath("hivexregedit"); err == nil {
			c.BcdTool.Available = true
		} else {
			c.BcdTool.Reason = str("hivexregedit not found on PATH (install hivex).")
		}
	}

	// LZX CABs (ADR 0019).
	lzx := &struct {
		Available bool    `json:"available"`
		Reason    *string `json:"reason,omitempty"`
		Tool      *string `json:"tool"`
	}{}
	if t, err := archive.LookupLZXTool(); err != nil {
		lzx.Reason = str(err.Error())
	} else {
		lzx.Available, lzx.Tool = true, &t.Name
	}
	c.CabLzx = lzx
	return c
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
// (found by its signature), e.g. "10.0.26100.8457".
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
