// Package wim reads what image import needs from Windows Imaging files
// (ADR 0018): the header and XML metadata (images, editions, languages,
// builds), for WIM, ESD and split WIMs alike, with our own small reader;
// and single files from LZX-compressed WIMs through go-wim. It never
// decompresses a whole image.
package wim

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	gowim "github.com/nradchenko/go-wim"
)

var (
	// ErrNotWIM: the data doesn't start with a WIM header.
	ErrNotWIM = errors.New("wim: not a WIM file")
	// ErrUnsupported: reading files needs a compression go-wim doesn't
	// support (only LZX and uncompressed are read).
	ErrUnsupported = errors.New("wim: unsupported")
	// ErrCorrupt: the header or metadata is malformed.
	ErrCorrupt = errors.New("wim: corrupt")
)

// maxXML caps the XML resource (real ones are a few KB per image).
const maxXML = 16 << 20

// Header flags (WIM format, FLAG_HEADER_*).
const (
	flagCompression   = 0x00000002
	flagXpress        = 0x00020000
	flagLZX           = 0x00040000
	flagLZMS          = 0x00080000
	flagXpressHuffman = 0x00200000
)

// Info is a WIM's header and XML metadata.
type Info struct {
	Version     uint32
	Compression string // "none", "xpress", "lzx", "lzms"
	ChunkSize   uint32
	PartNumber  int // split WIMs: this part (1-based) of TotalParts
	TotalParts  int
	BootIndex   int
	Images      []Image // by index
}

// Image is one image's XML metadata.
type Image struct {
	Index           int
	Name            string
	DisplayName     string
	Description     string
	EditionID       string
	Arch            string // "x64", "arm64", "x86", "arm", or "" if unknown
	Build           string // e.g. "26100.9457"
	Languages       []string
	DefaultLanguage string
	TotalBytes      int64 // expanded size
}

// ReadInfo reads the header and XML metadata. The XML resource is stored
// uncompressed in every WIM variant, so this works for ESD and split WIMs
// too.
func ReadInfo(r io.ReaderAt) (Info, error) {
	h := make([]byte, 208)
	if _, err := r.ReadAt(h, 0); err != nil {
		if errors.Is(err, io.EOF) {
			return Info{}, ErrNotWIM
		}
		return Info{}, fmt.Errorf("wim: read header: %w", err)
	}
	if !bytes.Equal(h[:8], []byte("MSWIM\x00\x00\x00")) {
		return Info{}, ErrNotWIM
	}
	flags := binary.LittleEndian.Uint32(h[16:])
	info := Info{
		Version:     binary.LittleEndian.Uint32(h[12:]),
		Compression: compression(flags),
		ChunkSize:   binary.LittleEndian.Uint32(h[20:]),
		PartNumber:  int(binary.LittleEndian.Uint16(h[40:])),
		TotalParts:  int(binary.LittleEndian.Uint16(h[42:])),
		BootIndex:   int(binary.LittleEndian.Uint32(h[120:])),
	}
	count := int(binary.LittleEndian.Uint32(h[44:]))
	// rhXmlData: 7-byte stored size + flags byte, offset, original size.
	res := h[72:96]
	size := int64(binary.LittleEndian.Uint64(res[0:8]) & 0x00ffffffffffffff)
	off := int64(binary.LittleEndian.Uint64(res[8:16]))
	if size == 0 {
		if count == 0 {
			return info, nil
		}
		return Info{}, fmt.Errorf("%w: no XML metadata", ErrCorrupt)
	}
	if size > maxXML || size%2 != 0 || off < 208 {
		return Info{}, fmt.Errorf("%w: XML resource of %d bytes at %d", ErrCorrupt, size, off)
	}
	raw := make([]byte, size)
	if _, err := r.ReadAt(raw, off); err != nil {
		return Info{}, fmt.Errorf("%w: read XML: %v", ErrCorrupt, err)
	}
	images, err := parseXML(raw)
	if err != nil {
		return Info{}, err
	}
	if len(images) != count {
		return Info{}, fmt.Errorf("%w: header says %d images, XML describes %d", ErrCorrupt, count, len(images))
	}
	info.Images = images
	return info, nil
}

func compression(flags uint32) string {
	switch {
	case flags&flagCompression == 0:
		return "none"
	case flags&flagLZX != 0:
		return "lzx"
	case flags&flagLZMS != 0:
		return "lzms"
	case flags&(flagXpress|flagXpressHuffman) != 0:
		return "xpress"
	}
	return "unknown"
}

type xmlWIM struct {
	Images []struct {
		Index       string `xml:"INDEX,attr"`
		Name        string `xml:"NAME"`
		DisplayName string `xml:"DISPLAYNAME"`
		Description string `xml:"DESCRIPTION"`
		TotalBytes  string `xml:"TOTALBYTES"`
		Windows     struct {
			Arch      string `xml:"ARCH"`
			EditionID string `xml:"EDITIONID"`
			Version   struct {
				Build   string `xml:"BUILD"`
				SPBuild string `xml:"SPBUILD"`
			} `xml:"VERSION"`
			Languages struct {
				Language []string `xml:"LANGUAGE"`
				Default  string   `xml:"DEFAULT"`
			} `xml:"LANGUAGES"`
		} `xml:"WINDOWS"`
	} `xml:"IMAGE"`
}

// parseXML decodes the UTF-16LE XML resource.
func parseXML(raw []byte) ([]Image, error) {
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(raw[2*i:])
	}
	s := strings.TrimPrefix(string(utf16.Decode(u)), "\uFEFF")
	var doc xmlWIM
	if err := xml.Unmarshal([]byte(s), &doc); err != nil {
		return nil, fmt.Errorf("%w: XML: %v", ErrCorrupt, err)
	}
	out := make([]Image, 0, len(doc.Images))
	for _, x := range doc.Images {
		idx, err := strconv.Atoi(x.Index)
		if err != nil || idx < 1 {
			return nil, fmt.Errorf("%w: image index %q", ErrCorrupt, x.Index)
		}
		im := Image{
			Index: idx, Name: x.Name, DisplayName: x.DisplayName, Description: x.Description,
			EditionID: x.Windows.EditionID, Arch: arch(x.Windows.Arch),
			Languages: x.Windows.Languages.Language, DefaultLanguage: x.Windows.Languages.Default,
		}
		im.TotalBytes, _ = strconv.ParseInt(strings.TrimSpace(x.TotalBytes), 10, 64)
		if b := strings.TrimSpace(x.Windows.Version.Build); b != "" {
			im.Build = b
			if sp := strings.TrimSpace(x.Windows.Version.SPBuild); sp != "" {
				im.Build += "." + sp
			}
		}
		out = append(out, im)
	}
	slices.SortFunc(out, func(a, b Image) int { return a.Index - b.Index })
	for i, im := range out {
		if im.Index != i+1 {
			return nil, fmt.Errorf("%w: image indexes are not 1..%d", ErrCorrupt, len(out))
		}
	}
	return out, nil
}

// arch maps <ARCH> (PROCESSOR_ARCHITECTURE) to the API's names.
func arch(s string) string {
	switch strings.TrimSpace(s) {
	case "0":
		return "x86"
	case "5":
		return "arm"
	case "9":
		return "x64"
	case "12":
		return "arm64"
	}
	return ""
}

// Reader reads files from the images of an LZX or uncompressed WIM.
type Reader struct {
	w *gowim.Reader
}

// Open opens a WIM for reading files. ESD (LZMS), XPRESS and split WIMs
// give ErrUnsupported; ReadInfo still works on them.
func Open(r io.ReaderAt, size int64) (*Reader, error) {
	info, err := ReadInfo(r)
	if err != nil {
		return nil, err
	}
	if c := info.Compression; c != "lzx" && c != "none" {
		return nil, fmt.Errorf("%w: %s compression", ErrUnsupported, c)
	}
	if info.TotalParts > 1 {
		return nil, fmt.Errorf("%w: split WIM", ErrUnsupported)
	}
	w, err := gowim.Open(r, size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return &Reader{w: w}, nil
}

// ReadFile returns a file from image index (1-based), matching path
// components case-insensitively (an exact match wins), as Windows does.
func (r *Reader) ReadFile(index int, name string) ([]byte, error) {
	im, err := r.w.Image(index)
	if err != nil {
		return nil, fmt.Errorf("wim: image %d: %w", index, err)
	}
	fsys := im.FS()
	p, err := resolve(fsys, strings.Trim(strings.ReplaceAll(name, `\`, "/"), "/"))
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return fs.ReadFile(fsys, p)
}

// ReadDir lists the names in a directory of image index (1-based),
// matching path components case-insensitively; directories are included.
func (r *Reader) ReadDir(index int, dir string) ([]fs.DirEntry, error) {
	im, err := r.w.Image(index)
	if err != nil {
		return nil, fmt.Errorf("wim: image %d: %w", index, err)
	}
	fsys := im.FS()
	p, err := resolve(fsys, strings.Trim(strings.ReplaceAll(dir, `\`, "/"), "/"))
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: dir, Err: err}
	}
	return fs.ReadDir(fsys, p)
}

// resolve finds name's exact-case path.
func resolve(fsys fs.FS, name string) (string, error) {
	if _, err := fs.Stat(fsys, name); err == nil {
		return name, nil
	}
	cur := "."
	for _, part := range strings.Split(name, "/") {
		ents, err := fs.ReadDir(fsys, cur)
		if err != nil {
			return "", fs.ErrNotExist
		}
		match := ""
		for _, e := range ents {
			if e.Name() == part {
				match = part
				break
			}
			if match == "" && strings.EqualFold(e.Name(), part) {
				match = e.Name()
			}
		}
		if match == "" {
			return "", fs.ErrNotExist
		}
		if cur == "." {
			cur = match
		} else {
			cur += "/" + match
		}
	}
	return cur, nil
}
