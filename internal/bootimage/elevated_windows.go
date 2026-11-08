//go:build windows

package bootimage

import "golang.org/x/sys/windows"

// elevated reports whether this process runs with an elevated token.
func elevated() bool { return windows.GetCurrentProcessToken().IsElevated() }
