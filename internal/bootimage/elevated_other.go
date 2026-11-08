//go:build !windows

package bootimage

// elevated is only meaningful on Windows (DISM).
func elevated() bool { return false }
