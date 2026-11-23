// Package iso9660 is a read-only ISO 9660 (ECMA-119) reader for Linux
// install media (ADR 0033): 2048-byte logical blocks, the primary volume
// descriptor, Rock Ridge names and file types (SUSP SP, CE, NM, PX, SL, CL,
// RE), Joliet names (a supplementary volume descriptor, UCS-2) when there's
// no Rock Ridge, else plain names with the ";1" version and a trailing "."
// removed. Multi-extent files are joined. Interleaved files are refused
// with ErrUnsupported; El Torito is ignored.
//
// FS implements fs.FS with case-insensitive name matching (an exact match
// wins), as the UDF reader does. Files implement io.ReaderAt and io.Seeker
// across their extents. Symlinks are reported with fs.ModeSymlink and
// never followed.
//
// The input is untrusted: every structure is bounds-checked, continuation
// chains and directory depth are limited, and malformed media fails with
// ErrCorrupt rather than panicking.
package iso9660

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// BlockSize is the only logical block size supported.
const BlockSize = 2048

const (
	maxDescriptors = 64
	maxDirBytes    = 64 << 20 // one directory's records
	maxDepth       = 64       // directory levels below the root
	maxCE          = 32       // continuation areas per record
)

var (
	// ErrNotISO9660: no ISO 9660 primary volume descriptor.
	ErrNotISO9660 = errors.New("iso9660: no ISO 9660 volume")
	// ErrUnsupported: a feature this reader doesn't implement.
	ErrUnsupported = errors.New("iso9660: unsupported")
	// ErrCorrupt: a structure is malformed or points outside the image.
	ErrCorrupt = errors.New("iso9660: corrupt")
)

func corrupt(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, a...))
}

// Names says which names the reader uses.
type Names string

const (
	RockRidge Names = "rock-ridge"
	Joliet    Names = "joliet"
	Plain     Names = "plain"
)

// FS is an opened ISO 9660 image.
type FS struct {
	r      io.ReaderAt
	blocks int64
	label  string
	names  Names
	skip   int // SUSP bytes to skip at the start of each system use area
	root   *entry

	mu     sync.Mutex
	cache  map[uint32][]dirent // directory extent -> children
	parent map[uint32]uint32   // directory extent -> the directory listing it
}

type extent struct {
	block  uint32
	length uint32
}

type entry struct {
	dir     bool
	symlink bool
	size    int64
	extents []extent
	mtime   time.Time
	depth   int
}

type dirent struct {
	name string
	e    *entry
}

// Open reads the volume descriptors of an image of size bytes.
func Open(r io.ReaderAt, size int64) (*FS, error) {
	f := &FS{r: r, blocks: size / BlockSize, cache: map[uint32][]dirent{}, parent: map[uint32]uint32{}}
	var pvd, joliet []byte
	for i := int64(16); i < 16+maxDescriptors; i++ {
		b, err := f.block(i)
		if err != nil {
			if pvd == nil {
				return nil, ErrNotISO9660
			}
			return nil, err
		}
		if string(b[1:6]) != "CD001" {
			if pvd == nil {
				return nil, ErrNotISO9660
			}
			break
		}
		switch b[0] {
		case 1:
			if pvd == nil {
				pvd = b
			}
		case 2:
			// Joliet: escape sequence %/@, %/C or %/E (UCS-2 levels 1–3).
			esc := b[88:120]
			if joliet == nil && (bytes.HasPrefix(esc, []byte("%/@")) || bytes.HasPrefix(esc, []byte("%/C")) || bytes.HasPrefix(esc, []byte("%/E"))) {
				joliet = b
			}
		case 255:
			i = 16 + maxDescriptors
		}
	}
	if pvd == nil {
		return nil, ErrNotISO9660
	}
	if bs := le16(pvd[128:]); bs != BlockSize {
		return nil, fmt.Errorf("%w: logical block size %d", ErrUnsupported, bs)
	}
	f.label = strings.TrimRight(string(pvd[40:72]), " \x00")

	root, _, err := f.record(pvd[156:190], 0)
	if err != nil {
		return nil, corrupt("root directory record: %v", err)
	}
	f.root = root
	// Rock Ridge: an SP entry at the start of the root's "." record.
	if skip, ok := f.susp(root); ok {
		f.names, f.skip = RockRidge, skip
		return f, nil
	}
	if joliet != nil {
		jroot, _, err := f.record(joliet[156:190], 0)
		if err == nil && jroot.dir {
			f.names, f.root = Joliet, jroot
			return f, nil
		}
	}
	f.names = Plain
	return f, nil
}

// Label returns the volume identifier.
func (f *FS) Label() string { return f.label }

// Names returns the kind of names in use.
func (f *FS) Names() Names { return f.names }

func (f *FS) block(n int64) ([]byte, error) {
	if n < 0 || n >= f.blocks {
		return nil, corrupt("block %d outside the image (%d blocks)", n, f.blocks)
	}
	b := make([]byte, BlockSize)
	if _, err := f.r.ReadAt(b, n*BlockSize); err != nil {
		return nil, fmt.Errorf("iso9660: reading block %d: %w", n, err)
	}
	return b, nil
}

// readExtent reads length bytes from block.
func (f *FS) readExtent(block uint32, length int64) ([]byte, error) {
	end := (int64(block)*BlockSize + length + BlockSize - 1) / BlockSize
	if int64(block) >= f.blocks || end > f.blocks {
		return nil, corrupt("extent at block %d (%d bytes) outside the image", block, length)
	}
	b := make([]byte, length)
	if _, err := f.r.ReadAt(b, int64(block)*BlockSize); err != nil && !(errors.Is(err, io.EOF) && length == 0) {
		return nil, fmt.Errorf("iso9660: reading block %d: %w", block, err)
	}
	return b, nil
}

// record parses one directory record (b holds at least its length). It
// returns the entry and the raw record.
func (f *FS) record(b []byte, depth int) (*entry, []byte, error) {
	if len(b) < 34 {
		return nil, nil, corrupt("directory record too short")
	}
	n := int(b[0])
	if n < 34 || n > len(b) {
		return nil, nil, corrupt("directory record length %d", n)
	}
	rec := b[:n]
	if rec[1] != 0 {
		return nil, nil, fmt.Errorf("%w: extended attribute records", ErrUnsupported)
	}
	if rec[26] != 0 || rec[27] != 0 {
		return nil, nil, fmt.Errorf("%w: interleaved files", ErrUnsupported)
	}
	flags := rec[25]
	// Every extent must lie inside the image, so a file's size is bounded
	// by the image's (readers can trust Stat).
	if blk, l := int64(le32(rec[2:])), int64(le32(rec[10:])); l > 0 && (blk >= f.blocks || blk*BlockSize+l > f.blocks*BlockSize) {
		return nil, nil, corrupt("extent at block %d (%d bytes) outside the image", blk, l)
	}
	e := &entry{
		dir:     flags&2 != 0,
		size:    int64(le32(rec[10:])),
		extents: []extent{{block: le32(rec[2:]), length: le32(rec[10:])}},
		mtime:   recDate(rec[18:25]),
		depth:   depth,
	}
	return e, rec, nil
}

// systemUse returns a record's system use area.
func systemUse(rec []byte) []byte {
	nameLen := int(rec[32])
	off := 33 + nameLen
	if nameLen%2 == 0 {
		off++ // padding byte
	}
	if off >= len(rec) {
		return nil
	}
	return rec[off:]
}

// susp checks the root directory's first ("." ) record for an SP entry.
func (f *FS) susp(root *entry) (int, bool) {
	b, err := f.readExtent(root.extents[0].block, min(root.size, BlockSize))
	if err != nil || len(b) < 34 || int(b[0]) < 34 || int(b[0]) > len(b) {
		return 0, false
	}
	su := systemUse(b[:b[0]])
	if len(su) >= 7 && string(su[:2]) == "SP" && su[2] >= 7 && su[4] == 0xBE && su[5] == 0xEF {
		return int(su[6]), true
	}
	return 0, false
}

// rrInfo is what the Rock Ridge entries of a record say.
type rrInfo struct {
	name      string
	hasName   bool
	symlink   bool
	child     uint32 // CL: the relocated directory's block
	hasChild  bool
	relocated bool // RE: hide this entry
}

// rockRidge walks a record's SUSP entries, following CE continuations.
func (f *FS) rockRidge(rec []byte) (rrInfo, error) {
	var info rrInfo
	var name strings.Builder
	area := systemUse(rec)
	if len(area) >= f.skip {
		area = area[f.skip:]
	} else {
		area = nil
	}
	for hops := 0; ; hops++ {
		var next []byte
		for len(area) >= 4 {
			sig, l := string(area[:2]), int(area[2])
			if l < 4 || l > len(area) {
				break // padding or garbage: the rest of this area is unusable
			}
			d := area[4:l]
			switch sig {
			case "ST":
				area = nil
				continue
			case "CE":
				if len(d) < 24 {
					return info, corrupt("CE entry too short")
				}
				blk, off, n := le32(d[0:]), le32(d[8:]), le32(d[16:])
				if off >= BlockSize || n > BlockSize || int64(off)+int64(n) > BlockSize {
					return info, corrupt("CE area out of range")
				}
				b, err := f.block(int64(blk))
				if err != nil {
					return info, err
				}
				next = b[off : off+n]
			case "NM":
				if len(d) >= 1 && d[0]&0x06 == 0 { // not "." or ".."
					name.Write(d[1:])
					info.hasName = true
				}
			case "PX":
				if len(d) >= 4 && le32(d[0:])&0o170000 == 0o120000 {
					info.symlink = true
				}
			case "SL":
				info.symlink = true
			case "CL":
				if len(d) >= 4 {
					info.child, info.hasChild = le32(d[0:]), true
				}
			case "RE":
				info.relocated = true
			}
			area = area[l:]
		}
		if next == nil {
			break
		}
		if hops >= maxCE {
			return info, corrupt("too many continuation areas")
		}
		area = next
	}
	info.name = name.String()
	return info, nil
}

// children lists a directory, in on-disc order, joining multi-extent files.
func (f *FS) children(dir *entry) ([]dirent, error) {
	if !dir.dir {
		return nil, fs.ErrInvalid
	}
	if dir.depth >= maxDepth {
		return nil, corrupt("directories nested more than %d deep", maxDepth)
	}
	key := dir.extents[0].block
	f.mu.Lock()
	if kids, ok := f.cache[key]; ok {
		f.mu.Unlock()
		return depthed(kids, dir.depth+1), nil
	}
	f.mu.Unlock()
	if dir.size > maxDirBytes {
		return nil, corrupt("directory of %d bytes", dir.size)
	}
	data, err := f.readExtent(dir.extents[0].block, dir.size)
	if err != nil {
		return nil, err
	}
	var kids []dirent
	var pending *dirent // a multi-extent file still collecting extents
	for off := 0; off < len(data); {
		n := int(data[off])
		if n == 0 { // records don't cross blocks: skip to the next one
			off = (off/BlockSize + 1) * BlockSize
			continue
		}
		if off+n > len(data) || (off%BlockSize)+n > BlockSize {
			return nil, corrupt("directory record crosses a block boundary")
		}
		e, rec, err := f.record(data[off:off+n], dir.depth+1)
		if err != nil {
			return nil, err
		}
		off += n
		nameLen := int(rec[32])
		if 33+nameLen > len(rec) {
			return nil, corrupt("directory record name overruns the record")
		}
		raw := rec[33 : 33+nameLen]
		if nameLen == 1 && (raw[0] == 0 || raw[0] == 1) {
			continue // "." and ".."
		}
		name, skip, err := f.name(rec, raw, e)
		if err != nil {
			return nil, err
		}
		if skip {
			continue
		}
		if pending != nil {
			if name != pending.name {
				return nil, corrupt("multi-extent file %q is cut short", pending.name)
			}
			pending.e.extents = append(pending.e.extents, e.extents...)
			pending.e.size += e.size
		} else {
			kids = append(kids, dirent{name: name, e: e})
			pending = &kids[len(kids)-1]
		}
		if rec[25]&0x80 == 0 { // last extent
			pending = nil
		}
	}
	if pending != nil {
		return nil, corrupt("multi-extent file %q is cut short", pending.name)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Each directory appears in exactly one parent: a directory listed twice
	// (a loop, or a shared subtree) would make a walk endless or exponential.
	seen := map[uint32]bool{}
	for _, k := range kids {
		if !k.e.dir {
			continue
		}
		b := k.e.extents[0].block
		if b == key || b == f.root.extents[0].block {
			return nil, corrupt("directory %q contains itself or the root", k.name)
		}
		if p, ok := f.parent[b]; seen[b] || (ok && p != key) {
			return nil, corrupt("directory %q appears in more than one directory", k.name)
		}
		f.parent[b] = key
		seen[b] = true
	}
	f.cache[key] = kids
	return kids, nil
}

// depthed copies cached children with the depth the caller reached them at.
func depthed(kids []dirent, depth int) []dirent {
	out := make([]dirent, len(kids))
	for i, k := range kids {
		e := *k.e
		e.depth = depth
		out[i] = dirent{name: k.name, e: &e}
	}
	return out
}

// name works out an entry's name; skip is true for entries to hide.
func (f *FS) name(rec, raw []byte, e *entry) (string, bool, error) {
	var name string
	switch f.names {
	case RockRidge:
		info, err := f.rockRidge(rec)
		if err != nil {
			return "", false, err
		}
		if info.relocated {
			return "", true, nil
		}
		if info.hasChild { // the real directory lives elsewhere
			child, err := f.block(int64(info.child))
			if err != nil {
				return "", false, err
			}
			ce, _, err := f.record(child, e.depth)
			if err != nil {
				return "", false, err
			}
			*e = entry{dir: true, size: ce.size, extents: ce.extents, mtime: ce.mtime, depth: e.depth}
		}
		e.symlink = info.symlink
		if info.hasName {
			name = info.name
		} else {
			name = plainName(raw, e.dir)
		}
	case Joliet:
		if len(raw)%2 != 0 {
			return "", false, corrupt("odd-length Joliet name")
		}
		u := make([]uint16, len(raw)/2)
		for i := range u {
			u[i] = binary.BigEndian.Uint16(raw[2*i:])
		}
		name = string(utf16.Decode(u))
		if !e.dir {
			if i := strings.LastIndexByte(name, ';'); i >= 0 {
				name = name[:i]
			}
		}
	default:
		name = plainName(raw, e.dir)
	}
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return "", false, corrupt("invalid file name %q", name)
	}
	return name, false, nil
}

// plainName strips the ";1" version and a trailing "." from a file name.
func plainName(raw []byte, dir bool) string {
	s := string(raw)
	if !dir {
		if i := strings.LastIndexByte(s, ';'); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSuffix(s, ".")
	}
	return s
}

// lookup resolves a cleaned fs path case-insensitively.
func (f *FS) lookup(name string) (*entry, string, error) {
	cur, base := f.root, "."
	if name == "." {
		return cur, base, nil
	}
	for _, part := range strings.Split(name, "/") {
		if !cur.dir {
			return nil, "", fs.ErrNotExist
		}
		kids, err := f.children(cur)
		if err != nil {
			return nil, "", err
		}
		match := -1
		for i, k := range kids {
			if k.name == part {
				match = i
				break
			}
			if match < 0 && strings.EqualFold(k.name, part) {
				match = i
			}
		}
		if match < 0 {
			return nil, "", fs.ErrNotExist
		}
		cur, base = kids[match].e, kids[match].name
	}
	return cur, base, nil
}

// Open implements fs.FS.
func (f *FS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	e, base, err := f.lookup(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	size := e.size
	if e.dir || e.symlink {
		size = 0
	}
	return &File{fs: f, e: e, info: fileInfo{e: e, name: base},
		SectionReader: io.NewSectionReader(&extentReader{f: f, extents: e.extents, size: size}, 0, size)}, nil
}

// ReadDir implements fs.ReadDirFS.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	file, err := f.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	d, ok := file.(fs.ReadDirFile)
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: errors.New("not a directory")}
	}
	list, err := d.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(list, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return list, nil
}

// File is an open file or directory. Regular files support Read, ReadAt
// and Seek.
type File struct {
	*io.SectionReader
	fs   *FS
	e    *entry
	info fileInfo
	dirs []fs.DirEntry
	pos  int
}

// Stat implements fs.File.
func (fl *File) Stat() (fs.FileInfo, error) { return fl.info, nil }

// Close implements fs.File.
func (fl *File) Close() error { return nil }

// ReadDir implements fs.ReadDirFile.
func (fl *File) ReadDir(n int) ([]fs.DirEntry, error) {
	if !fl.e.dir {
		return nil, &fs.PathError{Op: "readdir", Path: fl.info.name, Err: errors.New("not a directory")}
	}
	if fl.dirs == nil {
		kids, err := fl.fs.children(fl.e)
		if err != nil {
			return nil, err
		}
		fl.dirs = make([]fs.DirEntry, 0, len(kids))
		for _, k := range kids {
			fl.dirs = append(fl.dirs, fs.FileInfoToDirEntry(fileInfo{e: k.e, name: k.name}))
		}
	}
	rest := fl.dirs[fl.pos:]
	if n <= 0 {
		fl.pos = len(fl.dirs)
		return rest, nil
	}
	if len(rest) == 0 {
		return nil, io.EOF
	}
	n = min(n, len(rest))
	fl.pos += n
	return rest[:n], nil
}

type fileInfo struct {
	e    *entry
	name string
}

func (i fileInfo) Name() string { return i.name }
func (i fileInfo) Size() int64 {
	if i.e.dir || i.e.symlink {
		return 0
	}
	return i.e.size
}
func (i fileInfo) ModTime() time.Time { return i.e.mtime }
func (i fileInfo) IsDir() bool        { return i.e.dir }
func (i fileInfo) Sys() any           { return nil }
func (i fileInfo) Mode() fs.FileMode {
	switch {
	case i.e.dir:
		return fs.ModeDir | 0o555
	case i.e.symlink:
		return fs.ModeSymlink | 0o444
	}
	return 0o444
}

// extentReader reads a file's bytes across its extents.
type extentReader struct {
	f       *FS
	extents []extent
	size    int64
}

func (x *extentReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= x.size {
		return 0, io.EOF
	}
	n := 0
	var pos int64 // file offset where the current extent starts
	for _, ext := range x.extents {
		l := int64(ext.length)
		if off < pos+l && n < len(p) {
			within := off - pos
			want := min(int64(len(p)-n), l-within, x.size-off)
			start := int64(ext.block)*BlockSize + within
			if start+want > x.f.blocks*BlockSize {
				return n, corrupt("file data outside the image")
			}
			m, err := x.f.r.ReadAt(p[n:n+int(want)], start)
			n += m
			off += int64(m)
			if err != nil && !(errors.Is(err, io.EOF) && int64(m) == want) {
				return n, err
			}
		}
		pos += l
		if n == len(p) || off >= x.size {
			break
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// recDate decodes a 7-byte directory record date.
func recDate(b []byte) time.Time {
	if len(b) < 7 || b[1] == 0 || b[2] == 0 {
		return time.Time{}
	}
	zone := time.FixedZone("", int(int8(b[6]))*15*60)
	return time.Date(1900+int(b[0]), time.Month(b[1]), int(b[2]), int(b[3]), int(b[4]), int(b[5]), 0, zone)
}

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

var _ fs.ReadDirFS = (*FS)(nil)
