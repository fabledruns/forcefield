package tui

import (
	"strings"
	"testing"

	"forcefield/internal/permissions"
	"forcefield/internal/sandbox"
)

// The approval UI must derive its claims from the executor's Enforcement
// report. These tests pin that derivation so the interface can never
// quietly promise more isolation than the runtime delivers.

func TestFooterShowsWSLEnforcementBlock(t *testing.T) {
	p := &permissionPrompt{request: permissions.Request{
		Tool:      "shell",
		Arguments: map[string]any{"command": "go test ./..."},
		Execution: &sandbox.Enforcement{
			Mode:            sandbox.ModeWSL,
			Distro:          "Ubuntu",
			Network:         sandbox.NetworkDisabled,
			CwdPinned:       true,
			NetworkEnforced: true,
			Notes: []string{
				"the distribution can reach all Windows drives through /mnt and its own filesystem; only the working directory is validated",
			},
		},
	}}

	footer := p.footerPrompt("")
	for _, want := range []string{
		`go test ./...`,
		"Execution",
		"WSL (Ubuntu)",
		"Filesystem",
		"NOT blocked",
		"Network",
		"disabled - enforced",
		"Environment",
		"host variables are not forwarded",
		"Isolation",
		"WSL execution boundary",
		"/mnt",
	} {
		if !strings.Contains(footer, want) {
			t.Errorf("footer missing %q:\n%s", want, footer)
		}
	}
}

func TestFooterNativeSaysNoIsolation(t *testing.T) {
	p := &permissionPrompt{request: permissions.Request{
		Tool:      "shell",
		Arguments: map[string]any{"command": "ls"},
		Execution: &sandbox.Enforcement{Mode: sandbox.ModeNative},
	}}

	footer := p.footerPrompt("")
	if !strings.Contains(footer, "Isolation") || !strings.Contains(footer, "none") {
		t.Errorf("native footer must state isolation is none:\n%s", footer)
	}
	if !strings.Contains(footer, "full host environment") {
		t.Errorf("native footer must admit full env forwarding:\n%s", footer)
	}
	if strings.Contains(strings.ToLower(footer), "wsl execution boundary") {
		t.Errorf("native run must not be described as a WSL boundary:\n%s", footer)
	}
}

func TestFooterUnenforcedNetworkIsExplicit(t *testing.T) {
	p := &permissionPrompt{request: permissions.Request{
		Tool:      "shell",
		Arguments: map[string]any{"command": "curl example.com"},
		Execution: &sandbox.Enforcement{
			Mode:            sandbox.ModeWSL,
			Network:         sandbox.NetworkDisabled,
			CwdPinned:       true,
			NetworkEnforced: false,
			Notes:           []string{"network isolation cannot be established here (probe failed); affected commands refuse to run rather than fall back"},
		},
	}}

	footer := p.footerPrompt("")
	if !strings.Contains(footer, "disabled - NOT enforced") {
		t.Errorf("unenforced network deny must say so explicitly:\n%s", footer)
	}
}

func TestNonShellToolsKeepRiskNoteOnly(t *testing.T) {
	p := &permissionPrompt{request: permissions.Request{
		Tool:      "write_file",
		Arguments: map[string]any{"path": "x.txt"},
	}}

	footer := p.footerPrompt("")
	if !strings.Contains(footer, "creates or overwrites a file on disk") {
		t.Errorf("write_file risk note missing:\n%s", footer)
	}
	if strings.Contains(footer, "Isolation") {
		t.Errorf("tools without an executor must not render an enforcement block:\n%s", footer)
	}
}
