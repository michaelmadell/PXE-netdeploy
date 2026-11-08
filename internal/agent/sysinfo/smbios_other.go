//go:build !windows

package sysinfo

// Read is only implemented on Windows.
func Read() (System, bool) { return System{}, false }
