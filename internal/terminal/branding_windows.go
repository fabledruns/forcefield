//go:build windows

package terminal

import (
	"io"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleTitle = kernel32.NewProc("GetConsoleTitleW")
	procSetConsoleTitle = kernel32.NewProc("SetConsoleTitleW")
)

func isWindowsConsole(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		var mode uint32
		err := windows.GetConsoleMode(windows.Handle(f.Fd()), &mode)
		return err == nil
	}
	return false
}

func defaultGetConsoleTitle() (string, bool) {
	buf := make([]uint16, 2048)
	r1, _, _ := procGetConsoleTitle.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r1 == 0 {
		return "", false
	}
	return syscall.UTF16ToString(buf), true
}

func defaultSetConsoleTitle(title string) error {
	p, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return err
	}
	r1, _, err := procSetConsoleTitle.Call(uintptr(unsafe.Pointer(p)))
	if r1 == 0 {
		return err
	}
	return nil
}
