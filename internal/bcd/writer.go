package bcd

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

// WriteOptions control WriteHive.
type WriteOptions struct {
	// Security is the self-relative security descriptor every key gets.
	// Nil: DefaultSecurity.
	Security []byte
	// Timestamp is the FILETIME written for every key and the base block
	// (fixed, so equal inputs give equal bytes).
	Timestamp uint64
	// FileName is recorded in the base block (informational).
	FileName string
}

// DefaultSecurity owns every key by Administrators and allows
// Administrators read and SYSTEM full access, as bcdedit's stores do (with
// Administrators as the group instead of the creating user's SID).
var DefaultSecurity = []byte{
	0x01, 0x00, 0x04, 0x80, // revision 1, SE_SELF_RELATIVE | SE_DACL_PRESENT
	0x48, 0x00, 0x00, 0x00, // owner
	0x58, 0x00, 0x00, 0x00, // group
	0x00, 0x00, 0x00, 0x00, // no SACL
	0x14, 0x00, 0x00, 0x00, // DACL
	// DACL: revision 2, size 0x34, 2 ACEs.
	0x02, 0x00, 0x34, 0x00, 0x02, 0x00, 0x00, 0x00,
	// ACCESS_ALLOWED 0x00060019 (KEY_READ | WRITE_DAC | WRITE_OWNER) to S-1-5-32-544.
	0x00, 0x00, 0x18, 0x00, 0x19, 0x00, 0x06, 0x00,
	0x01, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0x20, 0x00, 0x00, 0x00, 0x20, 0x02, 0x00, 0x00,
	// ACCESS_ALLOWED 0x000F003F (KEY_ALL_ACCESS) to S-1-5-18.
	0x00, 0x00, 0x14, 0x00, 0x3f, 0x00, 0x0f, 0x00,
	0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0x12, 0x00, 0x00, 0x00,
	// Owner and group: S-1-5-32-544 (Administrators).
	0x01, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0x20, 0x00, 0x00, 0x00, 0x20, 0x02, 0x00, 0x00,
	0x01, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0x20, 0x00, 0x00, 0x00, 0x20, 0x02, 0x00, 0x00,
}

// writer lays cells out in one hive bin, in allocation order.
type writer struct {
	cells []byte // cell area: offsets are 32 (the bin header) + index
	opts  WriteOptions
	sk    uint32
	keys  uint32
}

const binHeader = 32

func align(n, to int) int { return (n + to - 1) / to * to }

// alloc reserves an allocated cell for n data bytes and returns its offset
// (relative to the first hive bin, as cells reference each other).
func (w *writer) alloc(n int) uint32 {
	size := align(4+n, 8)
	off := uint32(binHeader + len(w.cells))
	cell := make([]byte, size)
	binary.LittleEndian.PutUint32(cell, uint32(int32(-size)))
	w.cells = append(w.cells, cell...)
	return off
}

// data returns the data bytes of the cell at off.
func (w *writer) data(off uint32) []byte {
	i := int(off) - binHeader
	size := int(-int32(binary.LittleEndian.Uint32(w.cells[i:])))
	return w.cells[i+4 : i+size]
}

func (w *writer) put(b []byte) uint32 {
	off := w.alloc(len(b))
	copy(w.data(off), b)
	return off
}

// nameBytes encodes a key or value name: Latin-1 when possible (the
// "compressed" flag), else UTF-16LE.
func nameBytes(s string) ([]byte, bool) {
	var b []byte
	for _, r := range s {
		if r > 0xFF {
			u := utf16.Encode([]rune(s))
			out := make([]byte, len(u)*2)
			for i, c := range u {
				binary.LittleEndian.PutUint16(out[i*2:], c)
			}
			return out, false
		}
		b = append(b, byte(r))
	}
	return b, true
}

// utf16Len is a name's length in UTF-16 bytes (the nk "largest name"
// fields count those, whatever the storage).
func utf16Len(s string) uint32 { return uint32(len(utf16.Encode([]rune(s))) * 2) }

// sortedSubkeys orders subkeys as the registry requires: by upper-cased
// name.
func sortedSubkeys(k *Key) []*Key {
	subs := append([]*Key{}, k.Subkeys...)
	sort.SliceStable(subs, func(i, j int) bool { return strings.ToUpper(subs[i].Name) < strings.ToUpper(subs[j].Name) })
	return subs
}

func (w *writer) key(k *Key, parent uint32) (uint32, error) {
	name, comp := nameBytes(k.Name)
	if len(name) == 0 || len(name) > 255*2 {
		return 0, fmt.Errorf("bcd: invalid key name %q", k.Name)
	}
	off := w.alloc(0x4C + len(name))
	w.keys++
	if parent == noCell { // the root: the security cell follows it
		sd := w.opts.Security
		w.sk = w.alloc(0x14 + len(sd))
		c := w.data(w.sk)
		copy(c, "sk")
		le := binary.LittleEndian
		le.PutUint32(c[4:], w.sk) // flink and blink: a list of one
		le.PutUint32(c[8:], w.sk)
		le.PutUint32(c[0x10:], uint32(len(sd)))
		copy(c[0x14:], sd)
	}

	// Values, then their list.
	var maxValName, maxValData uint32
	valueList := uint32(noCell)
	if len(k.Values) > 0 {
		offs := make([]uint32, len(k.Values))
		for i, v := range k.Values {
			vo, err := w.value(v)
			if err != nil {
				return 0, fmt.Errorf("bcd: %s: %w", k.Name, err)
			}
			offs[i] = vo
			maxValName = max(maxValName, utf16Len(v.Name))
			maxValData = max(maxValData, uint32(len(v.Data)))
		}
		list := make([]byte, 4*len(offs))
		for i, o := range offs {
			binary.LittleEndian.PutUint32(list[i*4:], o)
		}
		valueList = w.put(list)
	}

	// Subkeys, then an "lf" list with 4-character name hints.
	subs := sortedSubkeys(k)
	for i := 1; i < len(subs); i++ {
		if strings.EqualFold(subs[i-1].Name, subs[i].Name) {
			return 0, fmt.Errorf("bcd: %s has two subkeys named %q", k.Name, subs[i].Name)
		}
	}
	var maxSubName uint32
	subList := uint32(noCell)
	if len(subs) > 0 {
		offs := make([]uint32, len(subs))
		for i, s := range subs {
			so, err := w.key(s, off)
			if err != nil {
				return 0, err
			}
			offs[i] = so
			maxSubName = max(maxSubName, utf16Len(s.Name))
		}
		list := make([]byte, 4+8*len(offs))
		copy(list, "lf")
		binary.LittleEndian.PutUint16(list[2:], uint16(len(offs)))
		for i, o := range offs {
			binary.LittleEndian.PutUint32(list[4+i*8:], o)
			hint, _ := nameBytes(subs[i].Name)
			copy(list[8+i*8:12+i*8], hint) // first 4 bytes, zero-padded
		}
		subList = w.put(list)
	}

	c := w.data(off)
	le := binary.LittleEndian
	copy(c, "nk")
	flags := uint16(0)
	if comp {
		flags |= flagCompName
	}
	if parent == noCell {
		flags |= 0x04 | 0x08 // KEY_HIVE_ENTRY | KEY_NO_DELETE
	}
	le.PutUint16(c[2:], flags)
	le.PutUint64(c[4:], w.opts.Timestamp)
	le.PutUint32(c[0x10:], parent)
	le.PutUint32(c[0x14:], uint32(len(subs)))
	le.PutUint32(c[0x1C:], subList)
	le.PutUint32(c[0x20:], noCell) // volatile subkeys
	le.PutUint32(c[0x24:], uint32(len(k.Values)))
	le.PutUint32(c[0x28:], valueList)
	le.PutUint32(c[0x2C:], w.sk)
	le.PutUint32(c[0x30:], noCell) // class name
	le.PutUint32(c[0x34:], maxSubName)
	le.PutUint32(c[0x3C:], maxValName)
	le.PutUint32(c[0x40:], maxValData)
	le.PutUint16(c[0x48:], uint16(len(name)))
	copy(c[0x4C:], name)
	return off, nil
}

func (w *writer) value(v Value) (uint32, error) {
	name, comp := nameBytes(v.Name)
	if len(v.Data) > 16344 {
		return 0, fmt.Errorf("value %q: %d bytes needs big data cells", v.Name, len(v.Data))
	}
	off := w.alloc(0x14 + len(name))
	var dataOff uint32
	size := uint32(len(v.Data))
	var inline [4]byte
	switch {
	case len(v.Data) <= 4 && len(v.Data) > 0:
		copy(inline[:], v.Data)
		dataOff = binary.LittleEndian.Uint32(inline[:])
		size |= 0x80000000
	case len(v.Data) == 0:
		dataOff = noCell
	default:
		dataOff = w.put(v.Data)
	}
	c := w.data(off)
	le := binary.LittleEndian
	copy(c, "vk")
	le.PutUint16(c[2:], uint16(len(name)))
	le.PutUint32(c[4:], size)
	le.PutUint32(c[8:], dataOff)
	le.PutUint32(c[0x0C:], v.Type)
	if comp {
		le.PutUint16(c[0x10:], valCompName)
	}
	copy(c[0x14:], name)
	return off, nil
}

// WriteHive serializes root as a version 1.3 hive file: a base block and
// one hive bin, with consistent sequence numbers (no transaction log
// needed). The output depends only on root and opts.
func WriteHive(root *Key, opts WriteOptions) ([]byte, error) {
	if opts.Security == nil {
		opts.Security = DefaultSecurity
	}
	w := &writer{opts: opts}
	rootOff, err := w.key(root, noCell)
	if err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint32(w.data(w.sk)[0x0C:], w.keys) // reference count

	binSize := align(binHeader+len(w.cells), 4096)
	bin := make([]byte, binSize)
	le := binary.LittleEndian
	copy(bin, "hbin")
	le.PutUint32(bin[8:], uint32(binSize))
	le.PutUint64(bin[0x14:], opts.Timestamp)
	copy(bin[binHeader:], w.cells)
	if free := binSize - binHeader - len(w.cells); free > 0 {
		le.PutUint32(bin[binHeader+len(w.cells):], uint32(free)) // one free cell
	}

	base := make([]byte, baseBlockSize)
	copy(base, "regf")
	le.PutUint32(base[4:], 1) // primary and secondary sequence numbers
	le.PutUint32(base[8:], 1)
	le.PutUint64(base[12:], opts.Timestamp)
	le.PutUint32(base[20:], 1) // version 1.3
	le.PutUint32(base[24:], 3)
	le.PutUint32(base[28:], 0) // primary file
	le.PutUint32(base[32:], 1) // direct memory load
	le.PutUint32(base[36:], rootOff)
	le.PutUint32(base[40:], uint32(binSize))
	le.PutUint32(base[44:], 1) // clustering factor
	fn := utf16.Encode([]rune(opts.FileName))
	for i := 0; i < len(fn) && i < 31; i++ {
		le.PutUint16(base[48+i*2:], fn[i])
	}
	le.PutUint32(base[508:], checksum(base[:508]))
	return append(base, bin...), nil
}
