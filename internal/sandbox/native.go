package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// nativeExecutor preserves Forcefield's historical behavior: Bash runs
// with full host privileges and the complete host environment. It is the
// default mode and exists so existing users keep working unchanged; it is
// never described as isolated.
type nativeExecutor struct {
	policy Policy
}

// newNativeExecutor builds the native backend. The policy is validated
// by NewExecutor before this is reached.
func newNativeExecutor(p Policy) (*nativeExecutor, error) {
	return &nativeExecutor{policy: p}, nil
}

// Prepare builds the same command the shell tool historically built:
//   - Unix:   bash -lc <command>, cmd.Dir set, os.Environ()+extras.
//   - Windows: the WSL relay used purely for Bash availability, exactly
//     as before (see wsl_windows.go in this package for why that relay
//     is not a security boundary).
//
// Directory handling is existence-only, matching the old validWorkingDir
// gate byte for byte: native mode has no workspace scope to enforce.
func (n *nativeExecutor) Prepare(ctx context.Context, req Request) (*Prepared, error) {
	if err := n.probePolicy(); err != nil {
		return nil, err
	}
	// Strict mode cages the working directory to the workspace through
	// the same boundary pipeline the wsl path uses; permissive mode
	// keeps the historical existence-only check. The boundary runs
	// before backend construction so violations fail without needing
	// Bash present.
	dir := req.Dir
	var err error
	if n.policy.Confines() {
		dir, err = resolveWithinWorkspace(n.policy.Workspace, req.Dir)
	} else {
		dir, err = resolveExistingDir(req.Dir)
	}
	if err != nil {
		return nil, err
	}
	prepared, err := n.build(ctx, req.Command, dir, req.ExtraEnv)
	if err != nil {
		return nil, err
	}
	prepared.Dir = dir
	return prepared, nil
}

func (n *nativeExecutor) probePolicy() error {
	if err := n.policy.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrBackendUnavailable, err)
	}
	return nil
}

// Describe reports native honestly: nothing is confined or enforced for
// shell command text in any mode. Strict pins the working directory and
// confines filesystem tools to the workspace (same invariant as wsl), but
// shell command text (e.g. cat /etc/passwd) remains unconfined by design —
// see Enforcement.FilesystemConfined ("For shell, NO BACKEND SETS THIS
// TRUE"). The full host environment reaches the command.
func (n *nativeExecutor) Describe(context.Context) Enforcement {
	if n.policy.Confines() {
		return Enforcement{
			Mode:               ModeNative,
			Distro:             n.policy.Distro,
			Network:            NetworkHost,
			CwdPinned:          true,
			FilesystemConfined: false,
			EnvForwarded:       true,
			NetworkEnforced:    false,
			Notes: []string{
				"strict workspace boundary: filesystem tools and the shell working directory are confined to the workspace; shell command text is not confined",
			},
		}
	}
	return Enforcement{
		Mode:               ModeNative,
		Distro:             n.policy.Distro,
		Network:            NetworkHost,
		CwdPinned:          false,
		FilesystemConfined: false,
		EnvForwarded:       true,
		NetworkEnforced:    false,
		Notes: []string{
			"native execution has no isolation: commands run with your user's permissions",
		},
	}
}

// envFor assembles the child environment for the host-side process.
func hostEnv(extra []string) []string {
	env := os.Environ()
	return append(env, extra...)
}

// Probe verifies the native interpreter is usable right now. It never
// fails because of policy: native has no isolation requirements beyond
// Bash existing.
func (n *nativeExecutor) Probe(ctx context.Context) error {
	return probeNativeHost(ctx)
}

var _ Executor = (*nativeExecutor)(nil)

// buildCommandHost is assigned per platform file.
var buildCommandHost func(ctx context.Context, command, dir string, extraEnv []string) (*exec.Cmd, func(), error)

func (n *nativeExecutor) build(ctx context.Context, command, dir string, extraEnv []string) (*Prepared, error) {
	cmd, cleanup, err := buildCommandHost(ctx, command, dir, extraEnv)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBackendUnavailable, err)
	}
	return &Prepared{Cmd: cmd, Cleanup: cleanup}, nil
}
