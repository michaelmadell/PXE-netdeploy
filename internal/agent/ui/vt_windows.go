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

// Size reports the console window's visible columns and rows and its
// buffer's. ok=false: stdout isn't a console.
func Size() (cols, rows, bufCols, bufRows int, ok bool) {
	var info windows.ConsoleScreenBufferInfo
	if windows.GetConsoleScreenBufferInfo(windows.Handle(os.Stdout.Fd()), &info) != nil {
		return 0, 0, 0, 0, false
	}
	w := info.Window
	return int(w.Right-w.Left) + 1, int(w.Bottom-w.Top) + 1, int(info.Size.X), int(info.Size.Y), true
}
