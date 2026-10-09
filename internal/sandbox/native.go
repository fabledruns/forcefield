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

// Prepare builds the historical shell command (Unix bash -lc; Windows WSL
// relay for Bash availability, not isolation). Strict cages cwd via the
// shared boundary pipeline; permissive keeps existence-only checks.
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

// Describe reports native honestly: no shell-text confinement in any mode.
// See docs/Sandbox.md and Enforcement.FilesystemConfined. Limitations is
// the structured source doctor renders; Notes stay the approval-UI lines.
func (n *nativeExecutor) Describe(context.Context) Enforcement {
	if n.policy.Confines() {
		return Enforcement{
			Mode:               ModeNative,
			Distro:             n.policy.Distro,
			Network:            NetworkHost,
			CwdPinned:          true,
			FilesystemConfined: false,
			EnvForwarded:       true,
			EnvCredsStripped:   len(n.policy.CredentialEnv) > 0,
			NetworkEnforced:    false,
			Notes: []string{
				"strict workspace boundary: filesystem tools and the shell working directory are confined to the workspace; shell command text is not confined",
			},
			Limitations: append([]Limitation{
				{ID: LimFilesystemToolsCaged, Detail: "filesystem tools are confined to the project workspace via tool-layer policy"},
				{ID: LimFilesystemShellOpen, Warn: true, Detail: "shell command text is NOT confined; only the working directory is pinned"},
				{ID: LimShellTextOpen, Warn: true, Detail: "shell command text is never confined, in any mode; outside paths are gated only by permissions (ask)"},
				{ID: LimNetworkNamespace, Detail: "host networking; no isolation is attempted in native mode"},
				{ID: LimEnvFullHost, Detail: "full host environment is forwarded by design"},
				{ID: LimPlatformHostOnly, Detail: "native runs on the host on all platforms; no Linux/macOS isolation backend exists in v1.5.0"},
			}, append(credentialStrippedLimitation(n.policy), processLimitations()...)...),
		}
	}
	return Enforcement{
		Mode:               ModeNative,
		Distro:             n.policy.Distro,
		Network:            NetworkHost,
		CwdPinned:          false,
		FilesystemConfined: false,
		EnvForwarded:       true,
		EnvCredsStripped:   len(n.policy.CredentialEnv) > 0,
		NetworkEnforced:    false,
		Notes: []string{
			"native execution has no isolation: commands run with your user's permissions",
		},
		Limitations: append([]Limitation{
			{ID: LimFilesystemToolsCaged, Detail: "filesystem tools are confined to the project workspace via tool-layer policy"},
			{ID: LimFilesystemShellOpen, Warn: true, Detail: "shell runs unconfined: working directory is NOT pinned and command text is NOT confined"},
			{ID: LimShellTextOpen, Warn: true, Detail: "shell command text is never confined, in any mode; outside paths are gated only by permissions (ask)"},
			{ID: LimNetworkNamespace, Detail: "host networking; no isolation is attempted in native mode"},
			{ID: LimEnvFullHost, Detail: "full host environment is forwarded by design"},
			{ID: LimPlatformHostOnly, Detail: "native runs on the host on all platforms; no Linux/macOS isolation backend exists in v1.5.0"},
		}, append(credentialStrippedLimitation(n.policy), processLimitations()...)...),
	}
}

// credentialStrippedLimitation reports the credential-stripping hygiene
// (info, never warn) when the policy names credential variables, and
// nothing otherwise, so unconfigured executors describe exactly what
// they were before.
func credentialStrippedLimitation(p Policy) []Limitation {
	if len(p.CredentialEnv) == 0 {
		return nil
	}
	return []Limitation{{
		ID:     LimEnvCredsStripped,
		Detail: "host variables Forcefield itself reads as provider credentials are removed from shell children; explicit per-command env still applies (hygiene, not an OS boundary)",
	}}
}

// envFor assembles the child environment for the host-side process:
// the full host environment minus the credential names Forcefield
// itself reads (see stripCredentialEnv), plus the request's explicit
// extras appended last so deliberate per-command values win.
func hostEnv(extra, strip []string) []string {
	env := stripCredentialEnv(os.Environ(), strip)
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
var buildCommandHost func(ctx context.Context, command, dir string, extraEnv, strip []string) (*exec.Cmd, func(), error)

func (n *nativeExecutor) build(ctx context.Context, command, dir string, extraEnv []string) (*Prepared, error) {
	cmd, cleanup, err := buildCommandHost(ctx, command, dir, extraEnv, n.policy.CredentialEnv)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBackendUnavailable, err)
	}
	return &Prepared{Cmd: cmd, Cleanup: cleanup}, nil
}
