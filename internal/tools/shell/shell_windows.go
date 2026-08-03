//go:build windows

package shell

import "os/exec"

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
	return cmd.Process.Kill()
}