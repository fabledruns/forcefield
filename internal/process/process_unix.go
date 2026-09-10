//go:build !windows

package process

import (
	"errors"
	"os/exec"
	"syscall"
)

// Configure makes cmd the leader of a new process group (pgid == its
// own pid) before Start. Combined with Kill, this terminates everything
// the child spawns — e.g. `sh -c "sleep 100 &"` — not just the direct
// child, which is all a default Kill would reach. Group membership is
// inherited reliably, so unlike enumeration-based kills there is no
// race with later-spawned grandchildren.
func Configure(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// Track is a no-op on Unix: Configure already established group
// membership before Start, which Kill reaches in full.
func Track(cmd *exec.Cmd) (release ReleaseFunc) {
	return func() {}
}

// Kill sends SIGKILL to every process in cmd's process group. A
// negative pid targets the whole group in kill(2); because Configure
// made this process its own group leader, -pid is exactly that group.
// ESRCH means the group already exited, which is not a failure.
func Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// Terminate asks the whole process group to exit gracefully (SIGTERM)
// so supervised children can run their own teardown — a cancelled `ff
// run` child then cancels its tools and persists terminal state instead
// of orphaning them. ESRCH is tolerated like in Kill.
func Terminate(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
