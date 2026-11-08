// Package firmware reads and changes the UEFI boot order (ADR 0013, 0025):
// BootOrder, BootNext and the Boot#### load options, through the firmware
// variables Windows exposes.
package firmware

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Vars reads and writes EFI global variables.
type Vars interface {
	Get(name string) ([]byte, error)
	Set(name string, value []byte) error
}

// ErrNotFound: the variable doesn't exist.
var ErrNotFound = errors.New("firmware: variable not found")

// Entry is a Boot#### load option.
type Entry struct {
	Number      uint16
	Active      bool
	Description string
	Disk        bool // the device path has a hard drive media node
	Network     bool // the device path has a MAC, IPv4 or IPv6 node
	File        string
}

func (e Entry) String() string {
	return fmt.Sprintf("Boot%04X %q", e.Number, e.Description)
}

// ParseLoadOption parses an EFI_LOAD_OPTION.
func ParseLoadOption(n uint16, b []byte) (Entry, error) {
	if len(b) < 6 {
		return Entry{}, fmt.Errorf("Boot%04X: too short", n)
	}
	le := binary.LittleEndian
	e := Entry{Number: n, Active: le.Uint32(b)&1 != 0}
	pathLen := int(le.Uint16(b[4:]))
	i := 6
	var desc []uint16
	for ; i+1 < len(b); i += 2 {
		c := le.Uint16(b[i:])
		if c == 0 {
			i += 2
			break
		}
		desc = append(desc, c)
	}
	e.Description = string(utf16.Decode(desc))
	if i+pathLen > len(b) {
		return e, fmt.Errorf("Boot%04X: device path beyond the option", n)
	}
	path := b[i : i+pathLen]
	for len(path) >= 4 {
		typ, sub, l := path[0], path[1], int(le.Uint16(path[2:]))
		if l < 4 || l > len(path) {
			break
		}
		switch {
		case typ == 0x7F && sub == 0xFF: // end of the whole path
			path = nil
			continue
		case typ == 4 && sub == 1: // media: hard drive
			e.Disk = true
		case typ == 4 && sub == 4: // media: file path
			u := make([]uint16, (l-4)/2)
			for j := range u {
				u[j] = le.Uint16(path[4+j*2:])
			}
			e.File = strings.TrimRight(string(utf16.Decode(u)), "\x00")
		case typ == 3 && (sub == 11 || sub == 12 || sub == 13): // messaging: MAC, IPv4, IPv6
			e.Network = true
		}
		path = path[l:]
	}
	return e, nil
}

// Entries reads BootOrder and the load options it lists, in order.
func Entries(v Vars) ([]Entry, error) {
	order, err := bootOrder(v)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, n := range order {
		b, err := v.Get(fmt.Sprintf("Boot%04X", n))
		if err != nil {
			continue // listed but missing: firmware skips it too
		}
		e, err := ParseLoadOption(n, b)
		if err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func bootOrder(v Vars) ([]uint16, error) {
	b, err := v.Get("BootOrder")
	if err != nil {
		return nil, fmt.Errorf("read BootOrder: %w", err)
	}
	out := make([]uint16, len(b)/2)
	for i := range out {
		out[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return out, nil
}

func encodeOrder(order []uint16) []byte {
	b := make([]byte, len(order)*2)
	for i, n := range order {
		binary.LittleEndian.PutUint16(b[i*2:], n)
	}
	return b
}

// LocalDisk is the entry a prompt timeout boots: the first active entry on
// a hard drive named "Windows Boot Manager", else the first active one on a
// hard drive. ok=false: none.
func LocalDisk(entries []Entry) (Entry, bool) {
	var first *Entry
	for i, e := range entries {
		if !e.Active || !e.Disk {
			continue
		}
		if e.Description == "Windows Boot Manager" {
			return e, true
		}
		if first == nil {
			first = &entries[i]
		}
	}
	if first != nil {
		return *first, true
	}
	return Entry{}, false
}

// SetBootNext makes the next boot (only) use entry n.
func SetBootNext(v Vars, n uint16) error {
	return v.Set("BootNext", encodeOrder([]uint16{n}))
}

// NetworkFirst moves the network entries to the front of BootOrder, keeping
// the rest in order (profile bootOrderAfterDeploy: network-first). It
// returns the new order.
func NetworkFirst(v Vars) ([]Entry, error) {
	entries, err := Entries(v)
	if err != nil {
		return nil, err
	}
	order, err := bootOrder(v)
	if err != nil {
		return nil, err
	}
	network := map[uint16]bool{}
	var reordered []Entry
	for _, e := range entries {
		if e.Network {
			network[e.Number] = true
			reordered = append(reordered, e)
		}
	}
	if len(network) == 0 {
		return entries, errors.New("no network boot entry in BootOrder")
	}
	var next []uint16
	for _, e := range reordered {
		next = append(next, e.Number)
	}
	for _, n := range order {
		if !network[n] {
			next = append(next, n)
		}
	}
	for _, e := range entries {
		if !e.Network {
			reordered = append(reordered, e)
		}
	}
	return reordered, v.Set("BootOrder", encodeOrder(next))
}
