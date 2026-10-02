//go:build windows

package pty

import (
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

// consoleSelfReport prints the child's own console state to its
// (possibly inherited) stdout. Runs inside the fake child.
func consoleSelfReport() int {
	probe := func(f *os.File) string {
		var mode uint32
		err := windows.GetConsoleMode(windows.Handle(f.Fd()), &mode)
		if err != nil {
			if en, ok := err.(windows.Errno); ok {
				return "err" + strconv.Itoa(int(en))
			}
			return "err?"
		}
		return "mode" + strconv.FormatUint(uint64(mode), 10)
	}
	_, _ = os.Stdout.WriteString("REPORT stdout=" + probe(os.Stdout) +
		" stdin=" + probe(os.Stdin) + " stderr=" + os.Stderr.Name() + "\n")
	return 0
}
