//go:build !windows

package memory

import (
	"syscall"
)

// sameVolume reports whether a and b live on the same filesystem.
// git stops repository discovery at filesystem boundaries by default,
// so a device change means only the subprocess knows the verdict:
// false (uncertain) defers to it. Stat errors also defer rather than
// guessing.
func sameVolume(a, b string) bool {
	var sa, sb syscall.Stat_t
	if err := syscall.Stat(a, &sa); err != nil {
		return false
	}
	if err := syscall.Stat(b, &sb); err != nil {
		return false
	}
	return sa.Dev == sb.Dev
}

// gitRootSpelling returns path unchanged: rev-parse prints Unix paths
// verbatim, and backslashes are legal filename characters here, so no
// separator folding may occur.
func gitRootSpelling(path string) string {
	return path
}
