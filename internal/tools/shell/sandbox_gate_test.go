package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

// failingExecutor refuses every request, simulating a backend that cannot
// honor its policy (missing WSL, impossible network isolation). The Shell
// tool must surface the refusal instead of falling back to any other
// execution path.
type failingExecutor struct {
	prepareErr error
	probeErr   error
}

func (f *failingExecutor) Prepare(context.Context, sandbox.Request) (*sandbox.Prepared, error) {
	return nil, f.prepareErr
}
func (f *failingExecutor) Probe(context.Context) error { return f.probeErr }
func (f *failingExecutor) Describe(context.Context) sandbox.Enforcement {
	return sandbox.Enforcement{Mode: sandbox.ModeWSL, Network: sandbox.NetworkDisabled, CwdPinned: true}
}

// TestShell_ExecutorRefusalNeverFallsBack pins the core security
// property: when the configured executor refuses to run, no command is
// constructed through any other path and nothing executes.
func TestShell_ExecutorRefusalNeverFallsBack(t *testing.T) {
	refusal := fmt.Errorf("%w: WSL distribution unavailable", sandbox.ErrBackendUnavailable)
	s := NewShellWithExecutor(&failingExecutor{prepareErr: refusal, probeErr: refusal})

	result, err := s.Execute(context.Background(), map[string]any{"command": "echo pwned"})
	if err != nil {
		t.Fatalf("Execute() returned a Go error = %v, want a failed Result", err)
	}
	if !result.IsError {
		t.Fatalf("result.IsError = false, content: %s", result.Content)
	}
	if strings.TrimSpace(result.Stdout) == "pwned" {
		t.Fatal("command executed despite executor refusal: silent fallback confirmed - this must never happen")
	}
	if !strings.Contains(result.Content, "WSL distribution unavailable") {
		t.Errorf("Content = %q, want the executor's refusal reason", result.Content)
	}
	if result.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 (nothing ran)", result.ExitCode)
	}
}

// TestShell_ProbeFailureIsStructuredResult checks that probe refusals are
// stable across repeated calls (the outcome is cached per Shell).
func TestShell_ProbeFailureIsStructuredResult(t *testing.T) {
	s := NewShellWithExecutor(&failingExecutor{probeErr: errors.New("no wsl.exe")})

	for i := 0; i < 2; i++ {
		result, err := s.Execute(context.Background(), map[string]any{"command": "echo x"})
		if err != nil || !result.IsError || !strings.Contains(result.Content, "no wsl.exe") {
			t.Fatalf("run %d: err=%v content=%q, want a stable structured refusal", i+1, err, result.Content)
		}
	}
}

// TestShell_WorkspaceEscapeSurfacesAsToolError verifies the typed escape
// mapping (the executor owns scope; the tool owns presentation).
func TestShell_WorkspaceEscapeSurfacesAsToolError(t *testing.T) {
	refusal := fmt.Errorf("%w: X:\\elsewhere is outside workspace", sandbox.ErrWorkspaceEscape)
	s := NewShellWithExecutor(&failingExecutor{prepareErr: refusal})

	result, err := s.Execute(context.Background(), map[string]any{
		"command": "pwd",
		"cwd":     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "outside the allowed workspace") {
		t.Fatalf("result = %+v, want an explicit scope message", result)
	}
}
