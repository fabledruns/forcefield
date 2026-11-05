//go:build !windows

package terminal

import (
	"io"
)

func isWindowsConsole(w io.Writer) bool {
	return false
}

func defaultGetConsoleTitle() (string, bool) {
	return "", false
}

func defaultSetConsoleTitle(title string) error {
	return nil
}
