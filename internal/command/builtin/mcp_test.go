package builtin

import (
	"fmt"
	"strings"
	"testing"

	"forcefield/internal/command"
)

func mcpTestServers() []command.MCPServerInfo {
	return []command.MCPServerInfo{
		{
			Name: "github", Enabled: true, Command: "npx",
			Args:  []string{"-y", "@modelcontextprotocol/server-github"},
			State: "ready", Tools: []string{"mcp__github__search"},
		},
		{
			Name: "filesystem", Enabled: true, Command: "/usr/local/bin/fs",
			State: "ready",
			Tools: []string{"mcp__filesystem__read", "mcp__filesystem__write"},
		},
		{Name: "postgres", Enabled: false, Command: "/usr/local/bin/pg", State: "disabled"},
		{Name: "broken", Enabled: true, Command: "/usr/local/bin/nope", State: "failed", Error: "exit code 1"},
	}
}

func mcpJoinedLines(ctx *fakeContext) string {
	return strings.Join(ctx.lines, "\n")
}

func TestMCP_BareShowsTable(t *testing.T) {
	ctx := &fakeContext{mcpServers: mcpTestServers()}
	if err := NewMCP().Execute(ctx, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := mcpJoinedLines(ctx)
	for _, want := range []string{"MCP SERVERS", "github", "connected", "1 tools", "filesystem", "2 tools", "postgres", "disabled", "broken", "failed", "/mcp list"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestMCP_ListMatchesBareTable(t *testing.T) {
	bare := &fakeContext{mcpServers: mcpTestServers()}
	listed := &fakeContext{mcpServers: mcpTestServers()}
	if err := NewMCP().Execute(bare, nil); err != nil {
		t.Fatalf("bare: %v", err)
	}
	if err := NewMCP().Execute(listed, []string{"list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	// list omits the subcommand hint; the table itself is identical.
	bareTable := strings.Join(bare.lines[:len(bare.lines)-1], "\n")
	if mcpJoinedLines(listed) != bareTable {
		t.Errorf("list table differs from bare table:\nbare:\n%s\nlist:\n%s", bareTable, mcpJoinedLines(listed))
	}
}

func TestMCP_EmptyShowsSetupGuidance(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewMCP().Execute(ctx, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := mcpJoinedLines(ctx)
	for _, want := range []string{"No MCP servers configured", "/mcp add <name> <command>"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if err := NewMCP().Execute(ctx, []string{"list"}); err != nil {
		t.Fatalf("list: %v", err)
	}
}

func TestMCP_UnknownLastKnownMarked(t *testing.T) {
	ctx := &fakeContext{mcpServers: []command.MCPServerInfo{
		{Name: "old", Enabled: true, Command: "/bin/old", State: "unknown", Tools: []string{"mcp__old__a", "mcp__old__b"}},
		{Name: "quiet", Enabled: true, Command: "/bin/q", State: "unknown"},
	}}
	if err := NewMCP().Execute(ctx, []string{"list"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := mcpJoinedLines(ctx)
	if !strings.Contains(out, "2 tools (last known)") {
		t.Errorf("last-known tools not marked:\n%s", out)
	}
	quiet := ""
	for _, line := range ctx.lines {
		if strings.Contains(line, "quiet") {
			quiet = line
		}
	}
	if strings.Contains(quiet, "last known") {
		t.Errorf("tool-less unknown server claims last-known tools: %q", quiet)
	}
}

func TestMCP_AddStoresArgs(t *testing.T) {
	ctx := &fakeContext{}
	args := []string{"github", "npx", "-y", "@modelcontextprotocol/server-github"}
	if err := NewMCP().Execute(ctx, append([]string{"add"}, args...)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(ctx.mcpAdded) != 1 {
		t.Fatalf("added = %v, want one call", ctx.mcpAdded)
	}
	got := ctx.mcpAdded[0]
	if got.name != "github" || got.command != "npx" {
		t.Errorf("added = %+v", got)
	}
	for i, want := range []string{"-y", "@modelcontextprotocol/server-github"} {
		if i >= len(got.args) || got.args[i] != want {
			t.Errorf("args = %v, want %v", got.args, args[2:])
			break
		}
	}
	if !strings.Contains(mcpJoinedLines(ctx), "Added MCP server 'github'.") {
		t.Errorf("missing confirmation:\n%s", mcpJoinedLines(ctx))
	}
}

func TestMCP_AddGuide(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewMCP().Execute(ctx, []string{"add"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := mcpJoinedLines(ctx)
	for _, want := range []string{"/mcp add <name> <command>", "timeout_seconds", "/mcp test <name>"} {
		if !strings.Contains(out, want) {
			t.Errorf("guide missing %q:\n%s", want, out)
		}
	}
	if len(ctx.mcpAdded) != 0 {
		t.Error("guide run stored a server")
	}
}

func TestMCP_AddMalformed(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewMCP().Execute(ctx, []string{"add", "onlyname"}); err == nil {
		t.Error("single-arg add accepted")
	}
}

func TestMCP_AddPropagatesError(t *testing.T) {
	ctx := &fakeContext{mcpAddErr: fmt.Errorf("nope")}
	if err := NewMCP().Execute(ctx, []string{"add", "x", "y"}); err == nil {
		t.Error("backend error swallowed")
	}
}

func TestMCP_GetShowsDetails(t *testing.T) {
	ctx := &fakeContext{mcpServers: []command.MCPServerInfo{{
		Name: "demo", Enabled: true, Command: "/usr/local/bin/demo",
		Args: []string{"--stdio"}, Cwd: "/srv", TimeoutSeconds: 45,
		State: "ready", Tools: []string{"mcp__demo__b", "mcp__demo__a"},
	}}}
	if err := NewMCP().Execute(ctx, []string{"get", "demo"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := mcpJoinedLines(ctx)
	for _, want := range []string{
		"MCP server 'demo':", "Enabled: yes", "Command: /usr/local/bin/demo",
		"Args: --stdio", "Working directory: /srv", "Timeout: 45s",
		"Status: connected", "Tools (2): mcp__demo__a, mcp__demo__b",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestMCP_GetHidesEnvAndRedactsArgs(t *testing.T) {
	ctx := &fakeContext{mcpServers: []command.MCPServerInfo{{
		Name: "demo", Enabled: true, Command: "/bin/demo",
		Args:           []string{"--token", "sk-test-secret-value-1234567890"},
		State:          "failed",
		Error:          "exit code 1",
		TimeoutSeconds: 0,
	}}}
	if err := NewMCP().Execute(ctx, []string{"get", "demo"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := mcpJoinedLines(ctx)
	if strings.Contains(out, "sk-test-secret-value-1234567890") {
		t.Errorf("secret arg displayed:\n%s", out)
	}
	if strings.Contains(out, "Error: exit code 1") == false {
		t.Errorf("failure reason missing:\n%s", out)
	}
	if !strings.Contains(out, "Timeout: 30s") {
		t.Errorf("zero timeout must show the 30s default:\n%s", out)
	}
}

func TestMCP_GetUnknown(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewMCP().Execute(ctx, []string{"get", "nope"}); err == nil {
		t.Error("unknown server accepted")
	}
	if err := NewMCP().Execute(ctx, []string{"get"}); err == nil {
		t.Error("missing name accepted")
	}
}

func TestMCP_Remove(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewMCP().Execute(ctx, []string{"remove", "demo"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(ctx.mcpRemoved) != 1 || ctx.mcpRemoved[0] != "demo" {
		t.Errorf("removed = %v", ctx.mcpRemoved)
	}
	if !strings.Contains(mcpJoinedLines(ctx), "Removed MCP server 'demo'.") {
		t.Errorf("missing confirmation:\n%s", mcpJoinedLines(ctx))
	}
	ctx2 := &fakeContext{mcpRemoveErr: fmt.Errorf("nope")}
	if err := NewMCP().Execute(ctx2, []string{"remove", "demo"}); err == nil {
		t.Error("backend error swallowed")
	}
	if err := NewMCP().Execute(ctx, []string{"remove"}); err == nil {
		t.Error("missing name accepted")
	}
}

func TestMCP_EnableDisable(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewMCP().Execute(ctx, []string{"disable", "demo"}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := NewMCP().Execute(ctx, []string{"enable", "demo"}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(ctx.mcpEnables) != 2 ||
		ctx.mcpEnables[0] != (mcpEnableCall{"demo", false}) ||
		ctx.mcpEnables[1] != (mcpEnableCall{"demo", true}) {
		t.Errorf("enables = %+v", ctx.mcpEnables)
	}
	out := mcpJoinedLines(ctx)
	if !strings.Contains(out, "'demo' disabled.") || !strings.Contains(out, "'demo' enabled.") {
		t.Errorf("confirmations missing:\n%s", out)
	}
	ctx2 := &fakeContext{mcpEnableErr: fmt.Errorf("nope")}
	if err := NewMCP().Execute(ctx2, []string{"enable", "demo"}); err == nil {
		t.Error("backend error swallowed")
	}
	if err := NewMCP().Execute(ctx, []string{"enable"}); err == nil {
		t.Error("missing name accepted")
	}
}

func TestMCP_TestSuccess(t *testing.T) {
	ctx := &fakeContext{mcpTest: command.MCPTestResult{Tools: []string{"mcp__demo__b", "mcp__demo__a"}}}
	if err := NewMCP().Execute(ctx, []string{"test", "demo"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(ctx.mcpTested) != 1 || ctx.mcpTested[0] != "demo" {
		t.Errorf("tested = %v", ctx.mcpTested)
	}
	out := mcpJoinedLines(ctx)
	for _, want := range []string{
		"MCP server 'demo' discovered 2 tool(s).", "mcp__demo__a", "mcp__demo__b",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Sorted display, not discovery order.
	a, b := strings.Index(out, "mcp__demo__a"), strings.Index(out, "mcp__demo__b")
	if a < 0 || b < 0 || a > b {
		t.Errorf("tools not sorted:\n%s", out)
	}
}

func TestMCP_TestFailure(t *testing.T) {
	ctx := &fakeContext{mcpTestErr: fmt.Errorf("exit code 1")}
	if err := NewMCP().Execute(ctx, []string{"test", "demo"}); err == nil {
		t.Error("failing server test accepted")
	}
	if len(ctx.mcpTested) != 1 {
		t.Errorf("tested = %v, want the attempt recorded", ctx.mcpTested)
	}
	if err := NewMCP().Execute(ctx, []string{"test"}); err == nil {
		t.Error("missing name accepted")
	}
}

func TestMCP_UnknownSubcommand(t *testing.T) {
	ctx := &fakeContext{}
	err := NewMCP().Execute(ctx, []string{"frobnicate"})
	if err == nil {
		t.Fatal("unknown subcommand accepted")
	}
	for _, want := range []string{"list", "test", "remove"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q omits %q", err, want)
		}
	}
}

func TestMCP_ListRejectsExtraArgs(t *testing.T) {
	ctx := &fakeContext{}
	if err := NewMCP().Execute(ctx, []string{"list", "extra"}); err == nil {
		t.Error("list with args accepted")
	}
}

func TestMCP_Metadata(t *testing.T) {
	cmd := NewMCP()
	if cmd.Name() != "mcp" {
		t.Errorf("Name() = %q", cmd.Name())
	}
	if len(cmd.Aliases()) != 0 {
		t.Errorf("Aliases() = %v, want none", cmd.Aliases())
	}
	if !strings.Contains(cmd.Usage(), "/mcp") {
		t.Errorf("Usage() = %q", cmd.Usage())
	}
}
