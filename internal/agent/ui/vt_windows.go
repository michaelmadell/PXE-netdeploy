//go:build windows

package ui

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVT turns on VT sequence processing on stdout.
func enableVT() bool {
	h := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return false
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
