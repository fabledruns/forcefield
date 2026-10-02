//go:build windows

package pty

import (
	"os"

	"golang.org/x/sys/windows"
)

func queryWinsize() (rows, cols int, err error) {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(os.Stdout.Fd()), &info); err != nil {
		return 0, 0, err
	}
	// Visible window (viewport), not the buffer: ConPTY sizes it to
	// the requested dimensions.
	cols = int(info.Window.Right-info.Window.Left) + 1
	rows = int(info.Window.Bottom-info.Window.Top) + 1
	return rows, cols, nil
}
