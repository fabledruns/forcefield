package tui

import (
	"strings"
	"testing"

	"forcefield/internal/permissions"
)

// TestPermissionPromptScrubsSecrets pins that the approval modal never
// paints credentials: argument blocks and action descriptions are
// display-scrubbed while the approval decision itself is unaffected.
func TestPermissionPromptScrubsSecrets(t *testing.T) {
	const secret = "sk-12345678901234567890abcdef"
	p := &permissionPrompt{
		request: permissions.Request{
			Tool: "shell",
			Arguments: map[string]any{
				"command": `curl -H "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc12345678901234567890" https://api.example.com`,
			},
		},
	}
	if block := p.formatToolBlock(); strings.Contains(block, "eyJhbGci") || strings.Contains(block, secret) {
		t.Errorf("tool block leaked secret:\n%s", block)
	}
	if desc := p.actionDescription(); strings.Contains(desc, "eyJhbGci") {
		t.Errorf("action description leaked secret: %q", desc)
	}

	pw := &permissionPrompt{
		request: permissions.Request{
			Tool:      "write_file",
			Arguments: map[string]any{"path": "cfg.env", "content": "password = \"hunter2-hunter2\"\n"},
		},
	}
	if block := pw.formatToolBlock(); strings.Contains(block, "hunter2-hunter2") {
		t.Errorf("write block leaked secret:\n%s", block)
	}
}

// TestPermissionPromptScrubsNonStringArgs pins that non-string argument
// values (notably shell env objects) are display-scrubbed in the approval
// modal. Regression test for the audit finding that only string values
// were scrubbed while marshaled objects were painted raw.
func TestPermissionPromptScrubsNonStringArgs(t *testing.T) {
	const secret = "sk-12345678901234567890abcdef"
	p := &permissionPrompt{
		request: permissions.Request{
			Tool: "shell",
			Arguments: map[string]any{
				"command": "deploy",
				"env": map[string]any{
					"DEPLOY_TOKEN": secret,
					"RETRIES":      float64(3),
				},
			},
		},
	}
	block := p.formatToolBlock()
	if strings.Contains(block, secret) {
		t.Errorf("tool block leaked secret from non-string arg:\n%s", block)
	}
	// Non-secret content must still render so the approver sees intent.
	if !strings.Contains(block, "deploy") || !strings.Contains(block, "RETRIES") {
		t.Errorf("tool block hid non-secret content:\n%s", block)
	}
}
