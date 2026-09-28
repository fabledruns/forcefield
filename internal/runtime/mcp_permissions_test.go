package runtime

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"forcefield/internal/mcp"
	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// MCP permission integration (Phase 7). No separate MCP permission
// system exists by design: mcp__* names flow through the standard
// manager/scheduler path, default to Ask, honor explicit per-tool
// overrides, and fail closed headless. Adapters claim no
// BoundaryChecker (pinned in internal/mcp), so MCP arguments are never
// treated as workspace-confined.

func TestMCPPermissionDefaultAsk(t *testing.T) {
	perms := newTestPermManager(t, permissions.Ask, nil)
	if got := perms.Check("mcp__demo__echo"); got != permissions.Ask {
		t.Errorf("Check(mcp tool) = %v, want Ask default", got)
	}
}

func TestMCPPermissionExplicitOverride(t *testing.T) {
	perms := newTestPermManager(t, permissions.Ask, map[string]permissions.Decision{
		"mcp__demo__echo":   permissions.Allow,
		"mcp__demo__other":  permissions.Deny,
		"mcp__other__thing": permissions.Deny,
	})
	if got := perms.Check("mcp__demo__echo"); got != permissions.Allow {
		t.Errorf("explicit allow = %v, want Allow", got)
	}
	if got := perms.Check("mcp__demo__other"); got != permissions.Deny {
		t.Errorf("explicit deny = %v, want Deny", got)
	}
	// Overrides are per qualified name: a sibling tool keeps the default.
	if got := perms.Check("mcp__demo__fresh"); got != permissions.Ask {
		t.Errorf("sibling tool = %v, want Ask default", got)
	}
}

func TestMCPPermissionDenyNeverExecutes(t *testing.T) {
	ran := false
	tool := &fnTool{name: "mcp__demo__echo", fn: func() { ran = true }}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, map[string]permissions.Decision{
		"mcp__demo__echo": permissions.Deny,
	})
	s := newScheduler(manager, perms, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: 0})
	results := s.Run(context.Background(),
		[]providers.ToolCall{{ID: "1", Name: "mcp__demo__echo", Arguments: map[string]any{}}},
		func(Event) bool { return true })
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("expected denied error result, got %+v", results)
	}
	if ran {
		t.Error("denied MCP tool executed")
	}
}

func TestMCPPermissionAllowExecutesLiveAdapter(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	host := startMCPHostForTest(t, cfg, full)
	_ = host
	tool, ok := full.Lookup("mcp__demo__echo")
	if !ok {
		t.Fatal("live adapter not registered")
	}
	// The Allow path must never consult the asker.
	asker := permissions.AskerFunc(func(context.Context, permissions.Request) (permissions.Prompt, error) {
		t.Error("asker consulted despite explicit Allow")
		return permissions.PromptDenyOnce, nil
	})
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, map[string]permissions.Decision{
		"mcp__demo__echo": permissions.Allow,
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: 0})
	results := s.Run(context.Background(),
		[]providers.ToolCall{{ID: "1", Name: "mcp__demo__echo", Arguments: map[string]any{}}},
		func(Event) bool { return true })
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("expected success, got %+v", results)
	}
	if !strings.Contains(results[0].Content, "echo") {
		t.Errorf("content = %q, want helper echo", results[0].Content)
	}
}

func TestMCPPermissionHeadlessFailsClosed(t *testing.T) {
	var transports []io.Closer
	t.Cleanup(func() {
		for _, c := range transports {
			_ = c.Close()
		}
	})
	adapter := newFakeMCPTool(t, &transports, "demo", "echo")
	manager := newTestManager(t, adapter)
	perms := newTestPermManager(t, permissions.Ask, nil)
	// nil asker: no interactive surface (automation). Must deny without
	// executing, exactly like any other ask-gated tool.
	s := newScheduler(manager, perms, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: 0})
	denied := false
	results := s.Run(context.Background(),
		[]providers.ToolCall{{ID: "1", Name: "mcp__demo__echo", Arguments: map[string]any{}}},
		func(e Event) bool {
			if e.Type == EventToolDenied {
				denied = true
			}
			return true
		})
	if !denied {
		t.Error("headless ask-gated MCP call missing EventToolDenied")
	}
	if len(results) != 1 || !results[0].IsError {
		t.Errorf("results = %+v, want one denied error", results)
	}
}

func TestMCPAdapterClaimsNoBoundary(t *testing.T) {

	var transports []io.Closer
	t.Cleanup(func() {
		for _, c := range transports {
			_ = c.Close()
		}
	})
	adapter := newFakeMCPTool(t, &transports, "demo", "echo")
	if _, ok := any(adapter).(tools.BoundaryChecker); ok {
		t.Error("MCP adapter must not claim BoundaryChecker: opaque arguments are not workspace-confined")
	}
}

func TestMCPPermissionAskApproveExecutes(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	host := startMCPHostForTest(t, cfg, full)
	_ = host
	tool, ok := full.Lookup("mcp__demo__echo")
	if !ok {
		t.Fatal("live adapter not registered")
	}
	// Ask + interactive approval executes through the normal scheduler
	// path: no separate MCP permission machinery involved.
	asked := false
	asker := permissions.AskerFunc(func(context.Context, permissions.Request) (permissions.Prompt, error) {
		asked = true
		return permissions.PromptAllowOnce, nil
	})
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: 0})
	results := s.Run(context.Background(),
		[]providers.ToolCall{{ID: "1", Name: "mcp__demo__echo", Arguments: map[string]any{}}},
		func(Event) bool { return true })
	if !asked {
		t.Error("asker not consulted on Ask-gated MCP call")
	}
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("expected approved success, got %+v", results)
	}
	if !strings.Contains(results[0].Content, "echo") {
		t.Errorf("content = %q, want helper echo", results[0].Content)
	}
}

func TestMCPSelection_MultiAgentFrozenSnapshot(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	startMCPHostForTest(t, cfg, full)
	before := lookupNames(full)
	// Two agents resolve against the same frozen universe: one opts in,
	// one does not, and neither resolution mutates the full manager.
	keepA, missingA := resolveAgentTools(full, []string{"read_file", "mcp__demo__echo"})
	keepB, missingB := resolveAgentTools(full, []string{"read_file"})
	if len(missingA) != 0 || len(missingB) != 0 {
		t.Errorf("missing = %v, %v; want none", missingA, missingB)
	}
	for name, keep := range map[string][]string{"a": keepA, "b": keepB} {
		filtered, err := full.Filtered(keep)
		if err != nil {
			t.Fatalf("agent %s Filtered: %v", name, err)
		}
		_, ok := filtered.Lookup("mcp__demo__echo")
		if (name == "a") == !ok {
			t.Errorf("agent %s visibility wrong for mcp__demo__echo", name)
		}
	}
	after := lookupNames(full)
	if fmt.Sprintf("%v", before) != fmt.Sprintf("%v", after) {
		t.Errorf("full universe mutated by filtering: %v -> %v", before, after)
	}
}
