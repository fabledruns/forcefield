//go:build !windows

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
)

// bashLookPath is a seam over exec.LookPath so tests can simulate Bash
// being absent without touching the real PATH.
var bashLookPath = exec.LookPath

func init() {
	buildCommandHost = buildNativeUnix
}

// buildNativeUnix runs command through the system's GNU Bash as a login
// shell (so PATH and profile are set up), using CommandContext so ctx
// cancellation/timeout terminates the process. The command is passed as a
// single argument and reaches Bash verbatim; there is deliberately no
// cmd.exe/PowerShell path and no string rebuilding. The returned cleanup
// is always nil on Unix.
//
// This is the full host environment: native mode forwards everything by
// design, and Enforcement.EnvForwarded says so.
func buildNativeUnix(ctx context.Context, command, dir string, extraEnv []string) (*exec.Cmd, func(), error) {
	bash, err := bashLookPath("bash")
	if err != nil {
		return nil, nil, fmt.Errorf("bash was not found on PATH; Forcefield requires Bash for shell commands")
	}
	cmd := exec.CommandContext(ctx, bash, "-lc", command)
	cmd.Dir = dir
	cmd.Env = hostEnv(extraEnv)
	return cmd, nil, nil
}

// probeNativeHost verifies Bash is reachable on PATH.
func probeNativeHost(context.Context) error {
	if _, err := bashLookPath("bash"); err != nil {
		return fmt.Errorf("bash was not found on PATH; Forcefield requires Bash for shell commands")
	}
	return nil
}
