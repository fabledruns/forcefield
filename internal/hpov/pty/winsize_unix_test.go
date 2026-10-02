//go:build !windows

package pty

import (
	"os"

	"golang.org/x/sys/unix"
)

func queryWinsize() (rows, cols int, err error) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, err
	}
	return int(ws.Row), int(ws.Col), nil
}
