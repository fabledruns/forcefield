//go:build windows

package memory

import (
	"path/filepath"
	"strings"
)

// sameVolume reports whether a and b live on the same Windows volume.
// git stops repository discovery at filesystem boundaries by default,
// so a volume change means only the subprocess knows the verdict:
// false (uncertain) defers to it. UNC-vs-mapped spellings of one share
// compare unequal and also defer, which is safe in the same way.
func sameVolume(a, b string) bool {
	return strings.EqualFold(filepath.VolumeName(a), filepath.VolumeName(b))
}

// gitRootSpelling folds separators to forward slashes, matching
// rev-parse's output spelling exactly (verified: `git rev-parse
// --show-toplevel` prints `C:/...`). Backslashes cannot appear in
// Windows filenames, so the fold is lossless. The spelling feeds
// project identity hashes, so byte-equality with git matters.
func gitRootSpelling(path string) string {
	return filepath.ToSlash(path)
}
