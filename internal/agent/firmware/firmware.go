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
	// Partition is the hard drive node's partition (GPT signature only;
	// zero GUID otherwise).
	Partition Partition
}

func (e Entry) String() string {
	return fmt.Sprintf("Boot%04X %q", e.Number, e.Description)
}

// Detail is the entry on one line, for logs.
func (e Entry) Detail() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Boot%04X %q active=%t", e.Number, e.Description, e.Active)
	if e.Disk {
		fmt.Fprintf(&b, " disk partition=%d guid=%s", e.Partition.Number, e.Partition.GUID)
	}
	if e.Network {
		b.WriteString(" network")
	}
	if e.File != "" {
		fmt.Fprintf(&b, " file=%s", e.File)
	}
	return b.String()
}

// GUID is a GUID in its EFI byte order (the first three fields little-endian).
type GUID [16]byte

func (g GUID) String() string {
	le := binary.LittleEndian
	return fmt.Sprintf("%08X-%04X-%04X-%X-%X", le.Uint32(g[0:]), le.Uint16(g[4:]), le.Uint16(g[6:]), g[8:10], g[10:])
}

// Partition is a GPT partition as a hard drive device path node names it.
type Partition struct {
	Number   uint32
	StartLBA uint64
	SizeLBA  uint64
	GUID     GUID
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
			if l >= 42 && path[41] == 2 { // GPT signature
				e.Partition.Number = le.Uint32(path[4:])
				e.Partition.StartLBA = le.Uint64(path[8:])
				e.Partition.SizeLBA = le.Uint64(path[16:])
				copy(e.Partition.GUID[:], path[24:40])
			}
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

// LocalDisk is the entry a prompt timeout boots. With present (the GPT
// partition GUIDs on the disks the agent can see), it first looks for an
// entry whose partition exists: an active "Windows Boot Manager" one, then
// any active disk entry. Otherwise, or with present nil, it takes the first
// active entry on a hard drive named "Windows Boot Manager", else the first
// active one on a hard drive: an entry for a disk WinPE can't see may still
// boot. ok=false: none.
func LocalDisk(entries []Entry, present map[GUID]bool) (Entry, bool) {
	pick := func(ok func(Entry) bool) (Entry, bool) {
		var first *Entry
		for i, e := range entries {
			if !e.Active || !e.Disk || !ok(e) {
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
	if len(present) > 0 {
		if e, ok := pick(func(e Entry) bool { return present[e.Partition.GUID] }); ok {
			return e, true
		}
	}
	return pick(func(Entry) bool { return true })
}

// RemoveStale deletes the disk entries whose partition GUID is in gone and
// takes them out of BootOrder; gone should hold only partitions known to have
// been destroyed (the target disk's, before partitioning, and on no disk
// now). It returns what it removed.
func RemoveStale(v Vars, gone map[GUID]bool) ([]Entry, error) {
	entries, _, err := All(v)
	if err != nil {
		return nil, err
	}
	drop := map[uint16]bool{}
	var removed []Entry
	for _, e := range entries {
		if e.Disk && e.Partition.GUID != (GUID{}) && gone[e.Partition.GUID] {
			drop[e.Number] = true
			removed = append(removed, e)
		}
	}
	if len(drop) == 0 {
		return nil, nil
	}
	order, err := bootOrder(v)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	var next []uint16
	for _, n := range order {
		if !drop[n] {
			next = append(next, n)
		}
	}
	if err := v.Set("BootOrder", encodeOrder(next)); err != nil {
		return nil, err
	}
	for _, e := range removed {
		if err := v.Set(fmt.Sprintf("Boot%04X", e.Number), nil); err != nil { // empty value deletes
			return removed, err
		}
	}
	return removed, nil
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

// maxProbe is how many Boot#### numbers All probes beyond BootOrder.
const maxProbe = 0x100

// All reads every load option: those BootOrder lists, in order, then any
// other Boot0000–Boot00FF. listed reports which were in BootOrder.
func All(v Vars) (entries []Entry, listed map[uint16]bool, err error) {
	order, err := bootOrder(v)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, nil, err
	}
	listed = map[uint16]bool{}
	read := func(n uint16) {
		b, err := v.Get(fmt.Sprintf("Boot%04X", n))
		if err != nil {
			return
		}
		if e, err := ParseLoadOption(n, b); err == nil {
			entries = append(entries, e)
		}
	}
	for _, n := range order {
		listed[n] = true
		read(n)
	}
	for n := uint16(0); n < maxProbe; n++ {
		if !listed[n] {
			read(n)
		}
	}
	return entries, listed, nil
}

// Dump describes the boot variables, one line each, for logs.
func Dump(v Vars) []string {
	var out []string
	for _, name := range []string{"BootCurrent", "BootNext", "BootOrder"} {
		b, err := v.Get(name)
		switch {
		case errors.Is(err, ErrNotFound):
			out = append(out, name+": not set")
		case err != nil:
			out = append(out, fmt.Sprintf("%s: %v", name, err))
		default:
			var nums []string
			for i := 0; i+1 < len(b); i += 2 {
				nums = append(nums, fmt.Sprintf("%04X", binary.LittleEndian.Uint16(b[i:])))
			}
			out = append(out, name+": "+strings.Join(nums, ","))
		}
	}
	entries, listed, err := All(v)
	if err != nil {
		return append(out, "entries: "+err.Error())
	}
	for _, e := range entries {
		line := e.Detail()
		if !listed[e.Number] {
			line += " (not in BootOrder)"
		}
		out = append(out, line)
	}
	return out
}

// LoadOption builds an active EFI_LOAD_OPTION that starts file on a GPT
// partition.
func LoadOption(desc string, p Partition, file string) []byte {
	le := binary.LittleEndian
	hd := make([]byte, 42)
	hd[0], hd[1] = 4, 1
	le.PutUint16(hd[2:], 42)
	le.PutUint32(hd[4:], p.Number)
	le.PutUint64(hd[8:], p.StartLBA)
	le.PutUint64(hd[16:], p.SizeLBA)
	copy(hd[24:40], p.GUID[:])
	hd[40], hd[41] = 2, 2 // GPT partition table, GUID signature
	name := utf16.Encode([]rune(file + "\x00"))
	fp := make([]byte, 4, 4+2*len(name))
	fp[0], fp[1] = 4, 4
	le.PutUint16(fp[2:], uint16(4+2*len(name)))
	for _, c := range name {
		fp = le.AppendUint16(fp, c)
	}
	path := append(append(hd, fp...), 0x7F, 0xFF, 4, 0)

	b := make([]byte, 6)
	le.PutUint32(b, 1) // LOAD_OPTION_ACTIVE
	le.PutUint16(b[4:], uint16(len(path)))
	for _, c := range utf16.Encode([]rune(desc)) {
		b = le.AppendUint16(b, c)
	}
	b = append(b, 0, 0)
	return append(b, path...)
}

// EnsureDiskEntry returns the load option that starts file on partition p,
// creating it (as desc, at the lowest free number) when none exists. bcdboot
// normally makes it, but not always from a network-booted WinPE.
func EnsureDiskEntry(v Vars, desc string, p Partition, file string) (e Entry, created bool, err error) {
	entries, _, err := All(v)
	if err != nil {
		return Entry{}, false, err
	}
	used := map[uint16]bool{}
	for _, e := range entries {
		used[e.Number] = true
		if e.Disk && e.Partition.GUID == p.GUID && strings.EqualFold(e.File, file) {
			if !e.Active {
				continue
			}
			return e, false, nil
		}
	}
	n := uint16(0)
	for used[n] {
		n++
	}
	if n >= maxProbe {
		return Entry{}, false, errors.New("no free Boot#### number")
	}
	b := LoadOption(desc, p, file)
	if err := v.Set(fmt.Sprintf("Boot%04X", n), b); err != nil {
		return Entry{}, false, err
	}
	e, err = ParseLoadOption(n, b)
	return e, true, err
}

// First puts entry n at the front of BootOrder, keeping the rest in order
// (profile bootOrderAfterDeploy: disk-first).
func First(v Vars, n uint16) error {
	order, err := bootOrder(v)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	next := []uint16{n}
	for _, m := range order {
		if m != n {
			next = append(next, m)
		}
	}
	return v.Set("BootOrder", encodeOrder(next))
}
