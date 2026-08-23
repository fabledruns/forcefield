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
	for _, option := range []string{"(y)", "(n)", "(a)", "(d)", "(esc)"} {
		if !strings.Contains(footer, option) {
			t.Errorf("footerPrompt() missing option %s in %q", option, footer)
		}
	}

	safe := &permissionPrompt{request: permissions.Request{
		Tool:      "read_file",
		Arguments: map[string]any{"path": "go.mod"},
	}}
	if strings.Contains(safe.footerPrompt(""), "permissions") {
		t.Errorf("read_file footer adds a risk note it doesn't need: %q", safe.footerPrompt(""))
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
