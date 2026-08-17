//go:build windows

package shell

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
)

// setProcessGroup is a no-op on Windows: POSIX process groups don't exist
// here. Cancellation falls back to killing the direct child process only
// (see killProcessGroup), same as exec.CommandContext's default behavior.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup kills cmd's own process. It doesn't reach grandchildren
// on Windows without CREATE_NEW_PROCESS_GROUP + job-object plumbing, which
// is out of scope here; this preserves the pre-existing behavior for the
// direct child at minimum.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}

	// Use taskkill to terminate the full process tree rooted at cmd.
	// This mirrors the Unix "kill process group" behavior as closely as
	// possible on Windows, where grandchildren can otherwise keep stdio
	// pipes open and delay timeout/cancellation completion. If taskkill
	// fails, fall through to the direct-child kill below rather than
	// surfacing a diagnostics-only error from the Cancel path.
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()

	// Fallback to killing the direct child if taskkill is unavailable.
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}