//go:build !windows

package shell

import (
	"errors"
	"os/exec"
	"syscall"
)

// setProcessGroup configures cmd to become the leader of a new process
// group (pgid == its own pid). Combined with killProcessGroup, this lets
// cancellation terminate everything the shell spawned - e.g.
// `sh -c "sleep 100 &"` - not just the `sh` process itself, which is all
// exec.CommandContext's default Cancel would kill.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup sends SIGKILL to every process in cmd's process group.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// A negative pid targets the whole process group in the kill(2)
	// syscall. Because setProcessGroup made this process its own group
	// leader, -pid is exactly that group. ESRCH means the group already
	// exited between the Process check and the kill, which is not a
	// failure.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}