package sandbox

import (
	"context"
	"strings"
	"testing"
)

// Phase 1: the structured limitation model is the single source of truth
// doctor renders. These tests pin IDs, warn/info split, and agreement
// with SummaryLines without asserting any isolation beyond Phase 0.

func limitationIDs(e Enforcement) map[string]Limitation {
	out := make(map[string]Limitation, len(e.Limitations))
	for _, l := range e.Limitations {
		out[l.ID] = l
	}
	return out
}

func TestLimitationIDsStable(t *testing.T) {
	want := map[string]string{
		"LimFilesystemShellOpen":   "filesystem.shell-open",
		"LimFilesystemToolsCaged":  "filesystem.tools-caged",
		"LimShellTextOpen":         "shell.text-open",
		"LimShellStagedVisible":    "shell.staged-visible",
		"LimNetworkInterop":        "network.wsl-interop",
		"LimNetworkNamespace":      "network.namespace",
		"LimEnvFullHost":           "env.full-host",
		"LimEnvRestrictedLauncher": "env.restricted-launcher",
		"LimProcessUnixPgroup":     "process.unix-pgroup",
		"LimProcessWindowsJob":     "process.windows-job",
		"LimProcessWSLRelay":       "process.wsl-relay",
		"LimPlatformWSLWindows":    "platform.wsl-windows-only",
		"LimPlatformHostOnly":      "platform.host-only",
		"LimMCPUnsandboxed":        "mcp.unsandboxed",
	}
	got := map[string]string{
		"LimFilesystemShellOpen":   LimFilesystemShellOpen,
		"LimFilesystemToolsCaged":  LimFilesystemToolsCaged,
		"LimShellTextOpen":         LimShellTextOpen,
		"LimShellStagedVisible":    LimShellStagedVisible,
		"LimNetworkInterop":        LimNetworkInterop,
		"LimNetworkNamespace":      LimNetworkNamespace,
		"LimEnvFullHost":           LimEnvFullHost,
		"LimEnvRestrictedLauncher": LimEnvRestrictedLauncher,
		"LimProcessUnixPgroup":     LimProcessUnixPgroup,
		"LimProcessWindowsJob":     LimProcessWindowsJob,
		"LimProcessWSLRelay":       LimProcessWSLRelay,
		"LimPlatformWSLWindows":    LimPlatformWSLWindows,
		"LimPlatformHostOnly":      LimPlatformHostOnly,
		"LimMCPUnsandboxed":        LimMCPUnsandboxed,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("limitation ID %s = %q, want stable %q", k, got[k], w)
		}
	}
}

func TestNativeDescribeLimitationsHonest(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name   string
		policy Policy
		pinned bool
	}{
		{"strict", Policy{Mode: ModeNative, Workspace: dir, Strict: true}, true},
		{"permissive", Policy{Mode: ModeNative, Workspace: dir}, false},
	} {
		ex, err := NewExecutor(tc.policy)
		if err != nil {
			t.Fatalf("%s: NewExecutor: %v", tc.name, err)
		}
		d := ex.Describe(context.Background())
		if d.FilesystemConfined {
			t.Errorf("%s: FilesystemConfined=true overclaims shell confinement", tc.name)
		}
		if d.CwdPinned != tc.pinned {
			t.Errorf("%s: CwdPinned=%v, want %v", tc.name, d.CwdPinned, tc.pinned)
		}
		ids := limitationIDs(d)
		for _, id := range []string{LimFilesystemShellOpen, LimShellTextOpen} {
			l, ok := ids[id]
			if !ok {
				t.Errorf("%s: missing limitation %q", tc.name, id)
				continue
			}
			if !l.Warn {
				t.Errorf("%s: limitation %q must warn", tc.name, id)
			}
		}
		if _, ok := ids[LimFilesystemToolsCaged]; !ok {
			t.Errorf("%s: missing %q info", tc.name, LimFilesystemToolsCaged)
		}
		if len(d.Warnings()) == 0 {
			t.Errorf("%s: Warnings() empty, want shell-open warns", tc.name)
		}
		lines := strings.Join(d.SummaryLines(), "\n")
		if !strings.Contains(lines, "Isolation") || !strings.Contains(lines, "none") {
			t.Errorf("%s: SummaryLines must say Isolation none:\n%s", tc.name, lines)
		}
	}
}

func TestWarningsFiltersInfo(t *testing.T) {
	e := Enforcement{Limitations: []Limitation{
		{ID: "a", Detail: "info"},
		{ID: "b", Warn: true, Detail: "warn"},
	}}
	w := e.Warnings()
	if len(w) != 1 || w[0].ID != "b" {
		t.Fatalf("Warnings() = %+v, want only b", w)
	}
}

func TestMCPUnsandboxedLimitation(t *testing.T) {
	l := MCPUnsandboxedLimitation()
	if l.ID != LimMCPUnsandboxed {
		t.Errorf("ID = %q, want %q", l.ID, LimMCPUnsandboxed)
	}
	if !l.Warn {
		t.Error("MCP unsandboxed must warn")
	}
	if !strings.Contains(l.Detail, "UNSANDBOXED") {
		t.Errorf("Detail must state UNSANDBOXED plainly: %q", l.Detail)
	}
}
