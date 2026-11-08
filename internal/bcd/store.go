package bcd

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Well-known BCD object identifiers.
const (
	BootMgrID        = "{9dea862c-5cdd-4e70-acc1-f32b344d4795}" // {bootmgr}
	RAMDiskOptionsID = "{ae5534e0-a924-466c-b836-758539a3ee3a}" // {ramdiskoptions}
	// WinPEID is our WinPE loader entry. bcdedit picks a random GUID; a
	// fixed one keeps a boot image's BCD byte-for-byte reproducible.
	WinPEID = "{6474656e-7065-6f6c-792d-77696e706521}"
)

// Object types (Description\Type).
const (
	TypeBootMgr    = 0x10100002 // firmware application: Windows Boot Manager
	TypeOSLoader   = 0x10200003 // boot application: Windows OS loader
	TypeDeviceOpts = 0x30000000 // device options (here {ramdiskoptions})
)

// Element types (Elements\<type>), as bcdedit names them.
const (
	ElemDevice         = 0x11000001 // device
	ElemPath           = 0x12000002 // path
	ElemDescription    = 0x12000004 // description
	ElemOSDevice       = 0x21000001 // osdevice
	ElemSystemRoot     = 0x22000002 // systemroot
	ElemDefault        = 0x23000003 // default
	ElemDisplayOrder   = 0x24000001 // displayorder
	ElemTimeout        = 0x25000004 // timeout
	ElemDetectHAL      = 0x26000010 // detecthal
	ElemWinPE          = 0x26000022 // winpe
	ElemRAMDiskSDIDev  = 0x31000003 // ramdisksdidevice
	ElemRAMDiskSDIPath = 0x32000004 // ramdisksdipath
	ElemRAMDiskTFTPBlk = 0x35000007 // ramdisktftpblocksize
	ElemRAMDiskTFTPWin = 0x35000008 // ramdisktftpwindowsize
)

// RAMDisk describes a BCD that boots WinPE from a WIM loaded as a RAM disk
// over TFTP (Phase 1 recipe; ADR 0006, 0023).
type RAMDisk struct {
	WIMPath        string // e.g. \Boot\boot.wim
	SDIPath        string // e.g. \Boot\boot.sdi
	TFTPBlockSize  int    // ramdisktftpblocksize Boot Manager requests
	TFTPWindowSize int    // ramdisktftpwindowsize
	Description    string // the loader entry's description
	TimeoutSeconds int    // Boot Manager timeout (no menu with one entry)
}

// Store returns the BCD key tree. The elements match the Phase 1 store
// that booted under Secure Boot (testdata/phase1.bcd, Phase 4 findings),
// apart from WinPEID.
func (r RAMDisk) Store() (*Key, error) {
	if !strings.HasPrefix(r.WIMPath, `\`) || !strings.HasPrefix(r.SDIPath, `\`) {
		return nil, fmt.Errorf("bcd: paths must start with \\: %q, %q", r.WIMPath, r.SDIPath)
	}
	if r.TFTPBlockSize < 512 || r.TFTPBlockSize > 65464 || r.TFTPWindowSize < 1 || r.TFTPWindowSize > 65535 {
		return nil, fmt.Errorf("bcd: TFTP block size %d / window size %d out of range", r.TFTPBlockSize, r.TFTPWindowSize)
	}
	device := ramdiskDevice(r.WIMPath)
	objects := &Key{Name: "Objects", Subkeys: []*Key{
		object(BootMgrID, TypeBootMgr,
			element(ElemDescription, sz("Boot Manager")),
			element(ElemDefault, sz(WinPEID)),
			element(ElemDisplayOrder, multiSZ(WinPEID)),
			element(ElemTimeout, integer(uint64(r.TimeoutSeconds))),
		),
		object(WinPEID, TypeOSLoader,
			element(ElemDevice, binaryValue(device)),
			element(ElemPath, sz(`\windows\system32\winload.efi`)),
			element(ElemDescription, sz(r.Description)),
			element(ElemOSDevice, binaryValue(device)),
			element(ElemSystemRoot, sz(`\windows`)),
			element(ElemDetectHAL, boolean(true)),
			element(ElemWinPE, boolean(true)),
		),
		object(RAMDiskOptionsID, TypeDeviceOpts,
			element(ElemDescription, sz("Ramdisk options")),
			element(ElemRAMDiskSDIDev, binaryValue(bootDevice())),
			element(ElemRAMDiskSDIPath, sz(r.SDIPath)),
			element(ElemRAMDiskTFTPBlk, integer(uint64(r.TFTPBlockSize))),
			element(ElemRAMDiskTFTPWin, integer(uint64(r.TFTPWindowSize))),
		),
	}}
	desc := &Key{Name: "Description", Values: []Value{
		{Name: "KeyName", Type: RegSZ, Data: utf16z("BCD00000001")},
		{Name: "FirmwareModified", Type: RegDWORD, Data: []byte{1, 0, 0, 0}},
	}}
	return &Key{Name: "NewStoreRoot", Subkeys: []*Key{desc, objects}}, nil
}

// Bytes returns the BCD store file.
func (r RAMDisk) Bytes() ([]byte, error) {
	root, err := r.Store()
	if err != nil {
		return nil, err
	}
	return WriteHive(root, WriteOptions{FileName: "netdeploy BCD"})
}

func object(id string, typ uint32, elems ...*Key) *Key {
	return &Key{Name: id, Subkeys: []*Key{
		{Name: "Description", Values: []Value{{Name: "Type", Type: RegDWORD, Data: le32(typ)}}},
		{Name: "Elements", Subkeys: elems},
	}}
}

func element(typ uint32, v Value) *Key {
	v.Name = "Element"
	return &Key{Name: fmt.Sprintf("%08X", typ), Values: []Value{v}}
}

func sz(s string) Value          { return Value{Type: RegSZ, Data: utf16z(s)} }
func binaryValue(b []byte) Value { return Value{Type: RegBinary, Data: b} }
func integer(n uint64) Value {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, n)
	return Value{Type: RegBinary, Data: b}
}
func boolean(b bool) Value {
	if b {
		return Value{Type: RegBinary, Data: []byte{1}}
	}
	return Value{Type: RegBinary, Data: []byte{0}}
}

func multiSZ(items ...string) Value {
	var b []byte
	for _, s := range items {
		b = append(b, utf16z(s)...)
	}
	return Value{Type: RegMultiSZ, Data: append(b, 0, 0)}
}

func le32(n uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, n); return b }

func utf16z(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2+2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[i*2:], c)
	}
	return b
}

// guidBytes encodes "{xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx}" in Windows'
// mixed-endian layout.
func guidBytes(s string) []byte {
	var g [16]byte
	h := strings.ReplaceAll(strings.Trim(s, "{}"), "-", "")
	for i := 0; i < 16; i++ {
		fmt.Sscanf(h[i*2:i*2+2], "%02x", &g[i])
	}
	out := make([]byte, 16)
	out[0], out[1], out[2], out[3] = g[3], g[2], g[1], g[0]
	out[4], out[5] = g[5], g[4]
	out[6], out[7] = g[7], g[6]
	copy(out[8:], g[8:])
	return out
}

// bootDevice is the "boot" device (the device Boot Manager was loaded
// from: here the TFTP server), as bcdedit writes it for ramdisksdidevice.
func bootDevice() []byte {
	b := make([]byte, 88)
	le := binary.LittleEndian
	le.PutUint32(b[16:], 5)    // device type: boot device
	le.PutUint32(b[24:], 0x48) // size of the device data from offset 16
	return b
}

// ramdiskDevice is "ramdisk=[boot]<wim>,{ramdiskoptions}": the WIM at path
// on the boot device, loaded as a RAM disk with {ramdiskoptions}. Layout
// as bcdedit writes it (checked byte for byte against the Phase 1 store).
func ramdiskDevice(path string) []byte {
	p := utf16z(path)
	b := make([]byte, 140+len(p))
	le := binary.LittleEndian
	copy(b, guidBytes(RAMDiskOptionsID)) // options object
	le.PutUint32(b[20:], 1)              // data present
	le.PutUint32(b[24:], uint32(len(b)-16))
	le.PutUint32(b[32:], 3) // device type: RAM disk
	le.PutUint32(b[56:], 1)
	le.PutUint32(b[60:], uint32(len(b)-56))
	le.PutUint32(b[64:], 5) // parent: the boot device
	le.PutUint32(b[68:], 5)
	le.PutUint32(b[76:], 0x48)
	copy(b[140:], p)
	return b
}
