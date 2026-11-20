// Package ipxe holds the signed iPXE chain HTTP(S) Boot clients get
// (ADR 0032): Microsoft-signed shim → iPXE-signed ipxe.efi → a generated
// script → Microsoft-signed wimboot → the boot image's bootmgfw.efi.
package ipxe

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Dir is the directory under the content listener's boot prefix that
// serves the chain, the same for every client: ipxe/<arch>/<file>.
const Dir = "ipxe"

// The files of one architecture's set. Shim is what the firmware loads; it
// loads Loader from the same directory, which runs Script from there.
const (
	Shim    = "ipxe-shim.efi"
	Loader  = "ipxe.efi"
	WIMBoot = "wimboot"
	Script  = "autoexec.ipxe"
)

// Bundled lists the files read from disk, in the order they're reported.
var Bundled = []string{Shim, Loader, WIMBoot}

// Architectures, as in the API's Architecture.
var Architectures = []string{"x64", "arm64"}

// ShimPath is the firmware's file for arch, relative to the boot prefix.
func ShimPath(arch string) string { return path.Join(Dir, arch, Shim) }

// File is one bundled file.
type File struct {
	Arch, Name string
	Data       []byte
	SHA256     string
}

// Bundle is the chain read at start. The zero value (or nil) has nothing.
type Bundle struct {
	Path   string
	Reason string // why no architecture is available; "" when one is
	files  map[string]File
	archs  []string
	loaded time.Time
}

// Load reads dir/<arch>/{ipxe-shim.efi, ipxe.efi, wimboot} for each
// architecture. An architecture with any file missing is left out; a
// missing or empty dir gives a bundle with none and a Reason, not an error.
// Errors are for files that exist but can't be read.
func Load(dir string) (*Bundle, error) {
	b := &Bundle{Path: dir, files: map[string]File{}, loaded: time.Now()}
	if dir == "" {
		b.Reason = "no iPXE directory (the packages install one; -ipxe-dir names one)"
		return b, nil
	}
	var missing []string
	for _, arch := range Architectures {
		set := map[string]File{}
		for _, name := range Bundled {
			data, err := os.ReadFile(filepath.Join(dir, arch, name))
			if errors.Is(err, fs.ErrNotExist) {
				missing = append(missing, arch+"/"+name)
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("reading the iPXE bundle: %w", err)
			}
			sum := sha256.Sum256(data)
			set[name] = File{Arch: arch, Name: name, Data: data, SHA256: hex.EncodeToString(sum[:])}
		}
		if len(set) != len(Bundled) {
			continue
		}
		for name, f := range set {
			b.files[path.Join(Dir, arch, name)] = f
		}
		script := []byte(GenerateScript(arch))
		sum := sha256.Sum256(script)
		b.files[path.Join(Dir, arch, Script)] = File{Arch: arch, Name: Script, Data: script, SHA256: hex.EncodeToString(sum[:])}
		b.archs = append(b.archs, arch)
	}
	if len(b.archs) == 0 {
		b.Reason = fmt.Sprintf("no complete iPXE set in %s (missing %s)", dir, strings.Join(missing, ", "))
	}
	return b, nil
}

// Archs returns the architectures with a complete set.
func (b *Bundle) Archs() []string {
	if b == nil {
		return nil
	}
	return b.archs
}

// Has reports whether arch has a complete set.
func (b *Bundle) Has(arch string) bool {
	for _, a := range b.Archs() {
		if a == arch {
			return true
		}
	}
	return false
}

// Files returns the bundled files (not the generated scripts) in a stable
// order.
func (b *Bundle) Files() []File {
	var out []File
	for _, arch := range b.Archs() {
		for _, name := range Bundled {
			out = append(out, b.files[path.Join(Dir, arch, name)])
		}
	}
	return out
}

// Open implements fs.FS for paths under Dir ("ipxe/x64/ipxe.efi"). Files
// implement io.ReadSeeker and SHA256(), as the content handler wants.
func (b *Bundle) Open(name string) (fs.File, error) {
	if b == nil || !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	f, ok := b.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &memFile{Reader: bytes.NewReader(f.Data), f: f, mod: b.loaded}, nil
}

// GenerateScript is the iPXE script for arch: wimboot, then the boot
// image's files by absolute path, so they come over the scheme, host and
// port the script came from, and from the client's own boot image (the
// content listener picks it as for PXE, ADR 0024). On failure it says why
// and returns to the firmware: no iPXE shell.
func GenerateScript(arch string) string {
	return `#!ipxe
# netdeploy (ADR 0032): generated, the same for every ` + arch + ` client.
echo netdeploy: iPXE ${version} loaded from ${cwuri}
kernel wimboot gui || goto failed
initrd /content/boot/bootmgfw.efi bootmgfw.efi || goto failed
initrd /content/boot/Boot/BCD BCD || goto failed
initrd /content/boot/Boot/boot.sdi boot.sdi || goto failed
initrd /content/boot/Boot/boot.wim boot.wim || goto failed
boot || goto failed
:failed
echo netdeploy: boot failed (${errno}); see the netdeployd log. Returning to the firmware in 15 seconds.
sleep 15
exit 1
`
}

type memFile struct {
	*bytes.Reader
	f   File
	mod time.Time
}

func (m *memFile) Stat() (fs.FileInfo, error) { return info{m.f, m.mod}, nil }
func (m *memFile) Close() error               { return nil }
func (m *memFile) SHA256() string             { return m.f.SHA256 }

type info struct {
	f   File
	mod time.Time
}

func (i info) Name() string       { return i.f.Name }
func (i info) Size() int64        { return int64(len(i.f.Data)) }
func (i info) Mode() fs.FileMode  { return 0o444 }
func (i info) ModTime() time.Time { return i.mod }
func (i info) IsDir() bool        { return false }
func (i info) Sys() any           { return nil }
