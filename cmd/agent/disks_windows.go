//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

const (
	ioctlDiskGetDriveGeometryEx = 0x000700A0
	ioctlStorageQueryProperty   = 0x002D1400
	maxPhysicalDrives           = 64
)

// busTypes are STORAGE_BUS_TYPE values.
var busTypes = map[uint32]string{
	1: "SCSI", 2: "ATAPI", 3: "ATA", 4: "1394", 6: "FibreChannel", 7: "USB", 8: "RAID", 9: "iSCSI",
	10: "SAS", 11: "SATA", 12: "SD", 13: "MMC", 14: "Virtual", 15: "FileBackedVirtual", 16: "Spaces", 17: "NVMe", 18: "SCM", 19: "UFS",
}

// listDisks returns the physical disks Windows sees (\\.\PhysicalDriveN).
func listDisks() []disk {
	var out []disk
	for i := 0; i < maxPhysicalDrives; i++ {
		name, _ := windows.UTF16PtrFromString(fmt.Sprintf(`\\.\PhysicalDrive%d`, i))
		// No access rights needed for these IOCTLs.
		h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			continue
		}
		d := disk{Number: i}
		var geo [256]byte
		var n uint32
		if windows.DeviceIoControl(h, ioctlDiskGetDriveGeometryEx, nil, 0, &geo[0], uint32(len(geo)), &n, nil) == nil && n >= 32 {
			d.SizeBytes = int64(binary.LittleEndian.Uint64(geo[24:32])) // after DISK_GEOMETRY (24 bytes)
		}
		// STORAGE_PROPERTY_QUERY{StorageDeviceProperty, PropertyStandardQuery}.
		query := make([]byte, 12)
		var desc [1024]byte
		if windows.DeviceIoControl(h, ioctlStorageQueryProperty, &query[0], uint32(len(query)), &desc[0], uint32(len(desc)), &n, nil) == nil && n >= 32 {
			str := func(off uint32) string {
				if off == 0 || off >= n {
					return ""
				}
				b := desc[off:n]
				if i := strings.IndexByte(string(b), 0); i >= 0 {
					b = b[:i]
				}
				return strings.TrimSpace(string(b))
			}
			d.Model = strings.TrimSpace(str(binary.LittleEndian.Uint32(desc[12:])) + " " + str(binary.LittleEndian.Uint32(desc[16:])))
			d.Bus = busTypes[binary.LittleEndian.Uint32(desc[28:])]
			if d.Bus == "" {
				d.Bus = fmt.Sprintf("bus %d", binary.LittleEndian.Uint32(desc[28:]))
			}
		}
		windows.CloseHandle(h)
		out = append(out, d)
	}
	return out
}
