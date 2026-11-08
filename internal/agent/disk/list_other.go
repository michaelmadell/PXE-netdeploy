//go:build !windows

package disk

// List is only implemented for Windows (WinPE).
func List() []Disk { return nil }
