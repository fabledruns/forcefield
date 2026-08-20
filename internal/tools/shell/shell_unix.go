//go:build !windows

package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// bashLookPath is a seam over exec.LookPath so tests can simulate Bash
// being absent without touching the real PATH.
var bashLookPath = exec.LookPath

// ensureBackend is a no-op on Unix: Bash availability is checked when the
// command is built (see buildCommand), which preserves the historical
// "bash was not found on PATH" error message.
func (s *Shell) ensureBackend(context.Context) error { return nil }

// validWorkingDir reports whether dir exists on the host and is a directory.
func validWorkingDir(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// backendFailure never reports a backend failure on Unix: a failing command
// is always the command's own doing.
func backendFailure(_, _ string) (detail, hint string) { return "", "" }

// buildCommand runs command through the system's GNU Bash as a login shell
// (so PATH and profile are set up), using CommandContext so ctx
// cancellation/timeout terminates the process. The command is passed as a
// single argument and reaches Bash verbatim; there is deliberately no
// cmd.exe/PowerShell path and no string rebuilding. The returned cleanup is
// always nil on Unix.
func buildCommand(ctx context.Context, command, cwd string, extraEnv []string) (*exec.Cmd, func(), error) {
	bash, err := bashLookPath("bash")
	if err != nil {
		return nil, nil, fmt.Errorf("bash was not found on PATH; Forcefield requires Bash for shell commands")
	}
	cmd := exec.CommandContext(ctx, bash, "-lc", command)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), extraEnv...)
	return cmd, nil, nil
}

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
