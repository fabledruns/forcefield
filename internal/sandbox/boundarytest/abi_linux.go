//go:build linux

package boundarytest

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// landlockABIQuery performs the documented Landlock version-query
// operation: landlock_create_ruleset(NULL, 0,
// LANDLOCK_CREATE_RULESET_VERSION). It applies no restrictions and
// creates no ruleset; it only asks the kernel which ABI it speaks, so
// it is safe to run on any Linux machine without privileges.
//
// A variable (not a plain func) so tests can stub unavailable and
// error states without a specific kernel, mirroring the repo's seam
// style (e.g. sandbox.bashLookPath).
var landlockABIQuery = func() (int, error) {
	r1, _, errno := unix.Syscall(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0, 0,
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION),
	)
	if errno != 0 {
		return -1, fmt.Errorf("landlock ABI query: %w", errno)
	}
	return int(r1), nil
}

// classifyLandlockErr maps a version-query failure onto the stable
// status vocabulary: ENOSYS (no such syscall) means the kernel cannot
// do Landlock at all; EOPNOTSUPP means it is compiled in but disabled
// (LSM list, boot flags, container policy). Anything else is an
// unexpected error whose context the caller preserves. Pure:
// unit-testable without a kernel.
func classifyLandlockErr(err error) string {
	switch {
	case errors.Is(err, unix.ENOSYS):
		return "unsupported"
	case errors.Is(err, unix.EOPNOTSUPP):
		return "disabled"
	default:
		return "error"
	}
}

// queryLandlock runs the version query and folds the outcome into the
// machine-readable triple the probe reports: abi >= 1 with status
// "supported", or abi -1 with a stable status plus human detail. An
// impossible ABI (e.g. 0 from a confused kernel) is reported as an
// error, never as support. An ABI query success proves only that the
// kernel speaks Landlock; it does not prove ruleset creation or
// enforcement works.
func queryLandlock() (abi int, status, detail string) {
	a, err := landlockABIQuery()
	if err == nil {
		if a < 1 {
			return -1, "error", fmt.Sprintf("landlock ABI query returned impossible ABI %d", a)
		}
		return a, "supported", ""
	}
	return -1, classifyLandlockErr(err), err.Error()
}
