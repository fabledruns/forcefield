//go:build windows

package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Windows .exe interop canary (Phase 5).
//
// The Linux user+network namespace does not cover Windows-side
// execution: .exe files reachable through interop run inside the
// namespace on the host network stack, even with an empty environment
// (verified Phase 0). This probe resolves which interop launchers are
// visible from INSIDE the isolated namespace. It runs only `command -v`
// lookups: no network traffic, no external targets, no command
// execution beyond the shell builtin.
//
// A positive result is concrete evidence for this machine; a negative
// result never weakens the structural limitation (interop availability
// varies by distribution, mount, and WSL version), so doctor always
// reports network.wsl-interop as a warning regardless.

// interopCanaryTimeout bounds the canary spawn. The distribution is
// already warm (doctor probes the backend first), so this is generous.
const interopCanaryTimeout = 15 * time.Second

// interopCanaryHelpers are the Windows launchers the canary resolves.
// They mirror the lexical mitigation's named cases plus the shell
// entry points, so doctor evidence and refusal messages agree.
var interopCanaryHelpers = []string{
	"cmd.exe", "powershell.exe", "curl.exe", "explorer.exe", "wsl.exe", "notepad.exe",
}

// InteropCanary reports the subset of interopCanaryHelpers resolvable
// from inside the policy's network-isolated namespace, using the same
// distribution resolution as Prepare (never silently the default when
// configured otherwise). It applies to network: disabled only: other
// modes make no isolation claim to check.
func (w *wslExecutor) InteropCanary(ctx context.Context) ([]string, error) {
	if w.policy.effectiveNetwork() != NetworkDisabled {
		return nil, fmt.Errorf("interop canary applies to network: disabled only")
	}
	probe, err := w.ensureNetProbe(ctx)
	if err != nil {
		return nil, fmt.Errorf("interop canary needs the network namespace: %w", err)
	}
	if !probe.supported {
		return nil, fmt.Errorf("interop canary needs the network namespace: unsupported here")
	}
	exe, err := wslExePath()
	if err != nil {
		return nil, wslMissingError()
	}
	// No user-controlled content enters this probe string: the helper
	// list is fixed above and the argv is assembled, never shelled.
	script := `for c in cmd.exe powershell.exe curl.exe explorer.exe wsl.exe notepad.exe; do command -v "$c" >/dev/null 2>&1 && echo "FOUND:$c"; done; echo DONE`
	args := append(distroFlagArgs(resolveDistro(w.policy.Distro)),
		"--exec", probe.unshare, "--user", "--net", "--map-root-user",
		"/bin/sh", "-c", script)
	cctx, cancel := context.WithTimeout(ctx, interopCanaryTimeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, exe, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("interop canary spawn failed: %w", err)
	}
	var found []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(decodeWSLText([]byte(line)))
		name, ok := strings.CutPrefix(line, "FOUND:")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		found = append(found, strings.TrimSpace(name))
	}
	return found, nil
}
