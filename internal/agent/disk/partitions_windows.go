//go:build windows

package disk

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

const (
	ioctlDiskGetDriveLayoutEx = 0x00070050
	layoutHeaderLength        = 48  // DRIVE_LAYOUT_INFORMATION_EX up to PartitionEntry
	partitionEntryLength      = 144 // PARTITION_INFORMATION_EX
)

// PartitionGUIDs returns the GPT partition GUIDs (PartitionId) on disk n, in
// EFI byte order; none for an MBR or empty disk.
func PartitionGUIDs(n int) ([][16]byte, error) {
	name, _ := windows.UTF16PtrFromString(fmt.Sprintf(`\\.\PhysicalDrive%d`, n))
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("open disk %d: %w", n, err)
	}
	defer windows.CloseHandle(h)
	for size := layoutHeaderLength + 128*partitionEntryLength; ; size *= 2 {
		buf := make([]byte, size)
		var got uint32
		err := windows.DeviceIoControl(h, ioctlDiskGetDriveLayoutEx, nil, 0, &buf[0], uint32(len(buf)), &got, nil)
		if errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) && size < 1<<20 {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("partitions of disk %d: %w", n, err)
		}
		return parseLayout(buf[:got]), nil
	}
}
