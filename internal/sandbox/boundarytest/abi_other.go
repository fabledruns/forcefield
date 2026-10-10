//go:build !linux

package boundarytest

// queryLandlock reports Landlock unavailable off Linux without probing
// anything: Landlock is a Linux-only mechanism, so there is no ABI to
// query here and no support to claim. Same signature as abi_linux.go so
// probe.go stays platform-independent.
func queryLandlock() (abi int, status, detail string) {
	return -1, "unsupported", "landlock is a Linux-only mechanism"
}
