//go:build !windows

package ui

func enableVT() bool { return true }

// Size is only implemented for Windows.
func Size() (cols, rows, bufCols, bufRows int, ok bool) { return 0, 0, 0, 0, false }
