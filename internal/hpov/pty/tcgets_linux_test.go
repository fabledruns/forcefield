//go:build linux

package pty

import "golang.org/x/sys/unix"

func ioctlTcgets() uint { return unix.TCGETS }
