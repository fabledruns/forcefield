//go:build windows

package shell

import (
	"context"
	"strings"
	"testing"
)

// These tests exercise the full Shell tool against a real WSL backend.
// They skip cleanly when WSL is unavailable (e.g. CI without WSL); the
// invocation-shape and failure-path behavior is unit-tested in
// internal/sandbox.

func TestShell_EnvSurvivesWSLBoundary(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	result, err := s.Execute(context.Background(), map[string]any{
		"command": `printf 'A=[%s] B=[%s]' "$FF_WSL_A" "$FF_WSL_B"`,
		"env": map[string]any{
			"FF_WSL_A": "simple",
			"FF_WSL_B": "with spaces 'quotes' and $shell chars",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("result.IsError = true, content: %s", result.Content)
	}
	want := "A=[simple] B=[with spaces 'quotes' and $shell chars]"
	if !strings.Contains(result.Stdout, want) {
		t.Errorf("Stdout = %q, want it to contain %q (env values must cross the WSL boundary verbatim)", result.Stdout, want)
	}
}

func TestShell_LargeCommandSpillToScriptRuns(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	// A command far over the ~32K command-line limit forces the staged
	// script path; it must still execute as ordinary Bash.
	command := "for i in 1 2 3; do echo line-$i-" + strings.Repeat("p", 12000) + "; done"
	result, err := s.Execute(context.Background(), map[string]any{
		"command":         command,
		"timeout_seconds": 30,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("result.IsError = true, content: %.200s", result.Content)
	}
	if !strings.Contains(result.Stdout, "line-1-") || !strings.Contains(result.Stdout, "line-3-") {
		t.Errorf("Stdout does not contain the expected loop output (%d bytes)", len(result.Stdout))
	}
}

func TestShell_PosixCwdValidatedInsideDistro(t *testing.T) {
	requireShellBackend(t)
	s := NewShell()
	result, err := s.Execute(context.Background(), map[string]any{
		"command": "pwd",
		"cwd":     "/definitely-not-a-real-directory-xyz",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError {
		t.Fatal("result.IsError = false, want true for a nonexistent POSIX cwd")
	}
	if !strings.Contains(result.Content, "working directory") {
		t.Errorf("Content = %q, want a working-directory message (not a silent fallback to /)", result.Content)
	}
}
