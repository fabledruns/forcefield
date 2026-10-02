//go:build !windows && !linux && !darwin

package collect

// readProcess reports unavailable: HPOV supports Windows, Linux and
// macOS, and inventing a third mechanism would produce a number whose
// meaning nobody can state.
func readProcess(pid int) (Reading, error) {
	return Reading{PID: pid, RSSBytes: -1, PeakBytes: -1,
			Reason: ReasonUnsupported},
		unavailable(ReasonUnsupported, nil)
}

// descendants reports unavailable for the same reason.
func descendants(root int) ([]int, error) { return nil, unavailable(ReasonUnsupported, nil) }
