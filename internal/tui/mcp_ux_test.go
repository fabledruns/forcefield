package tui

import (
	"strings"
	"testing"

	"forcefield/internal/permissions"
)

// Phase 6 agreement: an MCP tool prompt renders the UNSANDBOXED notice
// in place of the executor block it can never have, so approval never
// looks like a confined native tool.
func TestFooterMCPToolsLabelledUnsandboxed(t *testing.T) {
	p := &permissionPrompt{request: permissions.Request{
		Tool:      "mcp__docs__search",
		Arguments: map[string]any{"query": "x"},
	}}

	footer := p.footerPrompt("")
	if !strings.Contains(footer, "UNSANDBOXED") {
		t.Errorf("MCP prompt must state UNSANDBOXED:\n%s", footer)
	}
	if strings.Contains(footer, "Isolation") {
		t.Errorf("MCP prompt must not render an executor block:\n%s", footer)
	}
}
