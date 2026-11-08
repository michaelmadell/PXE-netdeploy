// Package bcd reads and writes Boot Configuration Data stores (ADR 0023):
// registry hive files (REGF) holding Boot Manager's objects and elements.
// It writes only what a network-booted WinPE needs, deterministically and
// without bcdedit, hivex or privileges.
package bcd

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Registry value types.
const (
	RegSZ      = 1
	RegBinary  = 3
	RegDWORD   = 4
	RegMultiSZ = 7
	RegQWORD   = 11
)

// Key is a registry key: a name, values and subkeys.
type Key struct {
	Name    string
	Values  []Value
	Subkeys []*Key
}

// Value is a named registry value.
type Value struct {
	Name string
	Type uint32
	Data []byte
}

// Subkey returns the subkey named name (case-insensitively), or nil.
func (k *Key) Subkey(name string) *Key {
	for _, s := range k.Subkeys {
		if strings.EqualFold(s.Name, name) {
			return s
		}
	}
	return nil
}

// Value returns the value named name (case-insensitively).
func (k *Key) Value(name string) (Value, bool) {
	for _, v := range k.Values {
		if strings.EqualFold(v.Name, name) {
			return v, true
		}
	}
	return Value{}, false
}

// Hive is a parsed hive file.
type Hive struct {
	Root     *Key
	Major    uint32
	Minor    uint32
	Security []byte // the root key's security descriptor
}

// ErrCorrupt: the file isn't a well-formed hive.
var ErrCorrupt = errors.New("bcd: corrupt hive")

func corrupt(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, a...))
}

const (
	baseBlockSize = 4096
	noCell        = 0xFFFFFFFF
	flagCompName  = 0x20 // key name stored as ASCII/Latin-1
	valCompName   = 0x01
)

type reader struct {
	b    []byte // hive bins (offsets are relative to this)
	seen int
}

// ParseHive parses a hive file. It checks the base block (signature,
// sequence numbers, checksum) and follows the cells from the root.
func ParseHive(b []byte) (*Hive, error) {
	if len(b) < baseBlockSize || string(b[:4]) != "regf" {
		return nil, corrupt("no regf signature")
	}
	le := binary.LittleEndian
	if le.Uint32(b[4:]) != le.Uint32(b[8:]) {
		return nil, corrupt("sequence numbers differ (the hive needs its transaction log)")
	}
	if got, want := checksum(b[:508]), le.Uint32(b[508:]); got != want {
		return nil, corrupt("base block checksum %#x, want %#x", want, got)
	}
	size := le.Uint32(b[40:])
	if int(size) > len(b)-baseBlockSize {
		return nil, corrupt("hive bins size %d beyond the file", size)
	}
	r := &reader{b: b[baseBlockSize : baseBlockSize+int(size)]}
	h := &Hive{Major: le.Uint32(b[20:]), Minor: le.Uint32(b[24:])}
	root, sec, err := r.key(le.Uint32(b[36:]), 0)
	if err != nil {
		return nil, err
	}
	h.Root, h.Security = root, sec
	return h, nil
}

// checksum is the base block checksum: XOR of its first 127 dwords, with
// 0 and 0xFFFFFFFF avoided.
func checksum(b []byte) uint32 {
	var x uint32
	for i := 0; i+4 <= len(b); i += 4 {
		x ^= binary.LittleEndian.Uint32(b[i:])
	}
	switch x {
	case 0xFFFFFFFF:
		return 0xFFFFFFFE
	case 0:
		return 1
	}
	return x
}

// cell returns the data of the allocated cell at off.
func (r *reader) cell(off uint32) ([]byte, error) {
	if r.seen++; r.seen > 100000 {
		return nil, corrupt("too many cells (loop?)")
	}
	if int(off)+4 > len(r.b) || off%8 != 0 {
		return nil, corrupt("cell offset %#x out of range", off)
	}
	size := int32(binary.LittleEndian.Uint32(r.b[off:]))
	if size >= 0 {
		return nil, corrupt("cell %#x is free", off)
	}
	n := int(-size)
	if n < 8 || int(off)+n > len(r.b) {
		return nil, corrupt("cell %#x size %d", off, n)
	}
	return r.b[off+4 : int(off)+n], nil
}

func (r *reader) key(off uint32, depth int) (*Key, []byte, error) {
	if depth > 64 {
		return nil, nil, corrupt("keys nested too deep")
	}
	c, err := r.cell(off)
	if err != nil {
		return nil, nil, err
	}
	if len(c) < 0x4C || string(c[:2]) != "nk" {
		return nil, nil, corrupt("cell %#x is not a key", off)
	}
	le := binary.LittleEndian
	flags := le.Uint16(c[2:])
	nameLen := int(le.Uint16(c[0x48:]))
	if 0x4C+nameLen > len(c) {
		return nil, nil, corrupt("key name at %#x", off)
	}
	k := &Key{Name: decodeName(c[0x4C:0x4C+nameLen], flags&flagCompName != 0)}

	var sec []byte
	if so := le.Uint32(c[0x2C:]); so != noCell {
		sc, err := r.cell(so)
		if err != nil {
			return nil, nil, err
		}
		if len(sc) >= 0x14 && string(sc[:2]) == "sk" {
			n := int(le.Uint32(sc[0x10:]))
			if 0x14+n <= len(sc) {
				sec = sc[0x14 : 0x14+n]
			}
		}
	}

	if n := le.Uint32(c[0x24:]); n > 0 {
		list, err := r.cell(le.Uint32(c[0x28:]))
		if err != nil {
			return nil, nil, err
		}
		if int(n)*4 > len(list) {
			return nil, nil, corrupt("value list of %q", k.Name)
		}
		for i := 0; i < int(n); i++ {
			v, err := r.value(le.Uint32(list[i*4:]))
			if err != nil {
				return nil, nil, err
			}
			k.Values = append(k.Values, v)
		}
	}

	if n := le.Uint32(c[0x14:]); n > 0 {
		offs, err := r.subkeyList(le.Uint32(c[0x1C:]), 0)
		if err != nil {
			return nil, nil, err
		}
		if len(offs) != int(n) {
			return nil, nil, corrupt("key %q has %d subkeys, list has %d", k.Name, n, len(offs))
		}
		for _, so := range offs {
			sub, _, err := r.key(so, depth+1)
			if err != nil {
				return nil, nil, err
			}
			k.Subkeys = append(k.Subkeys, sub)
		}
	}
	return k, sec, nil
}

func (r *reader) subkeyList(off uint32, depth int) ([]uint32, error) {
	if depth > 2 {
		return nil, corrupt("subkey index nested too deep")
	}
	c, err := r.cell(off)
	if err != nil {
		return nil, err
	}
	if len(c) < 4 {
		return nil, corrupt("subkey list %#x", off)
	}
	le := binary.LittleEndian
	n := int(le.Uint16(c[2:]))
	var out []uint32
	switch sig := string(c[:2]); sig {
	case "lf", "lh":
		if 4+n*8 > len(c) {
			return nil, corrupt("subkey list %#x", off)
		}
		for i := 0; i < n; i++ {
			out = append(out, le.Uint32(c[4+i*8:]))
		}
	case "li":
		if 4+n*4 > len(c) {
			return nil, corrupt("subkey list %#x", off)
		}
		for i := 0; i < n; i++ {
			out = append(out, le.Uint32(c[4+i*4:]))
		}
	case "ri":
		if 4+n*4 > len(c) {
			return nil, corrupt("subkey index %#x", off)
		}
		for i := 0; i < n; i++ {
			sub, err := r.subkeyList(le.Uint32(c[4+i*4:]), depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
		}
	default:
		return nil, corrupt("unknown subkey list %q at %#x", sig, off)
	}
	return out, nil
}

func (r *reader) value(off uint32) (Value, error) {
	c, err := r.cell(off)
	if err != nil {
		return Value{}, err
	}
	if len(c) < 0x14 || string(c[:2]) != "vk" {
		return Value{}, corrupt("cell %#x is not a value", off)
	}
	le := binary.LittleEndian
	nameLen := int(le.Uint16(c[2:]))
	if 0x14+nameLen > len(c) {
		return Value{}, corrupt("value name at %#x", off)
	}
	v := Value{Name: decodeName(c[0x14:0x14+nameLen], le.Uint16(c[0x10:])&valCompName != 0), Type: le.Uint32(c[0x0C:])}
	size := le.Uint32(c[4:])
	switch {
	case size&0x80000000 != 0: // inline in the offset field
		n := size &^ 0x80000000
		if n > 4 {
			return Value{}, corrupt("inline value %q of %d bytes", v.Name, n)
		}
		v.Data = append([]byte{}, c[8:8+n]...)
	case size > 0:
		d, err := r.cell(le.Uint32(c[8:]))
		if err != nil {
			return Value{}, err
		}
		if len(d) >= 2 && string(d[:2]) == "db" {
			return Value{}, corrupt("value %q uses big data cells (not supported)", v.Name)
		}
		if int(size) > len(d) {
			return Value{}, corrupt("value %q data", v.Name)
		}
		v.Data = append([]byte{}, d[:size]...)
	}
	return v, nil
}

func decodeName(b []byte, compressed bool) string {
	if compressed {
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c) // Latin-1
		}
		return string(r)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}

// Dump renders a key tree as indented text (for tests and findings).
func Dump(k *Key) string {
	var sb strings.Builder
	dump(&sb, k, 0)
	return sb.String()
}

func dump(sb *strings.Builder, k *Key, depth int) {
	ind := strings.Repeat("  ", depth)
	fmt.Fprintf(sb, "%s[%s]\n", ind, k.Name)
	for _, v := range k.Values {
		fmt.Fprintf(sb, "%s  %s = %s\n", ind, v.Name, FormatValue(v))
	}
	for _, s := range k.Subkeys {
		dump(sb, s, depth+1)
	}
}

// FormatValue renders a value's data by type.
func FormatValue(v Value) string {
	le := binary.LittleEndian
	switch v.Type {
	case RegSZ:
		return fmt.Sprintf("REG_SZ %q", utf16String(v.Data))
	case RegMultiSZ:
		var parts []string
		for _, s := range strings.Split(utf16String(v.Data), "\x00") {
			if s != "" {
				parts = append(parts, fmt.Sprintf("%q", s))
			}
		}
		return "REG_MULTI_SZ [" + strings.Join(parts, " ") + "]"
	case RegDWORD:
		if len(v.Data) == 4 {
			return fmt.Sprintf("REG_DWORD %#x", le.Uint32(v.Data))
		}
	case RegQWORD:
		if len(v.Data) == 8 {
			return fmt.Sprintf("REG_QWORD %#x", le.Uint64(v.Data))
		}
	}
	return fmt.Sprintf("type %d % x", v.Type, v.Data)
}

func utf16String(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return strings.TrimRight(string(utf16.Decode(u)), "\x00")
}
