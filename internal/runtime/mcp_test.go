package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/mcp"
	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// Phase 6 runtime integration tests.
//
// Strategy: startMCPHost and registerMCPTools are exercised against real
// helper subprocesses (re-executed test binary, never a shell) to preserve
// the Phase 5 real-process coverage; pure selection/collision/mode logic
// uses piped mcp clients that never touch a process. newRuntime itself is
// not invoked (it resolves the real forcefield home); its MCP wiring is
// three lines over the helpers tested here.

// TestRuntimeMCPHelperProcess is re-executed as a fake MCP server. It is
// never run as a real test: without FF_MCP_HELPER=1 it returns at once.
//
// Wire: line-delimited JSON-RPC on stdin/stdout. Modes via environment:
//
//	FF_MCP_TOOL: remote tool name served (default "echo")
//	FF_MCP_BAD_VERSION=1: initialize answers protocolVersion "0.0.0"
//	FF_MCP_EXIT_EARLY=N: exit with code N immediately
//
// EOF on stdin exits 0 so Host.Close stays on the graceful path.
func TestRuntimeMCPHelperProcess(t *testing.T) {
	if os.Getenv("FF_MCP_HELPER") != "1" {
		return
	}
	tool := os.Getenv("FF_MCP_TOOL")
	if tool == "" {
		tool = "echo"
	}
	if n, err := strconvAtoi(os.Getenv("FF_MCP_EXIT_EARLY")); err == nil {
		os.Exit(n)
	}
	version := mcp.ClientVersion
	if os.Getenv("FF_MCP_BAD_VERSION") == "1" {
		version = "0.0.0"
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 64*1024), 1024*1024)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for in.Scan() {
		line := in.Bytes()
		if len(bytesTrimSpaceMCP(line)) == 0 {
			continue
		}
		var env struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		hasID := len(bytesTrimSpaceMCP(env.ID)) > 0 && string(bytesTrimSpaceMCP(env.ID)) != "null"
		reply := func(payload string) {
			if !hasID {
				return
			}
			fmt.Fprintf(out, "{\"jsonrpc\":\"2.0\",\"id\":%s,%s}\n", string(env.ID), payload)
			out.Flush()
		}
		switch env.Method {
		case "initialize":
			reply(fmt.Sprintf(`"result":{"protocolVersion":%s,"capabilities":{"tools":{}},"serverInfo":{"name":"rt-fake","version":"1"}}`, quoteJSONMCP(version)))
		case "notifications/initialized":
			// Notification: no reply.
		case "tools/list":
			reply(fmt.Sprintf(`"result":{"tools":[{"name":%s,"description":"fake","inputSchema":{"type":"object"}}]}`, quoteJSONMCP(tool)))
		case "tools/call":
			reply(`"result":{"content":[{"type":"text","text":"ok:` + tool + `"}]}`)
		default:
			reply(`"error":{"code":-32601,"message":"Method not found"}`)
		}
	}
}

func strconvAtoi(s string) (int, error) {
	n := 0
	neg := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(c-'0')
	}
	if s == "" {
		return 0, fmt.Errorf("not a number")
	}
	if neg {
		n = -n
	}
	return n, nil
}

func bytesTrimSpaceMCP(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func quoteJSONMCP(s string) string {
	enc, _ := json.Marshal(s)
	return string(enc)
}

// mcpServerConfig builds a ServerConfig launching the fake helper.
func mcpServerConfig(t *testing.T, extraEnv map[string]string) mcp.ServerConfig {
	t.Helper()
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("helper exe: %v", err)
	}
	env := map[string]string{"FF_MCP_HELPER": "1"}
	for k, v := range extraEnv {
		env[k] = v
	}
	return mcp.ServerConfig{
		Command:        exe,
		Args:           []string{"-test.run=^TestRuntimeMCPHelperProcess$"},
		Env:            env,
		TimeoutSeconds: 30,
	}
}

func mcpTestFullManager(t *testing.T) *tools.Manager {
	t.Helper()
	full := tools.NewManager(tools.NewRegistry())
	registerTestTools(t, full)
	return full
}

// startMCPHostForTest runs the production startup path and registers host
// cleanup. It mirrors the newRuntime wiring without touching the real
// forcefield home.
func startMCPHostForTest(t *testing.T, cfg *config.Config, full *tools.Manager) *mcp.Host {
	t.Helper()
	host, err := startMCPHost(cfg, t.TempDir(), full)
	if err != nil {
		t.Fatalf("startMCPHost: %v", err)
	}
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Errorf("host Close: %v", err)
		}
	})
	return host
}

func mcpTestConfig(servers map[string]mcp.ServerConfig) *config.Config {
	return &config.Config{
		Model: config.Model{Provider: "ollama", Name: "test-model"},
		MCP:   mcp.Config{Servers: servers},
	}
}

func lookupNames(m *tools.Manager) []string {
	var out []string
	for _, d := range m.Definitions() {
		out = append(out, d.Name)
	}
	return out
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func TestMCPStartup_NoServersUnchanged(t *testing.T) {
	full := mcpTestFullManager(t)
	before := lookupNames(full)
	if mcpHasEnabledServers(mcpTestConfig(nil)) {
		t.Fatal("empty MCP config reports enabled servers")
	}
	disabled := false
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"srv": {Command: "/nonexistent-xyz", Enabled: &disabled},
	})
	if mcpHasEnabledServers(cfg) {
		t.Fatal("disabled-only config reports enabled servers")
	}
	host, err := startMCPHost(cfg, t.TempDir(), full)
	if err != nil {
		t.Fatalf("startMCPHost with no enabled servers: %v", err)
	}
	defer func() { _ = host.Close() }()
	if got := lookupNames(full); fmt.Sprintf("%v", got) != fmt.Sprintf("%v", before) {
		t.Errorf("full manager changed with no enabled servers: %v", got)
	}
	if host == nil {
		t.Fatal("expected non-nil host")
	}
}

func TestMCPStartup_HealthyServerRegisters(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	host := startMCPHostForTest(t, cfg, full)
	tool, ok := full.Lookup("mcp__demo__echo")
	if !ok {
		t.Fatalf("mcp__demo__echo not registered; manager has %v", lookupNames(full))
	}
	if tool.Name() != "mcp__demo__echo" {
		t.Errorf("tool name = %q", tool.Name())
	}
	snap := host.Snapshot()
	if len(snap.Servers) != 1 || !snap.Servers[0].Ready {
		t.Errorf("snapshot = %+v, want one ready server", snap.Servers)
	}
}

func TestMCPStartup_DeterministicOrder(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"b": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "bee"}),
		"a": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "aye"}),
	})
	startMCPHostForTest(t, cfg, full)
	var got []string
	for _, n := range lookupNames(full) {
		if strings.HasPrefix(n, "mcp__") {
			got = append(got, n)
		}
	}
	want := []string{"mcp__a__aye", "mcp__b__bee"}
	if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
		t.Errorf("mcp registration order = %v, want %v (sorted server keys)", got, want)
	}
}

func TestMCPStartup_FailedServerWarnsAndContinues(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"bad":  mcpServerConfig(t, map[string]string{"FF_MCP_BAD_VERSION": "1"}),
		"good": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	host := startMCPHostForTest(t, cfg, full)
	if _, ok := full.Lookup("mcp__good__echo"); !ok {
		t.Error("healthy survivor not registered after sibling failure")
	}
	if _, ok := full.Lookup("mcp__bad__echo"); ok {
		t.Error("failed server tool registered")
	}
	if _, ok := full.Lookup("read_file"); !ok {
		t.Error("native tools must survive MCP failures")
	}
	var badReady, badErr bool
	for _, ss := range host.Snapshot().Servers {
		if ss.Key == "bad" {
			badReady = ss.Ready
			badErr = ss.LastError != ""
		}
	}
	if badReady || !badErr {
		t.Error("failed server snapshot must show !Ready with a diagnostic")
	}
}

func TestMCPStartup_AllFailStillStarts(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"bad1": mcpServerConfig(t, map[string]string{"FF_MCP_BAD_VERSION": "1"}),
		"bad2": mcpServerConfig(t, map[string]string{"FF_MCP_EXIT_EARLY": "3"}),
	})
	startMCPHostForTest(t, cfg, full)
	for _, n := range lookupNames(full) {
		if strings.HasPrefix(n, "mcp__") {
			t.Errorf("failed servers registered %q", n)
		}
	}
	if _, ok := full.Lookup("shell"); !ok {
		t.Error("native tools must survive total MCP failure")
	}
}

func TestMCPStartup_DisabledNeverSpawned(t *testing.T) {
	full := mcpTestFullManager(t)
	disabled := false
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"srv": {Command: "/nonexistent-xyz-123", Enabled: &disabled},
	})
	host := startMCPHostForTest(t, cfg, full)
	snap := host.Snapshot()
	if len(snap.Servers) != 1 || !snap.Servers[0].Skipped {
		t.Errorf("disabled snapshot = %+v, want skipped", snap.Servers)
	}
	if snap.Servers[0].Started {
		t.Error("disabled server was spawned")
	}
}

// newFakeMCPTool builds a registered-shape mcp.Tool without a server: a
// piped transport that is never driven, so no process is involved.
func newFakeMCPTool(t *testing.T, transports *[]io.Closer, server, remote string) *mcp.Tool {
	t.Helper()
	pr, pw := io.Pipe()
	*transports = append(*transports, pr, pw)
	tr, err := mcp.NewTransport(pr, pw)
	if err != nil {
		t.Fatalf("NewTransport: %v", err)
	}
	cl, err := mcp.NewClient(tr, server, mcp.Implementation{Name: "test"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	qualified, err := mcp.QualifiedName(server, remote)
	if err != nil {
		t.Fatalf("QualifiedName: %v", err)
	}
	tool, err := mcp.NewTool(cl, mcp.DiscoveredTool{
		Server:      server,
		Name:        remote,
		Qualified:   qualified,
		Description: "fake " + remote,
		InputSchema: map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatalf("NewTool: %v", err)
	}
	return tool
}

func TestMCPSelection_OptIn(t *testing.T) {
	var transports []io.Closer
	t.Cleanup(func() {
		for _, c := range transports {
			_ = c.Close()
		}
	})
	full := mcpTestFullManager(t)
	adapter := newFakeMCPTool(t, &transports, "demo", "echo")
	snap := mcp.HostSnapshot{Servers: []mcp.ServerSnapshot{
		{Key: "demo", Enabled: true, Ready: true, Tools: []*mcp.Tool{adapter}},
	}}
	registerMCPTools(full, snap)

	def := agent.Definition{Name: "coding", Description: "c", SystemPrompt: "s",
		Tools: []string{"read_file", "mcp__demo__echo"}, Skills: []string{}, Constraints: []string{}}
	keep, missing := resolveAgentTools(full, def.Tools)
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
	filtered, err := full.Filtered(keep)
	if err != nil {
		t.Fatalf("Filtered: %v", err)
	}
	if _, ok := filtered.Lookup("mcp__demo__echo"); !ok {
		t.Error("opted-in MCP tool missing from agent manager")
	}

	plain := agent.Definition{Name: "plain", Description: "c", SystemPrompt: "s",
		Tools: []string{"read_file"}, Skills: []string{}, Constraints: []string{}}
	keep, _ = resolveAgentTools(full, plain.Tools)
	filtered, err = full.Filtered(keep)
	if err != nil {
		t.Fatalf("Filtered: %v", err)
	}
	if _, ok := filtered.Lookup("mcp__demo__echo"); ok {
		t.Error("non-opted-in agent exposes MCP tool")
	}
}

func TestMCPSelection_MissingWarnOmit(t *testing.T) {
	full := mcpTestFullManager(t)
	keep, missing := resolveAgentTools(full, []string{"read_file", "mcp__demo__nope"})
	if len(missing) != 1 || missing[0] != "mcp__demo__nope" {
		t.Errorf("missing = %v, want [mcp__demo__nope]", missing)
	}
	filtered, err := full.Filtered(keep)
	if err != nil {
		t.Fatalf("Filtered after omit: %v (must not fail)", err)
	}
	if _, ok := filtered.Lookup("mcp__demo__nope"); ok {
		t.Error("missing MCP tool present after omit")
	}
}

func TestMCPSelection_UnknownNativeStrict(t *testing.T) {
	full := mcpTestFullManager(t)
	keep, missing := resolveAgentTools(full, []string{"read_file", "frobnicate"})
	if len(missing) != 0 {
		t.Errorf("native unknown must not be classified missing: %v", missing)
	}
	if _, err := full.Filtered(keep); err == nil {
		t.Error("unknown native tool accepted; strict behavior must be preserved")
	}
}

func TestMCPSelection_AgentSwitchWarnOmit(t *testing.T) {
	var transports []io.Closer
	t.Cleanup(func() {
		for _, c := range transports {
			_ = c.Close()
		}
	})
	full := mcpTestFullManager(t)
	adapter := newFakeMCPTool(t, &transports, "demo", "echo")
	registerMCPTools(full, mcp.HostSnapshot{Servers: []mcp.ServerSnapshot{
		{Key: "demo", Enabled: true, Ready: true, Tools: []*mcp.Tool{adapter}},
	}})

	registry := agent.DefaultRegistry()
	override := map[string]config.AgentConfig{
		"coding": {Tools: []string{"read_file", "mcp__demo__echo", "mcp__demo__gone"}},
	}
	if err := applyAgentOverrides(registry, override); err != nil {
		t.Fatalf("applyAgentOverrides: %v", err)
	}
	rt := &Runtime{
		cfg:         mcpTestConfig(nil),
		fullManager: full,
		manager:     full,
		agents:      registry,
		activeAgent: "general",
		scheduler:   newScheduler(full, nil, nil, DefaultSchedulerConfig),
	}
	if err := rt.SetAgent("coding"); err != nil {
		t.Fatalf("SetAgent with one missing MCP tool: %v (must warn+omit, not fail)", err)
	}
	if _, ok := rt.manager.Lookup("mcp__demo__echo"); !ok {
		t.Error("opted-in MCP tool missing after switch")
	}
	if _, ok := rt.manager.Lookup("mcp__demo__gone"); ok {
		t.Error("missing MCP tool present after switch")
	}
	warns := rt.MCPWarnings()
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "mcp__demo__gone") || !strings.Contains(joined, "coding") {
		t.Errorf("MCPWarnings = %v, want agent+tool attribution", warns)
	}
}

func TestMCPRegister_NativeWins(t *testing.T) {
	var transports []io.Closer
	t.Cleanup(func() {
		for _, c := range transports {
			_ = c.Close()
		}
	})
	full := tools.NewManager(tools.NewRegistry())
	native := &testAgentTool{name: "mcp__x__y"}
	if err := full.Register(native); err != nil {
		t.Fatalf("register native: %v", err)
	}
	adapter := newFakeMCPTool(t, &transports, "x", "y")
	if adapter.Name() != "mcp__x__y" {
		t.Fatalf("adapter name = %q", adapter.Name())
	}
	registerMCPTools(full, mcp.HostSnapshot{Servers: []mcp.ServerSnapshot{
		{Key: "x", Enabled: true, Ready: true, Tools: []*mcp.Tool{adapter}},
	}})
	got, ok := full.Lookup("mcp__x__y")
	if !ok {
		t.Fatal("name vanished")
	}
	if got != tools.Tool(native) {
		t.Error("native tool lost collision against MCP name")
	}
}

func TestMCPRegister_DuplicateDropped(t *testing.T) {
	// "__" ambiguity: server "a" tool "b__c" and server "a__b" tool "c"
	// qualify identically. Both instances must drop, order-independently.
	q1, err := mcp.QualifiedName("a", "b__c")
	if err != nil {
		t.Fatalf("QualifiedName: %v", err)
	}
	q2, err := mcp.QualifiedName("a__b", "c")
	if err != nil {
		t.Fatalf("QualifiedName: %v", err)
	}
	if q1 != q2 {
		t.Skipf("no shared qualified name (%q vs %q); invariant holds trivially", q1, q2)
	}
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"a":    mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "b__c"}),
		"a__b": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "c"}),
	})
	host := startMCPHostForTest(t, cfg, full)
	if _, ok := full.Lookup(q1); ok {
		t.Errorf("colliding %q registered; all instances must drop", q1)
	}
	found := false
	for _, d := range host.Snapshot().Duplicates {
		if d == q1 {
			found = true
		}
	}
	if !found {
		t.Errorf("Duplicates = %v, want %q", host.Snapshot().Duplicates, q1)
	}
}

func TestMCPPlanModeExcludes(t *testing.T) {
	var transports []io.Closer
	t.Cleanup(func() {
		for _, c := range transports {
			_ = c.Close()
		}
	})
	full := mcpTestFullManager(t)
	adapter := newFakeMCPTool(t, &transports, "demo", "echo")
	registerMCPTools(full, mcp.HostSnapshot{Servers: []mcp.ServerSnapshot{
		{Key: "demo", Enabled: true, Ready: true, Tools: []*mcp.Tool{adapter}},
	}})
	plan, err := planManager(full)
	if err != nil {
		t.Fatalf("planManager: %v", err)
	}
	if _, ok := plan.Lookup("mcp__demo__echo"); ok {
		t.Error("mcp__* tool present in plan/read-only mode")
	}
	chat, err := full.Filtered([]string{"read_file", "mcp__demo__echo"})
	if err != nil {
		t.Fatalf("Filtered: %v", err)
	}
	if _, ok := chat.Lookup("mcp__demo__echo"); !ok {
		t.Error("opted-in MCP tool missing from normal mode")
	}
}

func TestMCPPermissionAskDefault(t *testing.T) {
	perms := newTestPermManager(t, permissions.Ask, nil)
	if got := perms.Check("mcp__demo__echo"); got != permissions.Ask {
		t.Errorf("Check(mcp tool) = %v, want Ask default", got)
	}
}

func TestMCPHeadlessDenied(t *testing.T) {
	var transports []io.Closer
	t.Cleanup(func() {
		for _, c := range transports {
			_ = c.Close()
		}
	})
	adapter := newFakeMCPTool(t, &transports, "demo", "echo")
	manager := newTestManager(t, adapter)
	perms := newTestPermManager(t, permissions.Ask, nil)
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

func TestMCPNoBoundaryChecker(t *testing.T) {
	var transports []io.Closer
	t.Cleanup(func() {
		for _, c := range transports {
			_ = c.Close()
		}
	})
	adapter := newFakeMCPTool(t, &transports, "demo", "echo")
	if _, ok := any(adapter).(tools.BoundaryChecker); ok {
		t.Error("MCP adapter must not claim BoundaryChecker over opaque args")
	}
}

func TestMCPCloseShutsHost(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	host, err := startMCPHost(cfg, t.TempDir(), full)
	if err != nil {
		t.Fatalf("startMCPHost: %v", err)
	}
	rt := &Runtime{
		cfg:         cfg,
		fullManager: full,
		manager:     full,
		mcpHost:     host,
		agents:      agent.DefaultRegistry(),
		activeAgent: "general",
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("second Close: %v (must be safe)", err)
	}
	for _, ss := range host.Snapshot().Servers {
		if ss.Ready {
			t.Errorf("server %q still ready after Close", ss.Key)
		}
	}
	// Frozen universe stays registered; execution fails inside the tool.
	if _, ok := full.Lookup("mcp__demo__echo"); !ok {
		t.Error("tool vanished from manager on Close; freeze the universe")
	}
}

func TestMCPCloseNilHost(t *testing.T) {
	if err := (&Runtime{}).Close(); err != nil {
		t.Errorf("Close without host: %v", err)
	}
	var nilRT *Runtime
	if err := nilRT.Close(); err != nil {
		t.Errorf("Close on nil runtime: %v", err)
	}
}

func TestMCPNoPerTurnClose(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"demo": mcpServerConfig(t, map[string]string{"FF_MCP_TOOL": "echo"}),
	})
	host := startMCPHostForTest(t, cfg, full)
	rt := &Runtime{
		cfg:         cfg,
		fullManager: full,
		manager:     full,
		mcpHost:     host,
		agents:      agent.DefaultRegistry(),
		activeAgent: "general",
		scheduler:   newScheduler(full, nil, nil, DefaultSchedulerConfig),
	}
	rt.agent = rt.buildAgent(rt.agents.Default())
	if err := rt.SetAgent("coding"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	if err := rt.SetAgent("general"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	for _, ss := range host.Snapshot().Servers {
		if !ss.Ready {
			t.Errorf("server %q lost readiness across agent switches; no per-turn teardown allowed", ss.Key)
		}
	}
	_ = rt.Close()
}

func TestMCPWarnings(t *testing.T) {
	full := mcpTestFullManager(t)
	cfg := mcpTestConfig(map[string]mcp.ServerConfig{
		"bad": mcpServerConfig(t, map[string]string{"FF_MCP_BAD_VERSION": "1"}),
	})
	host := startMCPHostForTest(t, cfg, full)

	registry := agent.DefaultRegistry()
	override := map[string]config.AgentConfig{
		"coding": {Tools: []string{"read_file", "mcp__demo__gone"}},
	}
	if err := applyAgentOverrides(registry, override); err != nil {
		t.Fatalf("applyAgentOverrides: %v", err)
	}
	rt := &Runtime{
		cfg:         cfg,
		fullManager: full,
		manager:     full,
		mcpHost:     host,
		agents:      registry,
		activeAgent: "general",
	}
	warns := rt.MCPWarnings()
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, `"bad"`) {
		t.Errorf("warnings lack failed server: %v", warns)
	}
	if !strings.Contains(joined, "mcp__demo__gone") || !strings.Contains(joined, "coding") {
		t.Errorf("warnings lack missing tool attribution: %v", warns)
	}
}

func TestMCPWarningsEmpty(t *testing.T) {
	rt := &Runtime{
		cfg:         mcpTestConfig(nil),
		fullManager: mcpTestFullManager(t),
		agents:      agent.DefaultRegistry(),
		activeAgent: "general",
	}
	if warns := rt.MCPWarnings(); len(warns) != 0 {
		t.Errorf("warnings = %v, want empty without MCP", warns)
	}
}
