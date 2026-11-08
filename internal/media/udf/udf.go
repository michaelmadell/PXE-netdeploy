// Package udf is a read-only UDF (ECMA-167 / OSTA UDF) reader for install
// media (ADR 0018): UDF 1.02 to 2.01 with 2048-byte sectors and a single
// type-1 partition, File Entries and Extended File Entries, short, long and
// inline allocation descriptors (with continuation extents), and 8- and
// 16-bit d-string names. UDF 2.50 metadata partitions, virtual and sparable
// partitions, and multi-session discs are refused with ErrUnsupported.
//
// FS implements fs.FS with case-insensitive name matching (an exact match
// wins), as Windows does. Files implement io.ReaderAt and io.Seeker across
// their extents, so large files (install.wim) can be read in place.
//
// The input is untrusted: every structure is bounds-checked and malformed
// media fails with ErrCorrupt rather than panicking.
package udf

import (
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

// SectorSize is the only sector (and logical block) size supported.
const SectorSize = 2048

var (
	// ErrNotUDF: the image has no UDF volume (e.g. plain ISO9660).
	ErrNotUDF = errors.New("udf: no UDF volume")
	// ErrUnsupported: a UDF feature this reader doesn't implement.
	ErrUnsupported = errors.New("udf: unsupported")
	// ErrCorrupt: a structure is malformed or points outside the image.
	ErrCorrupt = errors.New("udf: corrupt")
)

// Descriptor tag identifiers (ECMA-167 3/7.2.1, 4/7.2.1).
const (
	tagPVD  = 1
	tagAVDP = 2
	tagVDP  = 3
	tagIUVD = 4
	tagPD   = 5
	tagLVD  = 6
	tagUSD  = 7
	tagTD   = 8
	tagFSD  = 256
	tagFID  = 257
	tagAED  = 258
	tagFE   = 261
	tagEFE  = 266
)

// Limits that keep a hostile image from exhausting memory or looping.
const (
	maxDirBytes      = 64 << 20
	maxInlineBytes   = SectorSize
	maxADExtents     = 1 << 20
	maxContinuations = 4096
	maxVDSSectors    = 512
)

func corrupt(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, a...))
}

// FS is a UDF volume. Safe for concurrent use.
type FS struct {
	r         io.ReaderAt
	size      int64 // bytes
	partStart int64 // sector of logical block 0
	partLen   int64 // blocks
	label     string
	root      *entry

	mu   sync.Mutex
	dirs map[int64][]dirent // directory entry block -> children
}

type extent struct {
	off    int64 // byte offset in the image; -1: reads as zeros
	length int64
}

// entry is a parsed (Extended) File Entry.
type entry struct {
	block   int64 // logical block of the entry (cache key)
	dir     bool
	regular bool
	size    int64
	mtime   time.Time
	extents []extent
	inline  []byte
}

type dirent struct {
	name  string
	block int64 // logical block of its entry
}

// Open reads the UDF volume of an image of size bytes.
func Open(r io.ReaderAt, size int64) (*FS, error) {
	f := &FS{r: r, size: size, dirs: map[int64][]dirent{}}
	if err := f.checkVRS(); err != nil {
		return nil, err
	}
	main, reserve, err := f.anchor()
	if err != nil {
		return nil, err
	}
	fsd, err := f.volume(main)
	if err != nil && !errors.Is(err, ErrUnsupported) {
		// Fall back to the reserve sequence (ECMA-167 3/8.4.2).
		var err2 error
		if fsd, err2 = f.volume(reserve); err2 != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	rootBlock, err := f.fileSet(fsd)
	if err != nil {
		return nil, err
	}
	if f.root, err = f.readEntry(rootBlock); err != nil {
		return nil, err
	}
	if !f.root.dir {
		return nil, corrupt("root is not a directory")
	}
	return f, nil
}

// Label is the logical volume identifier (e.g. CPBA_X64FRE_EN-GB_DV9).
func (f *FS) Label() string { return f.label }

func (f *FS) readSector(sector int64) ([]byte, error) {
	if sector < 0 || (sector+1)*SectorSize > f.size {
		return nil, corrupt("sector %d outside the image", sector)
	}
	b := make([]byte, SectorSize)
	if _, err := f.r.ReadAt(b, sector*SectorSize); err != nil {
		return nil, fmt.Errorf("udf: read sector %d: %w", sector, err)
	}
	return b, nil
}

// readTagged reads a sector and checks its descriptor tag (checksum and
// recorded location; ECMA-167 3/7.2). want 0 accepts any identifier.
func (f *FS) readTagged(sector int64, location uint32, want uint16) ([]byte, uint16, error) {
	b, err := f.readSector(sector)
	if err != nil {
		return nil, 0, err
	}
	id, err := checkTag(b, location)
	if err != nil {
		return nil, 0, fmt.Errorf("sector %d: %w", sector, err)
	}
	if want != 0 && id != want {
		return nil, 0, corrupt("sector %d: tag %d, want %d", sector, id, want)
	}
	return b, id, nil
}

func checkTag(b []byte, location uint32) (uint16, error) {
	if len(b) < 16 {
		return 0, corrupt("short descriptor")
	}
	var sum byte
	for i := 0; i < 16; i++ {
		if i != 4 {
			sum += b[i]
		}
	}
	if sum != b[4] {
		return 0, corrupt("tag checksum")
	}
	if le16(b) == 0 { // zeros pass the checksum; identifier 0 is not a descriptor
		return 0, corrupt("no descriptor")
	}
	if got := le32(b[12:]); got != location {
		return 0, corrupt("tag location %d, expected %d", got, location)
	}
	return le16(b), nil
}

// checkVRS looks for BEA01 then NSR02/NSR03 in the volume recognition
// sequence (sectors 16+); ISO9660-only media has neither.
func (f *FS) checkVRS() error {
	bea := false
	for s := int64(16); s < 16+64; s++ {
		b, err := f.readSector(s)
		if err != nil {
			return ErrNotUDF
		}
		switch string(b[1:6]) {
		case "BEA01":
			bea = true
		case "NSR02", "NSR03":
			if bea {
				return nil
			}
		case "TEA01":
			return ErrNotUDF
		case "CD001", "CDW02", "BOOT2":
		default:
			if b[0] == 0 && b[1] == 0 {
				return ErrNotUDF
			}
		}
	}
	return ErrNotUDF
}

type extentAD struct{ length, loc uint32 }

// anchor reads the Anchor Volume Descriptor Pointer at sector 256, or the
// last sector (or 256 before it) as fallbacks.
func (f *FS) anchor() (main, reserve extentAD, err error) {
	last := f.size/SectorSize - 1
	for _, s := range []int64{256, last, last - 256} {
		b, _, err := f.readTagged(s, uint32(s), tagAVDP)
		if err != nil {
			continue
		}
		return extentAD{le32(b[16:]), le32(b[20:])}, extentAD{le32(b[24:]), le32(b[28:])}, nil
	}
	return extentAD{}, extentAD{}, fmt.Errorf("%w: no anchor volume descriptor", ErrNotUDF)
}

// volume reads a volume descriptor sequence: the partition and the logical
// volume, and returns the File Set Descriptor's location (a long_ad).
func (f *FS) volume(seq extentAD) (fsd []byte, err error) {
	var pd, lvd []byte
	n := int64(seq.length / SectorSize)
	for i := int64(0); i < min(n, maxVDSSectors); i++ {
		s := int64(seq.loc) + i
		b, id, err := f.readTagged(s, uint32(s), 0)
		if err != nil {
			return nil, err
		}
		switch id {
		case tagPD:
			if pd != nil {
				return nil, fmt.Errorf("%w: more than one partition", ErrUnsupported)
			}
			pd = b
		case tagLVD:
			lvd = b
		case tagVDP:
			return nil, fmt.Errorf("%w: volume descriptor pointer (multi-extent sequence)", ErrUnsupported)
		case tagTD:
			i = n
		case tagPVD, tagIUVD, tagUSD:
		}
	}
	if pd == nil || lvd == nil {
		return nil, corrupt("volume descriptor sequence lacks a partition or logical volume")
	}
	if bs := le32(lvd[212:]); bs != SectorSize {
		return nil, fmt.Errorf("%w: logical block size %d", ErrUnsupported, bs)
	}
	mapLen, nMaps := le32(lvd[264:]), le32(lvd[268:])
	if nMaps != 1 || mapLen != 6 || lvd[440] != 1 || lvd[441] != 6 {
		return nil, fmt.Errorf("%w: partition maps other than one type-1 map (UDF 2.50 metadata, virtual or sparable)", ErrUnsupported)
	}
	if le16(lvd[440+4:]) != le16(pd[22:]) {
		return nil, corrupt("logical volume refers to partition %d, descriptor is %d", le16(lvd[440+4:]), le16(pd[22:]))
	}
	f.partStart = int64(le32(pd[188:]))
	f.partLen = int64(le32(pd[192:]))
	if (f.partStart+f.partLen)*SectorSize > f.size {
		return nil, corrupt("partition extends past the image")
	}
	f.label = dstringField(lvd[84 : 84+128])
	return lvd[248 : 248+16], nil
}

// fileSet reads the File Set Descriptor and returns the root directory's
// entry block.
func (f *FS) fileSet(fsdAD []byte) (int64, error) {
	block, err := f.longAD(fsdAD)
	if err != nil {
		return 0, err
	}
	b, _, err := f.readTagged(f.partStart+block, uint32(block), tagFSD)
	if err != nil {
		return 0, err
	}
	return f.longAD(b[400:416])
}

// longAD returns the block a long_ad points to (partition 0 only).
func (f *FS) longAD(b []byte) (int64, error) {
	if ref := le16(b[8:]); ref != 0 {
		return 0, corrupt("long_ad partition reference %d", ref)
	}
	block := int64(le32(b[4:]))
	if block >= f.partLen {
		return 0, corrupt("block %d outside the partition", block)
	}
	return block, nil
}

// readEntry parses the (Extended) File Entry at a logical block.
func (f *FS) readEntry(block int64) (*entry, error) {
	b, id, err := f.readTagged(f.partStart+block, uint32(block), 0)
	if err != nil {
		return nil, err
	}
	var lea, lad, adStart, mtimeAt int
	switch id {
	case tagFE:
		lea, lad = int(le32(b[168:])), int(le32(b[172:]))
		adStart, mtimeAt = 176, 84
	case tagEFE:
		lea, lad = int(le32(b[208:])), int(le32(b[212:]))
		adStart, mtimeAt = 216, 92
	default:
		return nil, corrupt("block %d: tag %d is not a file entry", block, id)
	}
	if strategy := le16(b[16+4:]); strategy != 4 {
		return nil, fmt.Errorf("%w: ICB strategy %d", ErrUnsupported, strategy)
	}
	adStart += lea
	if lea < 0 || lad < 0 || adStart+lad > len(b) {
		return nil, corrupt("block %d: allocation descriptors overrun the entry", block)
	}
	e := &entry{block: block, size: int64(le64(b[56:])), mtime: timestamp(b[mtimeAt:])}
	if e.size < 0 {
		return nil, corrupt("block %d: negative size", block)
	}
	switch b[16+11] { // file type
	case 4:
		e.dir = true
	case 5:
		e.regular = true
	}
	ads := b[adStart : adStart+lad]
	switch adType := le16(b[16+18:]) & 7; adType {
	case 0, 1:
		if e.extents, err = f.allocation(ads, int(adType)); err != nil {
			return nil, fmt.Errorf("block %d: %w", block, err)
		}
	case 3:
		if int64(len(ads)) < e.size || len(ads) > maxInlineBytes {
			return nil, corrupt("block %d: inline data size", block)
		}
		e.inline = append([]byte(nil), ads[:e.size]...)
	default:
		return nil, fmt.Errorf("%w: allocation descriptor type %d", ErrUnsupported, adType)
	}
	var total int64
	for _, x := range e.extents {
		total += x.length
	}
	if e.inline == nil && total < e.size {
		return nil, corrupt("block %d: extents cover %d of %d bytes", block, total, e.size)
	}
	return e, nil
}

// allocation parses short (0) or long (1) allocation descriptors, following
// continuation extents (type 3) into Allocation Extent Descriptors.
func (f *FS) allocation(ads []byte, adType int) ([]extent, error) {
	size := 8
	if adType == 1 {
		size = 16
	}
	var out []extent
	for hops := 0; ; hops++ {
		next := int64(-1)
		for i := 0; i+size <= len(ads); i += size {
			raw := le32(ads[i:])
			length, kind := int64(raw&0x3fffffff), raw>>30
			if length == 0 {
				break
			}
			var block int64
			if adType == 0 {
				block = int64(le32(ads[i+4:]))
			} else {
				var err error
				if block, err = f.longAD(ads[i : i+16]); err != nil {
					return nil, err
				}
			}
			if kind == 3 {
				next = block
				break
			}
			x := extent{off: -1, length: length}
			if kind == 0 { // recorded and allocated
				if block+(length+SectorSize-1)/SectorSize > f.partLen {
					return nil, corrupt("extent outside the partition")
				}
				x.off = (f.partStart + block) * SectorSize
			}
			out = append(out, x)
			if len(out) > maxADExtents {
				return nil, corrupt("too many extents")
			}
		}
		if next < 0 {
			return out, nil
		}
		if hops >= maxContinuations {
			return nil, corrupt("too many allocation extent continuations")
		}
		b, _, err := f.readTagged(f.partStart+next, uint32(next), tagAED)
		if err != nil {
			return nil, err
		}
		l := int(le32(b[20:]))
		if 24+l > len(b) {
			return nil, corrupt("allocation extent descriptor overruns its sector")
		}
		ads = b[24 : 24+l]
	}
}

// children lists a directory, reading and caching its File Identifiers.
func (f *FS) children(dir *entry) ([]dirent, error) {
	f.mu.Lock()
	c, ok := f.dirs[dir.block]
	f.mu.Unlock()
	if ok {
		return c, nil
	}
	if dir.size > maxDirBytes {
		return nil, corrupt("directory of %d bytes", dir.size)
	}
	data := make([]byte, dir.size)
	if _, err := f.reader(dir).ReadAt(data, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	var out []dirent
	for i := 0; i+38 <= len(data); {
		if le16(data[i:]) != tagFID {
			return nil, corrupt("directory entry tag %d", le16(data[i:]))
		}
		chars := data[i+18]
		lfi := int(data[i+19])
		liu := int(le16(data[i+36:]))
		end := i + 38 + liu + lfi
		if end > len(data) {
			return nil, corrupt("directory entry overruns its directory")
		}
		block, err := f.longAD(data[i+20 : i+36])
		if err != nil {
			return nil, err
		}
		name := dstring(data[i+38+liu : end])
		i += (38 + liu + lfi + 3) &^ 3
		if chars&0x0c != 0 || name == "" { // deleted or parent
			continue
		}
		out = append(out, dirent{name: name, block: block})
	}
	f.mu.Lock()
	f.dirs[dir.block] = out
	f.mu.Unlock()
	return out, nil
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
		if cur, err = f.readEntry(kids[match].block); err != nil {
			return nil, "", err
		}
		base = kids[match].name
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
	return &File{fs: f, e: e, info: fileInfo{e: e, name: base}, SectionReader: io.NewSectionReader(f.reader(e), 0, e.size)}, nil
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
	// fs.ReadDirFS: sorted by name.
	slices.SortFunc(list, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return list, nil
}

func (f *FS) reader(e *entry) io.ReaderAt {
	if e.inline != nil {
		return bytesReaderAt(e.inline)
	}
	return &extentReader{r: f.r, extents: e.extents, size: e.size}
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
			e, err := fl.fs.readEntry(k.block)
			if err != nil {
				return nil, err
			}
			fl.dirs = append(fl.dirs, fs.FileInfoToDirEntry(fileInfo{e: e, name: k.name}))
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

func (i fileInfo) Name() string       { return i.name }
func (i fileInfo) Size() int64        { return i.e.size }
func (i fileInfo) ModTime() time.Time { return i.e.mtime }
func (i fileInfo) IsDir() bool        { return i.e.dir }
func (i fileInfo) Sys() any           { return nil }
func (i fileInfo) Mode() fs.FileMode {
	switch {
	case i.e.dir:
		return fs.ModeDir | 0o555
	case i.e.regular:
		return 0o444
	}
	return fs.ModeIrregular | 0o444
}

// extentReader reads a file's bytes across its extents; unrecorded extents
// read as zeros.
type extentReader struct {
	r       io.ReaderAt
	extents []extent
	size    int64
}

func (x *extentReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= x.size {
		return 0, io.EOF
	}
	want := len(p)
	if rem := x.size - off; int64(want) > rem {
		want = int(rem)
	}
	n := 0
	for _, e := range x.extents {
		if n == want {
			break
		}
		if off >= e.length {
			off -= e.length
			continue
		}
		k := int(min(int64(want-n), e.length-off))
		if e.off < 0 {
			clear(p[n : n+k])
		} else if got, err := x.r.ReadAt(p[n:n+k], e.off+off); err != nil {
			return n + got, err
		}
		n += k
		off = 0
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// dstringField decodes a fixed-size dstring field, whose last byte is the
// number of bytes used (OSTA UDF 2.1.3).
func dstringField(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	n := int(b[len(b)-1])
	if n == 0 || n >= len(b) {
		return ""
	}
	return dstring(b[:n])
}

// dstring decodes OSTA CS0 characters: compression ID 8 means one byte per
// code point (Latin-1), 16 means UTF-16BE.
func dstring(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	switch b[0] {
	case 8:
		r := make([]rune, 0, len(b)-1)
		for _, c := range b[1:] {
			if c == 0 {
				break
			}
			r = append(r, rune(c))
		}
		return string(r)
	case 16:
		u := make([]uint16, 0, len(b)/2)
		for i := 1; i+1 < len(b); i += 2 {
			v := uint16(b[i])<<8 | uint16(b[i+1])
			if v == 0 {
				break
			}
			u = append(u, v)
		}
		return string(utf16.Decode(u))
	}
	return ""
}

// timestamp decodes an ECMA-167 timestamp (1/7.3), UTC offset applied.
func timestamp(b []byte) time.Time {
	if len(b) < 12 {
		return time.Time{}
	}
	tz := le16(b) & 0x0fff
	t := time.Date(int(le16(b[2:])), time.Month(b[4]), int(b[5]), int(b[6]), int(b[7]), int(b[8]),
		int(b[9])*10_000_000+int(b[10])*100_000+int(b[11])*1_000, time.UTC)
	if le16(b)>>12 == 1 && tz != 0x0801 { // local time with a valid offset (minutes, signed 12-bit)
		off := int(tz)
		if off >= 0x800 {
			off -= 0x1000
		}
		t = t.Add(-time.Duration(off) * time.Minute)
	}
	return t
}

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

var _ fs.ReadDirFS = (*FS)(nil)
