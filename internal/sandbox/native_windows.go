//go:build windows

package sandbox

import (
	"context"
	"os"
	"os/exec"
)

func init() {
	buildCommandHost = buildNativeRelay
}

// buildNativeRelay is native mode on Windows: the historical invocation
// that reaches GNU Bash through the WSL distribution. It exists for Bash
// availability, not isolation: the host process's full environment goes to
// wsl.exe (so WSLENV-gated variables keep flowing exactly as before), the
// distribution's own default environment applies inside, and no filesystem
// or network restriction of any kind is attempted.
//
// The command is assembled as an argv (--exec) so agent-authored text can
// never be re-parsed by a host shell; that property predates sandboxing
// and is preserved unchanged.
func buildNativeRelay(ctx context.Context, command, dir string, extraEnv []string) (*exec.Cmd, func(), error) {
	exe, err := wslExePath()
	if err != nil {
		return nil, nil, wslMissingError()
	}

	distro := resolveDistro("")

	args := append([]string{}, distroFlagArgs(distro)...)
	if cd := wslCdPath(dir); cd != "" {
		args = append(args, "--cd", cd)
	}
	args = append(args, "--exec")

	var cleanup func()
	if commandBudget(command, extraEnv) <= wslArgBudget {
		if len(extraEnv) > 0 {
			args = append(args, "/usr/bin/env")
			args = append(args, extraEnv...)
		}
		args = append(args, "/bin/bash", "-lc", command)
	} else {
		script, remove, err := writeWslScript(extraEnv, command)
		if err != nil {
			return nil, nil, err
		}
		cleanup = remove
		args = append(args, "/bin/bash", "-l", script)
	}

	cmd := exec.CommandContext(ctx, exe, args...)
	// Historical behavior: wsl.exe gets its normal Windows environment;
	// the Linux-side environment comes from the distribution plus the
	// explicit extras above, since WSL does not forward arbitrary Windows
	// variables into the distribution. cmd.Dir stays empty: --cd handles
	// chdir inside WSL.
	cmd.Env = os.Environ()
	return cmd, cleanup, nil
}
