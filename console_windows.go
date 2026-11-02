//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// init makes the Windows console understand UTF-8 and ANSI escapes so the
// banner, the QR code and colours render instead of turning into mojibake.
func init() {
	h := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return // stdout is a pipe or a file
	}
	_ = windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	_ = windows.SetConsoleOutputCP(65001)
}
