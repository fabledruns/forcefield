//go:build !windows

package runner

import "os"

// havePTY reports /dev/ptmx availability for the openpty path.
func havePTY() bool {
	f, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
