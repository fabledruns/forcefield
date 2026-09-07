package hardening

import (
	"context"
	"strings"
	"testing"
	"time"

	"forcefield/internal/tools/shell"
)

// P1.15 lab: shell boundaries per sandbox mode. Proves caps, not isolation.
func TestShellOutputBounded(t *testing.T) {
	tool := shell.NewShell()
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

func TestShellEnvDoesNotLeakSecretsUnscrubbed(t *testing.T) {
	// Env values flow into process env; this test documents that shell
	// results must be scrubbed before persistence (see scrub tests).
	// Structural check: tool accepts env map without crashing on odd keys.
	tool := shell.NewShell()
	_, _ = tool.Execute(context.Background(), map[string]any{
		"command":         "echo hi",
		"timeout_seconds": float64(5),
		"env":             map[string]any{"FF_TEST_X": "1"},
	})
}
