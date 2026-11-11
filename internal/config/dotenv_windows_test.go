//go:build windows

package config

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
	bufLen := uint32(1024)
	for {
		buf := make([]uint16, bufLen)
		n, _, _ := proc.Call(uintptr(unsafe.Pointer(u16)), uintptr(unsafe.Pointer(&buf[0])), uintptr(bufLen))
		if n == 0 {
			return "", errors.New("GetShortPathNameW returned 0")
		}
		if n < uintptr(bufLen) {
			short := syscall.UTF16ToString(buf[:n])
			if strings.EqualFold(short, p) {
				return "", errors.New("short name identical to long name")
			}
			return short, nil
		}
		bufLen = uint32(n) + 1
	}
}
