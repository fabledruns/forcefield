//go:build !windows

package runner

import (
	"os"
	"runtime"
)

// havePTY reports /dev/ptmx availability for the openpty path.
func havePTY() bool {
	f, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// haveMemory reports OS memory-measurement availability. Linux exposes
// VmRSS/VmHWM in /proc; macOS exposes resident size through kern.proc.
func haveMemory() bool { return runtime.GOOS == "linux" || runtime.GOOS == "darwin" }
