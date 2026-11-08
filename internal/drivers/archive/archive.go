// Package archive extracts driver archives (ADR 0019): zip (standard
// library), 7z (bodgit/sevenzip), and CAB (abemedia/go-cabinet for
// uncompressed and MSZIP folders; expand.exe on Windows or cabextract on
// Linux for LZX). Installers (.exe, .msi) are recognised and refused.
// Extraction is bounded: total bytes and file count.
package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	cabinet "github.com/abemedia/go-cabinet"
	"github.com/bodgit/sevenzip"
)

// Format is an archive format.
type Format string

// Formats.
const (
	Zip      Format = "zip"
	SevenZip Format = "7z"
	Cab      Format = "cab"
)

var (
	// ErrInstaller: an .exe or .msi, not an archive.
	ErrInstaller = errors.New("archive: installer (.exe/.msi), not an archive")
	// ErrUnsupported: not a format we read, or an LZX CAB without a tool.
	ErrUnsupported = errors.New("archive: unsupported")
	// ErrInvalid: malformed, or over the limits.
	ErrInvalid = errors.New("archive: invalid")
)

// Detect identifies a format from the first bytes of a file.
func Detect(head []byte) (Format, error) {
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")):
		return Zip, nil
	case bytes.HasPrefix(head, []byte("7z\xbc\xaf\x27\x1c")):
		return SevenZip, nil
	case bytes.HasPrefix(head, []byte("MSCF")):
		return Cab, nil
	case bytes.HasPrefix(head, []byte("MZ")), bytes.HasPrefix(head, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}):
		return "", ErrInstaller
	}
	return "", fmt.Errorf("%w: not a zip, 7z or cab archive", ErrUnsupported)
}

// Limits bound an extraction.
type Limits struct {
	MaxBytes int64
	MaxFiles int
}

// DefaultLimits for an archive of size bytes: 32x its size (at least
// 64 MiB) and 200,000 files.
func DefaultLimits(size int64) Limits {
	return Limits{MaxBytes: max(32*size, 64<<20), MaxFiles: 200_000}
}

// AddFunc receives each extracted file. name uses '/' and may contain any
// path the archive holds (the caller's store validates it).
type AddFunc func(name string, r io.Reader) error

// Options configure Extract.
type Options struct {
	Limits Limits
	// TempDir is where an external tool may extract an LZX CAB (on the
	// same volume as the content store, ideally). Required for LZX CABs.
	TempDir string
	// LZXTool overrides the LZX CAB tool lookup (tests). Nil: LookupLZXTool.
	LZXTool *Tool
}

// Extract reads the archive at path (size bytes) and calls add for every
// regular file.
func Extract(ctx context.Context, path string, size int64, opt Options, add AddFunc) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 8)
	if _, err := f.ReadAt(head, 0); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	format, err := Detect(head)
	if err != nil {
		return err
	}
	b := &budget{limits: opt.Limits}
	switch format {
	case Zip:
		return extractZip(ctx, f, size, b, add)
	case SevenZip:
		return extract7z(ctx, f, size, b, add)
	default:
		return extractCab(ctx, f, path, size, opt, b, add)
	}
}

// budget enforces Limits while reading.
type budget struct {
	limits Limits
	bytes  int64
	files  int
}

func (b *budget) file() error {
	b.files++
	if b.limits.MaxFiles > 0 && b.files > b.limits.MaxFiles {
		return fmt.Errorf("%w: more than %d files", ErrInvalid, b.limits.MaxFiles)
	}
	return nil
}

type limitedReader struct {
	ctx context.Context
	r   io.Reader
	b   *budget
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if err := l.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := l.r.Read(p)
	l.b.bytes += int64(n)
	if l.b.limits.MaxBytes > 0 && l.b.bytes > l.b.limits.MaxBytes {
		return n, fmt.Errorf("%w: expands to more than %d bytes", ErrInvalid, l.b.limits.MaxBytes)
	}
	return n, err
}

func (b *budget) add(ctx context.Context, name string, r io.Reader, add AddFunc) error {
	if err := b.file(); err != nil {
		return err
	}
	return add(strings.ReplaceAll(name, `\`, "/"), &limitedReader{ctx: ctx, r: r, b: b})
}

func extractZip(ctx context.Context, f io.ReaderAt, size int64, b *budget, add AddFunc) error {
	z, err := zip.NewReader(f, size)
	if err != nil {
		return fmt.Errorf("%w: zip: %v", ErrInvalid, err)
	}
	for _, zf := range z.File {
		if !zf.Mode().IsRegular() || strings.HasSuffix(zf.Name, "/") {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("%w: zip %s: %v", ErrInvalid, zf.Name, err)
		}
		err = b.add(ctx, zf.Name, rc, add)
		rc.Close()
		if err != nil {
			return wrapRead(err, zf.Name)
		}
	}
	return nil
}

func extract7z(ctx context.Context, f io.ReaderAt, size int64, b *budget, add AddFunc) error {
	z, err := sevenzip.NewReader(f, size)
	if err != nil {
		return fmt.Errorf("%w: 7z: %v", ErrInvalid, err)
	}
	for _, zf := range z.File {
		if zf.FileInfo().IsDir() || !zf.Mode().IsRegular() {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("%w: 7z %s: %v", ErrInvalid, zf.Name, err)
		}
		err = b.add(ctx, zf.Name, rc, add)
		rc.Close()
		if err != nil {
			return wrapRead(err, zf.Name)
		}
	}
	return nil
}

// wrapRead keeps our own errors (limits, cancellation, the caller's) and
// marks decompression failures as invalid archives.
func wrapRead(err error, name string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrInvalid), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, io.ErrUnexpectedEOF), strings.Contains(err.Error(), "checksum"), strings.Contains(err.Error(), "flate"):
		return fmt.Errorf("%w: %s: %v", ErrInvalid, name, err)
	}
	return err
}

// cabInfo reads what we need from a CAB header: the compression of each
// folder, the declared uncompressed size of all files, and whether it's
// part of a multi-cabinet set.
type cabInfo struct {
	lzx      bool
	quantum  bool
	spanned  bool
	declared int64
	files    int
}

func readCabInfo(f io.ReaderAt, size int64) (cabInfo, error) {
	var ci cabInfo
	h := make([]byte, 36)
	if _, err := f.ReadAt(h, 0); err != nil {
		return ci, fmt.Errorf("%w: cab header: %v", ErrInvalid, err)
	}
	coffFiles := int64(binary.LittleEndian.Uint32(h[16:]))
	folders := int(binary.LittleEndian.Uint16(h[26:]))
	nFiles := int(binary.LittleEndian.Uint16(h[28:]))
	flags := binary.LittleEndian.Uint16(h[30:])
	ci.spanned = flags&3 != 0
	off := int64(36)
	folderReserve := int64(0)
	if flags&4 != 0 {
		r := make([]byte, 4)
		if _, err := f.ReadAt(r, 36); err != nil {
			return ci, fmt.Errorf("%w: cab reserve: %v", ErrInvalid, err)
		}
		off += 4 + int64(binary.LittleEndian.Uint16(r))
		folderReserve = int64(r[2])
	}
	if ci.spanned {
		// Previous/next cabinet names follow; we refuse sets anyway.
		return ci, nil
	}
	fh := make([]byte, 8)
	for i := 0; i < folders; i++ {
		if _, err := f.ReadAt(fh, off); err != nil {
			return ci, fmt.Errorf("%w: cab folder: %v", ErrInvalid, err)
		}
		switch binary.LittleEndian.Uint16(fh[6:]) & 0x0f {
		case 2:
			ci.quantum = true
		case 3:
			ci.lzx = true
		}
		off += 8 + folderReserve
	}
	// CFFILE: cbFile(4) uoffFolderStart(4) iFolder(2) date(2) time(2) attribs(2) szName.
	off = coffFiles
	entry := make([]byte, 16+256)
	for i := 0; i < nFiles; i++ {
		if off >= size {
			return ci, fmt.Errorf("%w: cab file table past the end", ErrInvalid)
		}
		n, _ := f.ReadAt(entry, off)
		if n < 17 {
			return ci, fmt.Errorf("%w: cab file entry", ErrInvalid)
		}
		ci.declared += int64(binary.LittleEndian.Uint32(entry))
		end := bytes.IndexByte(entry[16:n], 0)
		if end < 0 {
			return ci, fmt.Errorf("%w: cab file name", ErrInvalid)
		}
		off += 16 + int64(end) + 1
	}
	ci.files = nFiles
	return ci, nil
}

func extractCab(ctx context.Context, f *os.File, path string, size int64, opt Options, b *budget, add AddFunc) error {
	ci, err := readCabInfo(f, size)
	if err != nil {
		return err
	}
	switch {
	case ci.spanned:
		return fmt.Errorf("%w: multi-cabinet sets", ErrUnsupported)
	case ci.quantum:
		return fmt.Errorf("%w: Quantum-compressed CAB", ErrUnsupported)
	case b.limits.MaxBytes > 0 && ci.declared > b.limits.MaxBytes:
		return fmt.Errorf("%w: CAB declares %d bytes, over the limit of %d", ErrInvalid, ci.declared, b.limits.MaxBytes)
	case b.limits.MaxFiles > 0 && ci.files > b.limits.MaxFiles:
		return fmt.Errorf("%w: CAB declares %d files, over the limit of %d", ErrInvalid, ci.files, b.limits.MaxFiles)
	}
	if ci.lzx {
		return extractCabTool(ctx, path, opt, b, add)
	}
	r, err := cabinet.NewReader(f)
	if err != nil {
		return fmt.Errorf("%w: cab: %v", ErrInvalid, err)
	}
	for _, cf := range r.Files {
		rc, err := cf.Open()
		if err != nil {
			return fmt.Errorf("%w: cab %s: %v", ErrInvalid, cf.Name, err)
		}
		err = b.add(ctx, cf.Name, rc, add)
		rc.Close()
		if err != nil {
			return wrapRead(err, cf.Name)
		}
	}
	return nil
}

// Tool is an external LZX CAB extractor.
type Tool struct {
	Name string // "expand.exe" or "cabextract"
	Path string
}

// LookupLZXTool finds expand.exe (Windows) or cabextract (elsewhere).
func LookupLZXTool() (*Tool, error) {
	name := "cabextract"
	if runtime.GOOS == "windows" {
		name = "expand.exe"
		if root := os.Getenv("SystemRoot"); root != "" {
			p := filepath.Join(root, "System32", "expand.exe")
			if _, err := os.Stat(p); err == nil {
				return &Tool{Name: name, Path: p}, nil
			}
		}
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("%s not found: install it to import LZX-compressed CABs", name)
	}
	return &Tool{Name: name, Path: p}, nil
}

func extractCabTool(ctx context.Context, path string, opt Options, b *budget, add AddFunc) error {
	tool := opt.LZXTool
	if tool == nil {
		var err error
		if tool, err = LookupLZXTool(); err != nil {
			return fmt.Errorf("%w: LZX-compressed CAB: %v", ErrUnsupported, err)
		}
	}
	if opt.TempDir == "" {
		return errors.New("archive: TempDir is required for LZX CABs")
	}
	dir, err := os.MkdirTemp(opt.TempDir, "cab-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	var cmd *exec.Cmd
	if strings.EqualFold(tool.Name, "expand.exe") {
		cmd = exec.CommandContext(ctx, tool.Path, "-F:*", path, dir)
	} else {
		cmd = exec.CommandContext(ctx, tool.Path, "-q", "-d", dir, path)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s failed: %v: %s", ErrInvalid, tool.Name, err, strings.TrimSpace(string(out)))
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		fh, err := os.Open(p)
		if err != nil {
			return err
		}
		defer fh.Close()
		return wrapRead(b.add(ctx, filepath.ToSlash(rel), fh, add), rel)
	})
}
