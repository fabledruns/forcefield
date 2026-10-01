package shell

import (
	"context"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

// Phase 6 agreement: the WSL lexical interop refusal is consistent with
// the permission model — it fires inside tool execution regardless of
// any permission grant, reports a tool error (not a permission denial),
// names the mitigation honestly, and never reaches process startup.
func TestShell_WSLInteropRefusedDespiteGrant(t *testing.T) {
	exec := &fakeWSLExecutor{mode: sandbox.ModeWSL}
	s := NewShellWithExecutor(exec)
	for _, cmd := range []string{
		"curl.exe --version",
		"explorer.exe .",
		"notepad.exe file.txt",
		"setup.exe /s",
		"cat /mnt/c/Users/Admin/.env",
		"cat /run/WSL/interop",
	} {
		res, err := s.Execute(context.Background(), map[string]any{"command": cmd})
		if err != nil {
			t.Fatalf("Execute(%q) hard error = %v", cmd, err)
		}
		if !res.IsError {
			t.Errorf("Execute(%q) succeeded, want interop refusal", cmd)
		}
		lower := strings.ToLower(res.Content)
		if !strings.Contains(lower, "mitigation") {
			t.Errorf("Execute(%q) refusal must name the mitigation, got %q", cmd, res.Content)
		}
		if strings.Contains(lower, "sandboxed") && !strings.Contains(lower, "not a sandbox") {
			t.Errorf("Execute(%q) refusal must not claim a sandbox, got %q", cmd, res.Content)
		}
	}
}
