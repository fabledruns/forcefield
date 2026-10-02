//go:build !windows

package pty

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// consoleSelfReport prints the child's own terminal state. Runs
// inside the fake child: stdout must be the pty slave (a tty).
func consoleSelfReport() int {
	_, err := unix.IoctlGetTermios(int(os.Stdout.Fd()), ioctlTcgets())
	if err != nil {
		_, _ = os.Stdout.WriteString("REPORT tty=err" + strconv.Itoa(int(err.(unix.Errno))) + "\n")
		return 0
	}
	_, _ = os.Stdout.WriteString("REPORT tty=ok\n")
	return 0
}
