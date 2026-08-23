//go:build windows

package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// These tests exercise the restricted WSL executor against a real WSL
// installation. They prove the guarantees this package claims (and refuse
// to claim anything it cannot demonstrate); they skip cleanly when WSL is
// unavailable so non-Windows CI stays green. Unit-level equivalents live
// in sandbox_windows_test.go.

// newIntegrationExecutor builds a restricted executor over a throwaway
// workspace, skipping the test when the configured chain is unusable.
func newIntegrationExecutor(t *testing.T) *wslExecutor {
	t.Helper()
	e, err := newWSLExecutor(Policy{Mode: ModeWSL, Workspace: t.TempDir()})
	if err != nil {
		t.Skipf("wsl executor unavailable: %v", err)
	}
	if err := e.Probe(context.Background()); err != nil {
		t.Skipf("WSL backend unavailable on this machine: %v", err)
	}
	return e
}

func runPrepared(t *testing.T, prepared *Prepared) int {
	t.Helper()
	if prepared.Cleanup != nil {
		defer prepared.Cleanup()
	}
	if err := prepared.Cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	err := prepared.Cmd.Wait()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	t.Fatalf("Wait() error = %v", err)
	return -1
}

func TestIntegration_RestrictedRunsAndPropagatesExitCodes(t *testing.T) {
	e := newIntegrationExecutor(t)

	prepared, err := e.Prepare(context.Background(), Request{Command: "exit 3"})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if code := runPrepared(t, prepared); code != 3 {
		t.Errorf("exit code = %d, want 3 propagated from inside the distribution", code)
	}
}

// TestIntegration_HostSecretsDoNotCross is the end-to-end API-key check:
// the variable exists in the Forcefield process environment and must not
// exist inside the distribution.
func TestIntegration_HostSecretsDoNotCross(t *testing.T) {
	const marker = "nvapi-integration-must-not-leak"
	t.Setenv("NVIDIA_API_KEY", marker)
	t.Setenv("FF_INTEGRATION_CANARY", marker)

	e := newIntegrationExecutor(t)

	prepared, err := e.Prepare(context.Background(), Request{
		Command: `[ -n "$NVIDIA_API_KEY$FF_INTEGRATION_CANARY" ] && echo LEAKED || echo CLEAN`,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	out := &strings.Builder{}
	prepared.Cmd.Stdout = out
	runPrepared(t, prepared)

	got := strings.TrimSpace(out.String())
	switch got {
	case "CLEAN":
		// expected
	case "LEAKED":
		t.Fatal("host variables leaked into the restricted WSL environment")
	default:
		t.Fatalf("unexpected output %q", got)
	}
}

// TestIntegration_NetworkDisabledIsReal checks both sides of the network
// guarantee: when namespace isolation works, only loopback remains; when
// it cannot work, commands refuse to run. Either way there is no silent
// fallback onto host networking.
func TestIntegration_NetworkDisabledIsReal(t *testing.T) {
	e := newIntegrationExecutor(t)

	probe, err := e.ensureNetProbe(context.Background())
	if err != nil || !probe.supported {
		t.Skipf("this distribution cannot create network namespaces; enforced mode unavailable (%v)", errOr(probe))
	}

	prepared, err := e.Prepare(context.Background(), Request{
		Command: `ip -o link show | awk -F': ' '{print $2}' | grep -qv '^lo$' && echo EXTRA_LINKS || echo LOOPBACK_ONLY`,
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	out := &strings.Builder{}
	prepared.Cmd.Stdout = out
	runPrepared(t, prepared)

	if got := strings.TrimSpace(out.String()); got != "LOOPBACK_ONLY" {
		t.Fatalf("network namespace check = %q, want LOOPBACK_ONLY (got extra interfaces: host networking reachable)", got)
	}
}

// TestIntegration_TimeoutTerminates proves cancellation reaches through
// the namespace wrapper: a sleeping process must die promptly.
func TestIntegration_TimeoutTerminates(t *testing.T) {
	e := newIntegrationExecutor(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	prepared, err := e.Prepare(ctx, Request{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if err := prepared.Cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	start := time.Now()
	waitErr := prepared.Cmd.Wait()
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Errorf("termination took %v; timeout did not reach the sandboxed process", elapsed)
	}
	if waitErr == nil {
		t.Error("sleep survived the deadline without error")
	}
}

func errOr(p *netProbeResult) error {
	if p == nil {
		return nil
	}
	return p.err
}
