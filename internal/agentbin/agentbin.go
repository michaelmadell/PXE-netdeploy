// Package agentbin provides the WinPE agent binaries the boot image builder
// puts into boot images (ADR 0022). Release builds embed them (build tag
// `agent`, see scripts/build.ps1); otherwise they are read from a directory
// (`-agent-dir`, default ./dist after `just build`).
package agentbin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Architectures and their file names (as scripts/build.ps1 writes them).
var files = map[string]string{
	"x64":   "agent-windows-amd64.exe",
	"arm64": "agent-windows-arm64.exe",
}

// ErrUnavailable: no agent binary for the architecture.
var ErrUnavailable = errors.New("agentbin: no agent binary for this architecture")

// Binary is one agent executable.
type Binary struct {
	Arch    string // API Architecture: x64 or arm64
	Version string
	SHA256  string
	Size    int64
	data    []byte
}

// Open returns the binary's contents.
func (b Binary) Open() io.Reader { return bytes.NewReader(b.data) }

// Set is the agent binaries of one version.
type Set struct {
	Version string
	Source  string // "embedded", a directory, or "" when empty
	// Modified is the newest agent file's time when read from a directory
	// (logged at startup: a stale ./dist agent is an easy mistake).
	Modified time.Time
	bins     map[string]Binary
}

// Load returns the embedded agents, or, when this build has none, the
// agents in dir (missing files are skipped; a missing dir gives an empty
// Set). version is the server's: agents are built with it.
func Load(version, dir string) (*Set, error) {
	if embedded != nil {
		return load(version, "embedded", embedded)
	}
	if dir == "" {
		return &Set{Version: version, bins: map[string]Binary{}}, nil
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return &Set{Version: version, bins: map[string]Binary{}}, nil
	}
	s, err := load(version, dir, os.DirFS(dir))
	if err != nil {
		return nil, err
	}
	for _, name := range files {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && fi.ModTime().After(s.Modified) {
			s.Modified = fi.ModTime()
		}
	}
	return s, nil
}

func load(version, source string, fsys fs.FS) (*Set, error) {
	s := &Set{Version: version, Source: source, bins: map[string]Binary{}}
	for arch, name := range files {
		b, err := fs.ReadFile(fsys, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("agentbin: %s: %w", name, err)
		}
		if len(b) < 2 || b[0] != 'M' || b[1] != 'Z' {
			return nil, fmt.Errorf("agentbin: %s is not a Windows executable", name)
		}
		sum := sha256.Sum256(b)
		s.bins[arch] = Binary{Arch: arch, Version: version, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b)), data: b}
	}
	return s, nil
}

// Get returns the agent for arch (x64 or arm64). ErrUnavailable.
func (s *Set) Get(arch string) (Binary, error) {
	if s != nil {
		if b, ok := s.bins[arch]; ok {
			return b, nil
		}
	}
	return Binary{}, fmt.Errorf("%w: %s", ErrUnavailable, arch)
}

// Architectures lists the architectures with an agent, sorted.
func (s *Set) Architectures() []string {
	out := []string{}
	if s == nil {
		return out
	}
	for a := range s.bins {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// ForTest returns a Set holding the given binaries (arch → contents).
func ForTest(version string, bins map[string][]byte) *Set {
	s := &Set{Version: version, Source: "test", bins: map[string]Binary{}}
	for arch, b := range bins {
		sum := sha256.Sum256(b)
		s.bins[arch] = Binary{Arch: arch, Version: version, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b)), data: b}
	}
	return s
}
