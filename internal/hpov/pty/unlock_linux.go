//go:build linux

package pty

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// unlockPty unlocks the slave and resolves its device path from the
// pty number Linux reports via TIOCGPTN.
func unlockPty(masterFd int) (string, error) {
	var unlock int32
	if err := ioctl(masterFd, uint(unix.TIOCSPTLCK), unsafe.Pointer(&unlock)); err != nil {
		return "", fmt.Errorf("pty: unlock: %w", err)
	}
	var num uint32
	if err := ioctl(masterFd, uint(unix.TIOCGPTN), unsafe.Pointer(&num)); err != nil {
		return "", fmt.Errorf("pty: pty number: %w", err)
	}
	return fmt.Sprintf("/dev/pts/%d", num), nil
}
