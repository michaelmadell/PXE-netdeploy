//go:build windows

package cas

import "golang.org/x/sys/windows"

func diskSpace(path string) (total, free int64, err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	var avail, tot, totFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &tot, &totFree); err != nil {
		return 0, 0, err
	}
	return int64(tot), int64(avail), nil
}
