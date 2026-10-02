//go:build darwin

package pty

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// unlockPty grants/unlocks the slave and asks for its name: Darwin has
// no TIOCSPTLCK/TIOCGPTN pair, so the name comes back from
// TIOCPTYGNAME instead of being derived from a number.
func unlockPty(masterFd int) (string, error) {
	if err := ioctl(masterFd, uint(unix.TIOCPTYGRANT), nil); err != nil {
		return "", fmt.Errorf("pty: grant: %w", err)
	}
	if err := ioctl(masterFd, uint(unix.TIOCPTYUNLK), nil); err != nil {
		return "", fmt.Errorf("pty: unlock: %w", err)
	}
	var name [128]byte
	if err := ioctl(masterFd, uint(unix.TIOCPTYGNAME), unsafe.Pointer(&name[0])); err != nil {
		return "", fmt.Errorf("pty: slavename: %w", err)
	}
	n := 0
	for n < len(name) && name[n] != 0 {
		n++
	}
	if n == 0 {
		return "", fmt.Errorf("pty: empty slave name")
	}
	return string(name[:n]), nil
}
