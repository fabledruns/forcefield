package runtime

import (
	"context"
	"strings"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/mcp"
	"forcefield/internal/permissions"
	"forcefield/internal/providers"
)

func boolPtr(b bool) *bool { return &b }

// TestMCPAvailability_EligibleAgentsExposeDiscoveredTools verifies that discovered
// MCP tools are exposed to eligible agent types (general, coding, devops),
// while non-eligible or pure-text agents do not expose them unless explicitly requested.
func TestMCPAvailability_EligibleAgentsExposeDiscoveredTools(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	_ = startMCPHostForTest(t, cfg, full)

	registry := agent.DefaultRegistry()

	// 1. Eligible built-in agents (general, coding, devops) automatically expose the MCP tool.
	for _, name := range []string{"general", "coding", "devops"} {
		def, err := registry.Get(name)
		if err != nil {
			t.Fatalf("registry.Get(%q): %v", name, err)
		}
		resolved, _ := resolveAgentDefinitionTools(full, def)
		mgr, err := full.Filtered(resolved)
		if err != nil {
			t.Fatalf("agent %q Filtered: %v", name, err)
		}
		if _, ok := mgr.Lookup("mcp__demo__echo"); !ok {
			t.Errorf("eligible agent %q missing discovered MCP tool mcp__demo__echo", name)
		}
	}

	// 2. Non-eligible restricted agents (legal, cyber, docs, research) do NOT automatically expose the tool.
	for _, name := range []string{"legal", "cyber", "docs", "research"} {
		def, err := registry.Get(name)
		if err != nil {
			t.Fatalf("registry.Get(%q): %v", name, err)
		}
		resolved, _ := resolveAgentDefinitionTools(full, def)
		mgr, err := full.Filtered(resolved)
		if err != nil {
			t.Fatalf("agent %q Filtered: %v", name, err)
		}
		if _, ok := mgr.Lookup("mcp__demo__echo"); ok {
			t.Errorf("restricted agent %q should not expose discovered MCP tool mcp__demo__echo", name)
		}
	}

	// 3. Pure-text agent (explicitly empty tools slice) gets NO tools.
	pureTextDef := agent.Definition{
		Name:         "writer",
		Description:  "pure text agent",
		SystemPrompt: "write text",
		Tools:        []string{},
	}
	resolvedText, _ := resolveAgentDefinitionTools(full, pureTextDef)
	if len(resolvedText) != 0 {
		t.Errorf("pure text agent got tools: %v, want empty", resolvedText)
	}

	// 4. Custom agent without MCP tools in its allow-list does not expose them.
	customDef := agent.Definition{
		Name:         "custom",
		Description:  "custom agent",
		SystemPrompt: "custom",
		Tools:        []string{"read_file"},
	}
	resolvedCustom, _ := resolveAgentDefinitionTools(full, customDef)
	mgrCustom, err := full.Filtered(resolvedCustom)
	if err != nil {
		t.Fatalf("custom Filtered: %v", err)
	}
	if _, ok := mgrCustom.Lookup("mcp__demo__echo"); ok {
		t.Errorf("custom agent without MCP in allowlist should not expose mcp__demo__echo")
	}

	// 5. Custom agent that explicitly names the MCP tool DOES expose it.
	customOptInDef := agent.Definition{
		Name:         "custom_opt_in",
		Description:  "custom agent with mcp",
		SystemPrompt: "custom",
		Tools:        []string{"read_file", "mcp__demo__echo"},
	}
	resolvedOptIn, _ := resolveAgentDefinitionTools(full, customOptInDef)
	mgrOptIn, err := full.Filtered(resolvedOptIn)
	if err != nil {
		t.Fatalf("custom_opt_in Filtered: %v", err)
	}
	if _, ok := mgrOptIn.Lookup("mcp__demo__echo"); !ok {
		t.Errorf("custom_opt_in agent should expose mcp__demo__echo")
	}
}

// TestMCPAvailability_DelegatedChildAgents verifies that child agents spawned via
// orchestration also receive discovered MCP tools if they are eligible types.
func TestMCPAvailability_DelegatedChildAgents(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	_ = startMCPHostForTest(t, cfg, full)

	registry := agent.DefaultRegistry()
	orch := newOrchestrator(config.Orchestration{Enabled: true, MaxDepth: intPtr(2)}, registry, nil, full)

	// Child coding agent gets mcp__demo__echo.
	codingDef, _ := registry.Get("coding")
	codingMgr, err := orch.childAgentManager(codingDef, false)
	if err != nil {
		t.Fatalf("childAgentManager coding: %v", err)
	}
	if _, ok := codingMgr.Lookup("mcp__demo__echo"); !ok {
		t.Errorf("delegated child coding agent missing discovered MCP tool")
	}

	// Child legal agent does NOT get mcp__demo__echo.
	legalDef, _ := registry.Get("legal")
	legalMgr, err := orch.childAgentManager(legalDef, false)
	if err != nil {
		t.Fatalf("childAgentManager legal: %v", err)
	}
	if _, ok := legalMgr.Lookup("mcp__demo__echo"); ok {
		t.Errorf("delegated child legal agent should not have discovered MCP tool")
	}
}

// TestMCPAvailability_DisabledServerToolsRemainUnavailable verifies that tools from
// explicitly disabled servers (enabled: false) are never exposed.
func TestMCPAvailability_DisabledServerToolsRemainUnavailable(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"disabled_demo": {
			Command: mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}).Command,
			Args:    mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}).Args,
			Env:     mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}).Env,
			Enabled: boolPtr(false),
		},
	})
	_ = startMCPHostForTest(t, cfg, full)

	registry := agent.DefaultRegistry()
	generalDef, _ := registry.Get("general")
	resolved, _ := resolveAgentDefinitionTools(full, generalDef)
	mgr, err := full.Filtered(resolved)
	if err != nil {
		t.Fatalf("Filtered: %v", err)
	}

	for _, d := range mgr.Definitions() {
		if strings.HasPrefix(d.Name, "mcp__disabled_demo__") {
			t.Errorf("disabled server tool %q exposed in general agent manager", d.Name)
		}
	}
}

// TestMCPAvailability_AgentDiscoversAndInvokesMockedMCPTool tests an end-to-end
// StreamChat run where the agent discovers the MCP tool and executes it against
// the mocked MCP server process.
func TestMCPAvailability_AgentDiscoversAndInvokesMockedMCPTool(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "inspect"}),
	})
	host := startMCPHostForTest(t, cfg, full)

	registry := agent.DefaultRegistry()
	codingDef, _ := registry.Get("coding")
	resolved, _ := resolveAgentDefinitionTools(full, codingDef)
	mgr, err := full.Filtered(resolved)
	if err != nil {
		t.Fatalf("Filtered: %v", err)
	}

	// Verify discovery
	tool, ok := mgr.Lookup("mcp__demo__inspect")
	if !ok {
		t.Fatal("mcp__demo__inspect not discovered in coding agent manager")
	}
	if tool.Name() != "mcp__demo__inspect" {
		t.Fatalf("tool name = %q, want mcp__demo__inspect", tool.Name())
	}

	// Scripted provider: turn 1 calls the MCP tool, turn 2 receives result and completes.
	turns := [][]providers.StreamEvent{
		{
			{Text: "Invoking MCP tool..."},
			{ToolCalls: []providers.ToolCall{{
				ID:        "call-mcp-1",
				Name:      "mcp__demo__inspect",
				Arguments: map[string]any{"path": "test"},
			}}},
			{Done: true},
		},
		{
			{Text: "Tool finished successfully."},
			{Done: true},
		},
	}
	provider := &scriptedProvider{turns: turns}

	perms := newTestPermManager(t, permissions.Ask, map[string]permissions.Decision{
		"mcp__demo__inspect": permissions.Allow,
	})

	rt := &Runtime{
		cfg:         cfg,
		provider:    provider,
		fullManager: full,
		manager:     mgr,
		mcpHost:     host,
		agents:      registry,
		activeAgent: "coding",
		scheduler:   newScheduler(mgr, perms, nil, DefaultSchedulerConfig),
		limits:      DefaultLimits,
	}
	rt.agent = rt.buildAgent(codingDef)

	events, err := rt.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "please run inspect"},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	foundToolStart := false
	foundToolFinish := false
	var toolResultContent string

	for ev := range events {
		switch ev.Type {
		case EventToolStart:
			if ev.ToolCall != nil && ev.ToolCall.Name == "mcp__demo__inspect" {
				foundToolStart = true
			}
		case EventToolFinish:
			if ev.ToolResult != nil && ev.ToolResult.Name == "mcp__demo__inspect" {
				foundToolFinish = true
				toolResultContent = ev.ToolResult.Content
			}
		}
	}

	if !foundToolStart {
		t.Error("EventToolStart for mcp__demo__inspect not observed")
	}
	if !foundToolFinish {
		t.Error("EventToolFinish for mcp__demo__inspect not observed")
	}
	if !strings.Contains(toolResultContent, "ok:inspect") {
		t.Errorf("tool result content = %q, want ok:inspect from mocked server", toolResultContent)
	}
}

// TestMCPAvailability_ExplicitPermissionDenyBlocksExecution verifies that an explicit
// Deny permission blocks invocation of an available MCP tool.
func TestMCPAvailability_ExplicitPermissionDenyBlocksExecution(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "inspect"}),
	})
	host := startMCPHostForTest(t, cfg, full)

	registry := agent.DefaultRegistry()
	generalDef, _ := registry.Get("general")
	resolved, _ := resolveAgentDefinitionTools(full, generalDef)
	mgr, err := full.Filtered(resolved)
	if err != nil {
		t.Fatalf("Filtered: %v", err)
	}

	turns := [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{
				ID:        "call-mcp-1",
				Name:      "mcp__demo__inspect",
				Arguments: map[string]any{},
			}}},
			{Done: true},
		},
		{
			{Text: "Handling denial"},
			{Done: true},
		},
	}
	provider := &scriptedProvider{turns: turns}

	perms := newTestPermManager(t, permissions.Ask, map[string]permissions.Decision{
		"mcp__demo__inspect": permissions.Deny,
	})

	rt := &Runtime{
		cfg:         cfg,
		provider:    provider,
		fullManager: full,
		manager:     mgr,
		mcpHost:     host,
		agents:      registry,
		activeAgent: "general",
		scheduler:   newScheduler(mgr, perms, nil, DefaultSchedulerConfig),
		limits:      DefaultLimits,
	}
	rt.agent = rt.buildAgent(generalDef)

	events, err := rt.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "inspect"},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	foundDenied := false
	for ev := range events {
		if ev.Type == EventToolDenied && ev.ToolResult != nil && ev.ToolResult.Name == "mcp__demo__inspect" {
			foundDenied = true
		}
	}
	if !foundDenied {
		t.Error("EventToolDenied not observed for denied MCP tool")
	}
}
