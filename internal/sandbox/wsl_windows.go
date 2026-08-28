//go:build windows

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// wslExecutor runs commands inside a WSL distribution under an explicitly
// restricted invocation. What it enforces, at boundaries this package
// controls:
//
//   - Structured argv through wsl.exe --exec: agent text is never
//     re-parsed by a host or distribution shell during invocation.
//   - Working directory pinned inside the workspace: every request is
//     resolved (symlinks included) against the workspace before the
//     process exists; escapes are rejected, never expanded.
//   - Host environment severed: wsl.exe receives only SystemRoot/TEMP/TMP
//     plus an explicitly EMPTY WSLENV, so no host variable - API keys
//     included - crosses into the distribution. Inside, the environment
//     is the distribution's own defaults plus exactly the K=V pairs the
//     caller asked for.
//   - Network policy: "disabled" is enforced by launching the command in
//     a fresh network namespace (in-distribution unshare with an
//     unprivileged user namespace). When the kernel/distro refuses that,
//     the executor FAILS CLOSED rather than running with host networking.
//   - Process lifetime: timeout, context cancellation, and process-tree
//     teardown remain fully effective.
//
// What it does NOT do, stated plainly:
//
//   - Filesystems are not confined. The distribution mounts every Windows
//     drive under /mnt/*, so a command can read and write anywhere its OS
//     identity allows, plus the whole Linux filesystem of the
//     distribution. Only the working directory is validated. This package
//     will not claim workspace confinement that plain wsl.exe cannot
//     deliver.
//   - CPU/memory/process-count limits are not enforced.
//
// Enforcement.Describe carries these facts to the approval UI and doctor;
// nothing else gets to characterize the boundary.

// restrictedHostEnvNames are the only host variables the launcher itself
// receives: what wsl.exe needs to start reliably (its own staging uses
// TEMP/TMP), and WSLENV deliberately set empty to sever all variable
// sharing between host and distribution.
var restrictedHostEnvNames = []string{"SystemRoot", "TEMP", "TMP"}

type wslExecutor struct {
	policy Policy

	mu        sync.Mutex // guards the cached probes below
	healthy   bool       // distro probed OK at least once
	healthErr error
	netProbe  *netProbeResult
}

type netProbeResult struct {
	supported bool
	unshare   string // resolved path inside the distribution
	err       error
}

func newWSLExecutor(p Policy) (*wslExecutor, error) {
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid sandbox policy: %w", err)
	}
	return &wslExecutor{policy: p}, nil
}

// restrictedEnv builds the complete host-side environment for wsl.exe.
// Everything absent here cannot reach the distribution.
func restrictedEnv() []string {
	env := make([]string, 0, len(restrictedHostEnvNames)+1)
	for _, name := range restrictedHostEnvNames {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	env = append(env, "WSLENV=")
	return env
}

// ensureHealth runs (once) the distribution health probe.
func (w *wslExecutor) ensureHealth(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.healthy {
		return nil
	}
	if w.healthErr != nil && ctx.Err() == nil {
		// Re-probe after a previous failure only if it was context-driven;
		// deterministic failures stay cached like the historical behavior.
		if !errors.Is(w.healthErr, context.Canceled) && !errors.Is(w.healthErr, context.DeadlineExceeded) {
			return w.healthErr
		}
	}

	exe, err := wslExePath()
	if err != nil {
		w.healthErr = wslMissingError()
		return w.healthErr
	}
	distro := resolveDistro(w.policy.Distro)
	if err := probeWSLDistro(ctx, exe, distro); err != nil {
		w.healthErr = fmt.Errorf("%w: %v", ErrBackendUnavailable, err)
		return w.healthErr
	}
	w.healthy = true
	w.healthErr = nil
	return nil
}

// ensureNetProbe resolves whether network namespaces can be created
// unprivileged inside the distribution. The probe prints the resolved
// unshare path on success so later invocations need no PATH guessing.
func (w *wslExecutor) ensureNetProbe(ctx context.Context) (*netProbeResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.netProbe != nil {
		return w.netProbe, w.netProbe.err
	}

	exe, err := wslExePath()
	if err != nil {
		res := &netProbeResult{err: wslMissingError()}
		w.netProbe = res
		return res, res.err
	}

	// No user-controlled content enters this probe string.
	const probeScript = `p=$(command -v unshare 2>/dev/null) || exit 97; "$p" --user --net --map-root-user true && printf %s "$p"`
	args := append(distroFlagArgs(resolveDistro(w.policy.Distro)),
		"--exec", "/bin/sh", "-c", probeScript)
	pctx, cancel := context.WithTimeout(ctx, wslPreflightTimeout)
	defer cancel()
	out, err := exec.CommandContext(pctx, exe, args...).Output()

	res := &netProbeResult{}
	switch {
	case ctx.Err() != nil:
		res.err = ctx.Err()
	case err != nil:
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 97 {
			res.err = errors.New("no unshare utility found in the distribution")
		} else {
			detail := strings.TrimSpace(decodeWSLText(out))
			res.err = fmt.Errorf("network namespace creation failed (%v)", detail)
		}
	case strings.TrimSpace(string(out)) == "":
		res.err = errors.New("unshare probe produced no path")
	default:
		res.supported = true
		res.unshare = strings.TrimSpace(string(out))
	}

	w.netProbe = res
	return res, res.err
}

// Probe verifies the whole configured chain: launcher, distribution, and
// - when a disabled network was requested - namespace support. A failure
// here means wsl mode refuses to run anything; there is no fallback.
func (w *wslExecutor) Probe(ctx context.Context) error {
	if err := w.ensureHealth(ctx); err != nil {
		return err
	}
	if w.policy.effectiveNetwork() == NetworkDisabled {
		if _, err := w.ensureNetProbe(ctx); err != nil {
			return fmt.Errorf("%w: network isolation requested but unavailable: %v", ErrBackendUnavailable, err)
		}
	}
	return nil
}

// Prepare validates the request against the policy and assembles the
// restricted invocation. See the type comment for the exact guarantees.
func (w *wslExecutor) Prepare(ctx context.Context, req Request) (*Prepared, error) {
	exe, err := wslExePath()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBackendUnavailable, wslMissingError())
	}

	dir, err := resolveWithinWorkspace(w.policy.Workspace, req.Dir)
	if err != nil {
		return nil, err
	}

	distro := resolveDistro(w.policy.Distro)
	if err := w.ensureHealth(ctx); err != nil {
		return nil, err
	}
	if !validWorkingDir(exe, distro, dir) {
		return nil, fmt.Errorf("%w: %s", ErrInvalidDir, dir)
	}

	var linuxArgv []string
	if w.policy.effectiveNetwork() == NetworkDisabled {
		probe, err := w.ensureNetProbe(ctx)
		if err != nil {
			// Fail closed: a requested network deny that cannot be built
			// never silently becomes an unrestricted run.
			return nil, fmt.Errorf("%w: network isolation requested (sandbox.wsl.network: disabled) but this distribution cannot create network namespaces: %v",
				ErrBackendUnavailable, err)
		}
		linuxArgv = append(linuxArgv, probe.unshare, "--user", "--net", "--map-root-user")
	}
	linuxArgv = append(linuxArgv, "/usr/bin/env")
	linuxArgv = append(linuxArgv, req.ExtraEnv...)

	command := req.Command
	var cleanup func()
	if commandBudget(command, req.ExtraEnv) <= wslArgBudget {
		linuxArgv = append(linuxArgv, "/bin/bash", "-lc", command)
	} else {
		script, remove, err := writeWslScript(req.ExtraEnv, command)
		if err != nil {
			return nil, fmt.Errorf("staging large command for WSL: %w", err)
		}
		cleanup = remove
		linuxArgv = append(linuxArgv, "/bin/bash", "-l", script)
		// Env pairs were folded into the staged script; don't duplicate.
		linuxArgv = trimEnvPairs(linuxArgv, req.ExtraEnv)
	}

	args := append([]string{}, distroFlagArgs(distro)...)
	if cd := wslCdPath(dir); cd != "" {
		args = append(args, "--cd", cd)
	}
	args = append(args, "--exec")
	args = append(args, linuxArgv...)

	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = restrictedEnv()
	return &Prepared{Cmd: cmd, Dir: dir, Cleanup: cleanup}, nil
}

// trimEnvPairs removes the leading "/usr/bin/env" + pairs section once the
// payload has been spilled into a staged script that exports them itself.
func trimEnvPairs(argv, extra []string) []string {
	if len(extra) == 0 {
		return argv
	}
	// argv starts [unshare?] "/usr/bin/env" k=v...
	i := 0
	for i < len(argv) && argv[i] != "/usr/bin/env" {
		i++
	}
	end := i + 1 + len(extra)
	if end > len(argv) {
		return argv
	}
	return append(append([]string{}, argv[:i]...), argv[end:]...)
}

// Describe reports enforcement truthfully, probing lazily for network
// capability so the wording is never aspirational.
func (w *wslExecutor) Describe(ctx context.Context) Enforcement {
	e := Enforcement{
		Mode:            ModeWSL,
		Distro:          resolveDistro(w.policy.Distro),
		Network:         w.policy.effectiveNetwork(),
		CwdPinned:       true,
		EnvForwarded:    false,
		NetworkEnforced: false,
		Notes: []string{
			"the distribution can reach all Windows drives through /mnt and its own filesystem; only the working directory is validated",
			"filesystem tools (read_file, write_file, list_files) are confined to the project workspace via tool-layer policy",
		},
	}
	if e.Network == NetworkDisabled {
		if _, err := w.ensureNetProbe(ctx); err == nil {
			e.NetworkEnforced = true
			e.Notes = append(e.Notes,
				"commands run in an unprivileged user+network namespace (loopback only) while network isolation is active",
			)
		} else {
			e.Notes = append(e.Notes,
				fmt.Sprintf("network isolation cannot be established here (%v); affected commands refuse to run rather than fall back", err),
			)
		}
	} else {
		e.Notes = append(e.Notes, "network isolation was not requested; the command shares WSL/host networking")
	}
	return e
}

var _ Executor = (*wslExecutor)(nil)
