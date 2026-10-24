package tui

import (
	"strings"
	"testing"

	"forcefield/internal/permissions"
)

// TestPermissionPromptAttributesChildAsks [R] pins the resolved behavior
// for defect O3: the approval modal shows who is asking for a delegated
// child's call, with the same chain fallback the stdin asker uses — the
// full delegation chain (root first) when present, the child's name
// alone otherwise — and invents nothing for top-level asks. Display
// only: no answer path or prompt semantics change.
func TestPermissionPromptAttributesChildAsks(t *testing.T) {
	// The operator sees the delegation chain, root first.
	nested := &permissionPrompt{request: permissions.Request{
		Tool:      "write_file",
		Arguments: map[string]any{"path": "notes.md"},
		Origin:    &permissions.Origin{Agent: "researcher", Chain: "root > reviewer > researcher"},
	}}
	if block := nested.formatToolBlock(); !strings.Contains(block, "Requested by child agent: root > reviewer > researcher") {
		t.Fatalf("modal did not attribute the nested child ask by chain:\n%s", block)
	}

	// A stamped origin with no chain falls back to the child's name —
	// the attribution is honest, never an empty line.
	fallback := &permissionPrompt{request: permissions.Request{
		Tool:   "echo",
		Origin: &permissions.Origin{Agent: "docs"},
	}}
	if block := fallback.formatToolBlock(); !strings.Contains(block, "Requested by child agent: docs") {
		t.Fatalf("modal did not fall back to the child's name:\n%s", block)
	}

	// Top-level asks fabricate nothing: no origin line invented.
	plain := &permissionPrompt{request: permissions.Request{
		Tool:      "read_file",
		Arguments: map[string]any{"path": "main.go"},
	}}
	if block := plain.formatToolBlock(); strings.Contains(block, "Requested by child agent") {
		t.Fatalf("top-level ask created an attribution line:\n%s", block)
	}
}
