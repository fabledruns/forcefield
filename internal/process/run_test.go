package process

import (
	"errors"
	"os/exec"
	"testing"
)

func TestExitCodeMapping(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Errorf("exitCode(nil) = %d, want 0", got)
	}
	// A signaled process never produced a code: callers (e.g. the
	// supervisor's classifyChildWait) must see -1, not a plausible
	// success or contract code.
	if got := exitCode(&exec.ExitError{}); got != -1 {
		t.Errorf("exitCode(empty ExitError) = %d, want -1", got)
	}
	if got := exitCode(errors.New("boom")); got != -1 {
		t.Errorf("exitCode(generic) = %d, want -1", got)
	}
}
