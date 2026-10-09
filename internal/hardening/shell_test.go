package hardening

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools/shell"
)

// P1.15 lab: shell boundaries per sandbox mode. Proves caps, not isolation.
func TestShellOutputBounded(t *testing.T) {
	tool := shell.NewShell()
	probe, err := tool.Execute(context.Background(), map[string]any{
		"command": "python3 --version",
	})
	if err != nil || probe.IsError {
		t.Skipf("python3 unavailable: err=%v content=%q", err, probe.Content)
	}
	// yes-style unbounded output must be truncated, not OOM.
	res, err := tool.Execute(context.Background(), map[string]any{
		"command":         "python3 -c \"print('x'*10000000)\"",
		"timeout_seconds": float64(20),
	})
	if err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	// Capture is capped at 2MiB shared stdout+stderr; Content duplicates
	// those streams for model consumption, so the in-mem Result is bounded
	// at ~2x cap + marker overhead. Runtime truncates to 6000 chars before
	// the model. Unbounded would be 10MB+ here.
	combined := len(res.Content) + len(res.Stdout) + len(res.Stderr)
	if combined > 5<<20 {
		t.Fatalf("shell output unbounded: %d bytes", combined)
	}
	if !strings.Contains(res.Content, "truncat") {
		t.Fatalf("10MB output missing truncation marker (bounded=%v)", combined < 5<<20)
	}
}

func TestShellTimeoutEnforced(t *testing.T) {
	tool := shell.NewShell()
	start := time.Now()
	res, err := tool.Execute(context.Background(), map[string]any{
		"command":         "python3 -c \"import time; time.sleep(30)\"",
		"timeout_seconds": float64(2),
	})
	if err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("timeout not enforced: took %v", elapsed)
	}
	if !res.IsError {
		t.Fatalf("expected timeout error, got success: %#v", res)
	}
}

func TestShellTimeoutCeilingRejected(t *testing.T) {
	tool := shell.NewShell()
	_, err := tool.Execute(context.Background(), map[string]any{
		"command":         "echo hi",
		"timeout_seconds": float64(999999),
	})
	if err == nil {
		t.Fatalf("timeout_seconds=999999 accepted; must be rejected or clamped to 300s ceiling")
	}
}

func TestShellCancelStopsProcess(t *testing.T) {
	tool := shell.NewShell()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var resErr error
	go func() {
		defer close(done)
		_, resErr = tool.Execute(ctx, map[string]any{
			"command":         "python3 -c \"import time; time.sleep(30)\"",
			"timeout_seconds": float64(60),
		})
		_ = resErr
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatalf("cancel did not stop shell within 15s")
	}
}

func TestShellChildDoesNotSeeProviderKey(t *testing.T) {
	// Fake markers, never real credentials. The child prints only
	// LEAKED/CLEAN/BENIGN-OK so values stay out of logs entirely.
	//
	// Leak-detection power is platform-dependent: on Unix the child
	// inherits the host environment, so disabling the strip list flips
	// this test to LEAKED (verified by mutation). On Windows the relay
	// reaches Bash through wsl.exe, which forwards no host variables
	// without WSLENV, so the distribution child is CLEAN with or
	// without stripping; the launcher-side filter there is pinned by
	// TestNativePrepareStripsCredentials instead.
	const canaryName = "FF_SHELL_CRED_CANARY"
	const benignName = "FF_SHELL_BENIGN_HOST"
	t.Setenv(canaryName, "shell-child-must-not-see")
	t.Setenv(benignName, "visible")

	ex, err := sandbox.NewExecutor(sandbox.Policy{Mode: sandbox.ModeNative, CredentialEnv: []string{canaryName}})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	tool := shell.NewShellWithExecutor(ex)
	ctx := context.Background()
	if probe, err := tool.Execute(ctx, map[string]any{"command": "echo hi"}); err != nil || probe.IsError {
		t.Skipf("shell backend unavailable: err=%v content=%q", err, probe.Content)
	}

	res, err := tool.Execute(ctx, map[string]any{
		"command": `[ -n "$FF_SHELL_CRED_CANARY" ] && echo LEAKED || echo CLEAN`,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(res.Content, "LEAKED") {
		t.Fatal("policy-listed credential variable visible to shell child")
	}
	if !strings.Contains(res.Content, "CLEAN") {
		t.Fatalf("unexpected child output: %q", res.Content)
	}

	// A host-set unlisted variable flows on Unix. On Windows the
	// native relay reaches Bash through wsl.exe, which forwards no
	// host variables without WSLENV (deliberately empty here), so the
	// distribution child cannot see it by platform design.
	res, err = tool.Execute(ctx, map[string]any{
		"command": `[ "$FF_SHELL_BENIGN_HOST" = visible ] && echo BENIGN-OK || echo BENIGN-MISSING`,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if runtime.GOOS == "windows" {
		if !strings.Contains(res.Content, "BENIGN-MISSING") {
			t.Fatalf("Windows relay child unexpectedly saw a host variable: %q", res.Content)
		}
	} else if !strings.Contains(res.Content, "BENIGN-OK") {
		t.Fatalf("unlisted variable missing from child environment: %q", res.Content)
	}

	// Explicit per-command env reaches the child on every platform:
	// stripping applies to inheritance, never to deliberate values.
	res, err = tool.Execute(ctx, map[string]any{
		"command": `[ "$FF_SHELL_BENIGN_ARG" = "on-purpose" ] && echo ARG-OK || echo ARG-MISSING`,
		"env":     map[string]any{"FF_SHELL_BENIGN_ARG": "on-purpose"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "ARG-OK") {
		t.Fatalf("explicit per-command env missing from child environment: %q", res.Content)
	}
}
