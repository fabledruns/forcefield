//go:build windows

package sandbox

import (
	"fmt"
	"os"
)

// Windows has no O_NOFOLLOW equivalent in the Go standard library:
// os.Open and os.OpenFile follow a final-component symlink or junction.
// These helpers are mitigation, not equivalence, and must be read that
// way:
//
//   - the caller resolves the path within the workspace first
//     (Resolve/EnsureWithinWorkspace with EvalLinks junction chasing),
//   - OpenNoFollow* refuses a pre-open Lstat symlink so an obvious swap
//     fails with a clear error instead of opening,
//   - the caller fstats the open descriptor and applies AssertRegular,
//     which rejects directories, named pipes, and other special files.
//
// A symlink swapped in between Lstat and open is NOT stopped here.
// That residual is honest and tested (see enforcement notes); Unix
// closes it with O_NOFOLLOW.

// OpenNoFollowRead opens an already-resolved path for reading. Name kept
// symmetric with Unix; behavior is follow-with-pre-check (see above).
func OpenNoFollowRead(resolved string) (*os.File, error) {
	if info, err := os.Lstat(resolved); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing symlink %s: %w", resolved, ErrNotRegular)
		}
	}
	return os.Open(resolved)
}

// OpenNoFollowWrite opens an already-resolved path for truncate-write.
// Same follow-with-pre-check caveat as OpenNoFollowRead.
func OpenNoFollowWrite(resolved string, perm os.FileMode) (*os.File, error) {
	if info, err := os.Lstat(resolved); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing symlink %s: %w", resolved, ErrNotRegular)
		}
	}
	return os.OpenFile(resolved, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
}

// linkCount is unsupported on Windows via the standard library: the
// Win32 FileAttributeData exposed through FileInfo.Sys carries no link
// count. Returns ok=false so AssertWriteLinkCount permits the write and
// callers document the gap instead of claiming a guarantee.
func linkCount(os.FileInfo) (uint64, bool) {
	return 0, false
}
