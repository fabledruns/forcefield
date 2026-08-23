//go:build windows

package shell

import (
	"context"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

// These tests drive the full Shell tool through the restricted WSL
// executor on this machine's real backend. They prove the end-to-end
// contract - tool layer requests, executor enforces - rather than testing
// either layer in isolation. They skip when WSL cannot deliver the policy.

func newRestrictedTool(t *testing.T) (*Shell, string) {
	t.Helper()
	ws := t.TempDir()
	e, err := sandbox.NewExecutor(sandbox.Policy{
		Mode:      sandbox.ModeWSL,
		Workspace: ws,
		Network:   sandbox.NetworkDisabled,
	})
	if err != nil {
		t.Skipf("executor unavailable: %v", err)
	}
	if err := e.Probe(context.Background()); err != nil {
		t.Skipf("WSL restricted backend unavailable: %v", err)
	}
	return NewShellWithExecutor(e), ws
}

func TestIntegration_RestrictedToolRunsInWorkspace(t *testing.T) {
	s, _ := newRestrictedTool(t)

	result, err := s.Execute(context.Background(), map[string]any{
		"command": "pwd && echo SANDBOX_OK",
	})
	if err != nil || result.IsError {
		t.Fatalf("Execute failed: err=%v content=%.300s", err, result.Content)
	}
	if !strings.Contains(result.Stdout, "SANDBOX_OK") {
		t.Errorf("stdout = %q, want successful in-workspace execution", result.Stdout)
	}
}

func TestIntegration_RestrictedToolRejectsOutsideCwd(t *testing.T) {
	s, _ := newRestrictedTool(t)
	outside := t.TempDir() // a different temp root than the workspace

	result, err := s.Execute(context.Background(), map[string]any{
		"command": "pwd",
		"cwd":     outside,
	})
	if err != nil {
		t.Fatalf("Execute() returned a Go error = %v, want a Result", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "outside the allowed workspace") {
		t.Fatalf("result = %.200s, want explicit workspace rejection", result.Content)
	}
}

func TestIntegration_RestrictedToolEnvIsRestricted(t *testing.T) {
	const marker = "ff-restricted-marker-do-not-see"
	t.Setenv("NVIDIA_API_KEY", marker)

	s, _ := newRestrictedTool(t)

	result, err := s.Execute(context.Background(), map[string]any{
		"command": `[ -n "$NVIDIA_API_KEY" ] && echo LEAKED:$NVIDIA_API_KEY || echo CLEAN`,
	})
	if err != nil || result.IsError {
		t.Fatalf("Execute failed: err=%v content=%.300s", err, result.Content)
	}
	if strings.Contains(result.Stdout, "LEAKED") {
		t.Fatalf("API key leaked into sandboxed command output: %s", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "CLEAN") {
		t.Errorf("stdout = %q, want CLEAN", result.Stdout)
	}
}

func TestIntegration_RestrictedToolTimeoutStillKills(t *testing.T) {
	s, _ := newRestrictedTool(t)

	result, err := s.Execute(context.Background(), map[string]any{
		"command":         "sleep 30",
		"timeout_seconds": 2,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content, "timed out") {
		t.Fatalf("result = %.200s, want a timeout report", result.Content)
	}
}
