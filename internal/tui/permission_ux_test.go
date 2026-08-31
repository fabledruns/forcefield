package tui

import (
	"strings"
	"testing"

	"forcefield/internal/permissions"
)

func TestPermissionActionDescriptionShell(t *testing.T) {
	p := &permissionPrompt{request: permissions.Request{
		Tool: "shell",
		Arguments: map[string]any{
			"command": "go test ./...",
			"cwd":     `C:\repo`,
		},
	}}

	got := p.actionDescription()
	want := `shell "go test ./..." in C:\repo`
	if got != want {
		t.Errorf("actionDescription() = %q, want %q", got, want)
	}
}

func TestPermissionActionDescriptionFileTools(t *testing.T) {
	p := &permissionPrompt{request: permissions.Request{
		Tool:      "write_file",
		Arguments: map[string]any{"path": "main.go", "content": "..."},
	}}
	if got := p.actionDescription(); got != "write_file main.go" {
		t.Errorf("actionDescription() = %q, want the path form", got)
	}

	p = &permissionPrompt{request: permissions.Request{
		Tool:      "list_files",
		Arguments: map[string]any{},
	}}
	if got := p.actionDescription(); got != "list_files ." {
		t.Errorf("actionDescription() = %q, want \"list_files .\"", got)
	}
}

func TestPermissionUnknownToolFallsBackToFullArguments(t *testing.T) {
	p := &permissionPrompt{request: permissions.Request{
		Tool:      "mystery_tool",
		Arguments: map[string]any{"secret_arg": "value"},
	}}

	got := p.actionDescription()
	if !strings.Contains(got, "secret_arg") || !strings.Contains(got, "value") {
		t.Errorf("actionDescription() = %q, want full arguments preserved", got)
	}
}

func TestPermissionFooterShowsRiskNote(t *testing.T) {
	p := &permissionPrompt{request: permissions.Request{
		Tool:      "shell",
		Arguments: map[string]any{"command": "rm -rf build"},
	}}

	footer := p.footerPrompt("")
	if !strings.Contains(footer, "with your user's permissions") {
		t.Errorf("footerPrompt() = %q, want an honest scope statement", footer)
	}
	for _, option := range []string{"Allow once", "Always allow", "Deny", "Always deny"} {
		if !strings.Contains(footer, option) {
			t.Errorf("footerPrompt() missing option %q in %q", option, footer)
		}
	}
	if !strings.Contains(footer, "↑/↓") || !strings.Contains(footer, "enter") || !strings.Contains(footer, "esc") {
		t.Errorf("footerPrompt() missing navigation hint in %q", footer)
	}
	// New UI must not dump raw JSON as the primary presentation
	if strings.Contains(footer, `"command":"rm -rf build"`) {
		t.Errorf("footerPrompt() shows raw JSON as primary UI: %q", footer)
	}
	if !strings.Contains(footer, "Tool: shell") || !strings.Contains(footer, "command:") {
		t.Errorf("footerPrompt() missing clean tool block in %q", footer)
	}

	safe := &permissionPrompt{request: permissions.Request{
		Tool:      "read_file",
		Arguments: map[string]any{"path": "go.mod"},
	}}
	if strings.Contains(safe.footerPrompt(""), "permissions") {
		t.Errorf("read_file footer adds a risk note it doesn't need: %q", safe.footerPrompt(""))
	}
	if !strings.Contains(safe.footerPrompt(""), "Tool: read_file") {
		t.Errorf("footerPrompt() missing clean tool block for read_file: %q", safe.footerPrompt(""))
	}
}

func TestPermissionSummaryPrefixesRequest(t *testing.T) {
	p := &permissionPrompt{request: permissions.Request{
		Tool:      "shell",
		Arguments: map[string]any{"command": "make"},
	}}
	if s := p.summary(); !strings.HasPrefix(s, "permission requested:") || !strings.Contains(s, "make") {
		t.Errorf("summary() = %q", s)
	}
}

func TestPermissionSelectableNavigation(t *testing.T) {
	ch0 := make(chan permissions.Prompt, 1)
	m := model{
		permissionPrompt: &permissionPrompt{
			request:  permissions.Request{Tool: "shell", Arguments: map[string]any{"command": "echo hi"}},
			respond:  ch0,
			selected: 0,
		},
	}
	// down moves to 1
	m, ok := m.handlePermissionKey("down")
	if !ok || m.permissionPrompt.selected != 1 {
		t.Fatalf("down: selected=%d want 1 ok=%v", m.permissionPrompt.selected, ok)
	}
	// up wraps
	m, _ = m.handlePermissionKey("up")
	if m.permissionPrompt.selected != 0 {
		t.Fatalf("up: selected=%d want 0", m.permissionPrompt.selected)
	}
	// enter confirms selected (Allow once)
	m, ok = m.handlePermissionKey("enter")
	if !ok || m.permissionPrompt != nil {
		t.Fatalf("enter should resolve prompt")
	}
	if got := <-ch0; got != permissions.PromptAllowOnce {
		t.Fatalf("enter on Allow once: got %v want AllowOnce", got)
	}
	// Recreate to test enter value for Deny
	ch := make(chan permissions.Prompt, 1)
	m2 := model{permissionPrompt: &permissionPrompt{request: permissions.Request{Tool: "shell"}, respond: ch, selected: 2}}
	m2, _ = m2.handlePermissionKey("enter")
	if got := <-ch; got != permissions.PromptDenyOnce {
		t.Fatalf("enter on Deny: got %v want DenyOnce", got)
	}
}

func TestPermissionEscDenies(t *testing.T) {
	ch := make(chan permissions.Prompt, 1)
	m := model{permissionPrompt: &permissionPrompt{request: permissions.Request{Tool: "shell"}, respond: ch, selected: 0}}
	m, ok := m.handlePermissionKey("esc")
	if !ok {
		t.Fatalf("esc not handled")
	}
	if got := <-ch; got != permissions.PromptDenyOnce {
		t.Fatalf("esc got %v want DenyOnce", got)
	}
	if m.permissionPrompt != nil {
		t.Fatalf("prompt not cleared after esc")
	}
}

func TestPermissionFooterShowsCleanBlockNotRawJSON(t *testing.T) {
	p := &permissionPrompt{request: permissions.Request{Tool: "test_tool", Arguments: map[string]any{"id": "intelligence", "extra": "val"}}, selected: 0}
	footer := p.footerPrompt("")
	if strings.Contains(footer, `{"id":"intelligence"}`) {
		t.Errorf("footer shows raw JSON %q", footer)
	}
	if !strings.Contains(footer, "Tool: test_tool") {
		t.Errorf("missing Tool block %q", footer)
	}
	if !strings.Contains(footer, "id:") || !strings.Contains(footer, "intelligence") {
		t.Errorf("missing formatted args %q", footer)
	}
}
