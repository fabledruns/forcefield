//go:build windows

package filesystem

import (
	"errors"
	"strings"
	"syscall"
	"unsafe"
)

func windowsShortPath(p string) (string, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetShortPathNameW")
	u16, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, 1024)
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(u16)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return "", errors.New("GetShortPathNameW returned 0")
	}
	short := syscall.UTF16ToString(buf[:n])
	if strings.EqualFold(short, p) {
		return "", errors.New("short name identical to long name")
	}
	return short, nil
}
