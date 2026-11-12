//go:build !windows

package disk

// List is only implemented for Windows (WinPE).
func List() []Disk { return nil }

// PartitionGUIDs is only implemented for Windows (WinPE).
func PartitionGUIDs(int) ([][16]byte, error) { return nil, nil }
