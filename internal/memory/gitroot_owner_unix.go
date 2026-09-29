//go:build !windows

package memory

import (
	"syscall"
)

// gitRootOwnerMatches reports whether gitPath is owned by the current
// effective user. git refuses repositories owned by someone else as
// dubiously owned (unless safe.directory allows them); requiring the
// match keeps the fast path from contradicting git. Any uncertainty
// falls back to the subprocess, which is today's exact behavior.
func gitRootOwnerMatches(gitPath string) bool {
	var st syscall.Stat_t
	if err := syscall.Stat(gitPath, &st); err != nil {
		return false
	}
	return st.Uid == uint32(syscall.Geteuid())
}
