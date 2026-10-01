//go:build !windows

package sandbox

import (
	"os"
	"syscall"
)

// OpenNoFollowRead opens an already-resolved path for reading without
// following a final-component symlink (O_NOFOLLOW). O_NONBLOCK ensures
// opening a FIFO never blocks waiting for a writer: fstat plus
// AssertRegular then rejects it before any read.
func OpenNoFollowRead(resolved string) (*os.File, error) {
	return os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// OpenNoFollowReadWrite opens an already-resolved path for read-write
// without following a final-component symlink and without truncating:
// callers validate the open descriptor first, then truncate explicitly
// with f.Truncate, so a refused write never destroys existing content
// (truncation used to happen inside open, before validation).
// O_NONBLOCK avoids blocking on FIFOs; the caller fstats the descriptor
// and applies AssertRegular plus AssertWriteLinkCount before touching it.
func OpenNoFollowReadWrite(resolved string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(resolved, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, perm)
}

// linkCount reports the hard-link count where the platform exposes it.
func linkCount(info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, false
	}
	return uint64(stat.Nlink), true
}
