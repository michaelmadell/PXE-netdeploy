//go:build windows

package sysinfo

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getTable = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemFirmwareTable")

// Read returns SMBIOS type 1 through GetSystemFirmwareTable('RSMB').
func Read() (System, bool) {
	const rsmb = 'R'<<24 | 'S'<<16 | 'M'<<8 | 'B'
	n, _, _ := getTable.Call(rsmb, 0, 0, 0)
	if n == 0 {
		return System{}, false
	}
	buf := make([]byte, n)
	if r, _, _ := getTable.Call(rsmb, 0, uintptr(unsafe.Pointer(&buf[0])), n); r == 0 || len(buf) < 8 {
		return System{}, false
	}
	l := int(binary.LittleEndian.Uint32(buf[4:]))
	if 8+l > len(buf) {
		return System{}, false
	}
	return ParseSMBIOS(buf[8 : 8+l])
}
