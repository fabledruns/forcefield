package runtime

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

func TestSensitiveFileRequiresApprovalEvenWhenAllowed(t *testing.T) {
	tool := &fixedResultTool{name: "read_file"}
	manager := newTestManager(t, tool)
	// read_file globally allowed
	perms := newTestPermManager(t, permissions.Ask, map[string]permissions.Decision{"read_file": permissions.Allow})
	asked := false
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asked = true
		return permissions.PromptAllowOnce, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	// Non-sensitive path should not ask (allowed directly)
	asked = false
	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "read_file", Arguments: map[string]any{"path": "normal.txt"}}}, func(Event) bool { return true })
	if results[0].IsError {
		t.Fatalf("normal file should be allowed without ask, got %#v", results[0])
	}
	if asked {
		t.Error("asker should not be called for normal file when allowed")
	}

	// Sensitive .env should force Ask even though Allow
	asked = false
	results = s.Run(context.Background(), []providers.ToolCall{{ID: "2", Name: "read_file", Arguments: map[string]any{"path": ".env"}}}, func(Event) bool { return true })
	if results[0].IsError {
		t.Fatalf("sensitive .env with AllowOnce should execute, got %#v", results[0])
	}
	if !asked {
		t.Error("sensitive .env should have forced an Ask prompt even when globally allowed")
	}

	// Also test .env.local, id_rsa, .pem
	for _, sensitive := range []string{".env.local", "my.pem", "key.key", ".ssh/id_rsa", ".aws/credentials"} {
		asked = false
		results = s.Run(context.Background(), []providers.ToolCall{{ID: "3", Name: "read_file", Arguments: map[string]any{"path": sensitive}}}, func(Event) bool { return true })
		if !asked {
			t.Errorf("path %q should be considered sensitive and require Ask", sensitive)
		}
		if results[0].IsError {
			t.Errorf("path %q with AllowOnce should succeed", sensitive)
		}
	}

	// Sensitive with DenyOnce should be denied
	asker2 := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		return permissions.PromptDenyOnce, nil
	})
	s2 := newScheduler(manager, perms, asker2, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	results = s2.Run(context.Background(), []providers.ToolCall{{ID: "4", Name: "read_file", Arguments: map[string]any{"path": ".env"}}}, func(Event) bool { return true })
	if !results[0].IsError {
		t.Error("sensitive .env denied should be error")
	}
}

func TestIsSensitiveCall_CoversSearchCode(t *testing.T) {
	if !isSensitiveCall(providers.ToolCall{Name: "search_code", Arguments: map[string]any{"path": ".env"}}, "") {
		t.Errorf("search_code on .env must escalate to Ask")
	}
	if isSensitiveCall(providers.ToolCall{Name: "search_code", Arguments: map[string]any{"path": "normal.go"}}, "") {
		t.Errorf("search_code on a normal path must not escalate")
	}
}

func TestIsSensitiveCall_CoversResolvedPath(t *testing.T) {
	// A suspicious name must escalate even when only the canonical
	// pre-flight path carries it (raw spelling hides it).
	call := providers.ToolCall{Name: "read_file", Arguments: map[string]any{"path": "notes.txt"}}
	if !isSensitiveCall(call, "/home/user/.ssh/id_rsa") {
		t.Errorf("resolved sensitive path must escalate to Ask")
	}
	if isSensitiveCall(call, "/home/user/notes.txt") {
		t.Errorf("resolved normal path must not escalate")
	}
}

// TestIsSensitiveCall_ShellCommand pins the P1 shell-escalation fix at
// the unit level: a foreground shell command naming a sensitive file
// escalates exactly like a read_file path, while ordinary commands do
// not. Quoted, chained, and variable-prefixed spellings are covered
// because attackers choose the spelling, not the defender.
func TestIsSensitiveCall_ShellCommand(t *testing.T) {
	for _, cmd := range []string{
		"cat ~/.ssh/id_rsa",
		`cat "~/.ssh/id_rsa"`,
		"cat $HOME/.ssh/id_rsa",
		"cat .env && echo done",
		"cat .env;echo done",
		"$(cat ~/.ssh/id_rsa)",
		"cp id_rsa /tmp/x",
		"ls C:\\Users\\me\\.aws\\credentials",
	} {
		call := providers.ToolCall{Name: "shell", Arguments: map[string]any{"command": cmd}}
		if !isSensitiveCall(call, "") {
			t.Errorf("shell command %q must escalate to Ask", cmd)
		}
	}
	for _, cmd := range []string{
		"ls -la",
		"echo hello world",
		`cat "my notes.txt"`,
		"grep -r password src/",
		"go test ./...",
	} {
		call := providers.ToolCall{Name: "shell", Arguments: map[string]any{"command": cmd}}
		if isSensitiveCall(call, "") {
			t.Errorf("ordinary shell command %q must not escalate", cmd)
		}
	}
	// Sensitive working directory escalates even with a benign command.
	cwdCall := providers.ToolCall{Name: "shell", Arguments: map[string]any{"command": "ls", "cwd": "/home/user/.aws"}}
	if !isSensitiveCall(cwdCall, "") {
		t.Errorf("shell with sensitive cwd must escalate to Ask")
	}
	// shell_job commands get the same treatment (previously cwd-only).
	jobCall := providers.ToolCall{Name: "shell_job", Arguments: map[string]any{"command": "cat .env", "cwd": "/tmp"}}
	if !isSensitiveCall(jobCall, "") {
		t.Errorf("shell_job command naming .env must escalate to Ask")
	}
}

// TestIsSensitiveCall_MCPArguments pins the P1 MCP-escalation fix:
// path-like strings anywhere in MCP arguments (top level or nested)
// escalate through the same shared definition, while ordinary payloads
// do not. Non-string values are ignored, not treated as paths.
func TestIsSensitiveCall_MCPArguments(t *testing.T) {
	sensitive := []map[string]any{
		{"path": "~/.ssh/id_rsa"},
		{"path": ".env"},
		{"outer": map[string]any{"inner": map[string]any{"path": ".env"}}},
		{"files": []any{"notes.txt", "id_rsa"}},
		{"text": "see attached", "target": "/home/user/.aws/credentials"},
	}
	for _, args := range sensitive {
		call := providers.ToolCall{Name: "mcp__smoke__read", Arguments: args}
		if !isSensitiveCall(call, "") {
			t.Errorf("MCP args %#v must escalate to Ask", args)
		}
	}
	benign := []map[string]any{
		{"text": "hello world"},
		{"a": float64(20), "b": float64(22)},
		{"path": "notes.txt"},
		{"outer": map[string]any{"inner": []any{float64(1), true, nil}}},
		{},
	}
	for _, args := range benign {
		call := providers.ToolCall{Name: "mcp__smoke__read", Arguments: args}
		if isSensitiveCall(call, "") {
			t.Errorf("benign MCP args %#v must not escalate", args)
		}
	}
}

// TestScheduler_SearchCodeSensitivePathStillAsks proves the shipped
// allow default does not weaken sensitive-path escalation: search_code
// on a credential-looking path must prompt even with no per-tool entry.
func TestScheduler_SearchCodeSensitivePathStillAsks(t *testing.T) {
	tool := &fixedResultTool{name: "search_code"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil) // legacy shape: no search_code entry
	asked := false
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asked = true
		return permissions.PromptDenyOnce, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})

	results := s.Run(context.Background(), []providers.ToolCall{{
		ID: "1", Name: "search_code",
		Arguments: map[string]any{"pattern": "x", "path": ".env"},
	}}, func(Event) bool { return true })
	if !asked {
		t.Error("search_code on .env must force an Ask prompt even with no per-tool entry")
	}
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("expected denied sensitive search to error, got %#v", results)
	}
}

// allowScheduler builds a scheduler where every named tool is globally
// allowed, tracking whether the asker ran. Sensitive escalation is the
// only thing that may still prompt.
func allowScheduler(t *testing.T, names []string, asker permissions.AskerFunc, asked *bool) (*scheduler, *tools.Manager) {
	t.Helper()
	var tl []tools.Tool
	for _, name := range names {
		tl = append(tl, &fixedResultTool{name: name})
	}
	manager := newTestManager(t, tl...)
	overrides := make(map[string]permissions.Decision, len(names))
	for _, name := range names {
		overrides[name] = permissions.Allow
	}
	perms := newTestPermManager(t, permissions.Ask, overrides)
	wrapped := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		*asked = true
		return asker(ctx, req)
	})
	s := newScheduler(manager, perms, wrapped, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	return s, manager
}

// TestScheduler_ShellSensitiveCommandEscalates proves the P1 fix
// end to end at the scheduler boundary: an allow-ruled shell runs
// ordinary commands without prompting, but a command naming a
// sensitive file forces Ask (allowed once here so the test observes
// execution, denied variants assert the denial path).
func TestScheduler_ShellSensitiveCommandEscalates(t *testing.T) {
	allow := permissions.AskerFunc(func(context.Context, permissions.Request) (permissions.Prompt, error) {
		return permissions.PromptAllowOnce, nil
	})
	var asked bool
	s, _ := allowScheduler(t, []string{"shell"}, allow, &asked)
	run := func(cmd string) ToolResult {
		asked = false
		results := s.Run(context.Background(), []providers.ToolCall{{
			ID: "1", Name: "shell", Arguments: map[string]any{"command": cmd},
		}}, func(Event) bool { return true })
		if len(results) != 1 {
			t.Fatalf("shell %q: got %d results, want 1", cmd, len(results))
		}
		return results[0]
	}

	// 1. Normal shell operation remains allowed without prompting.
	if res := run("ls -la /tmp"); res.IsError {
		t.Fatalf("ordinary shell must execute, got %#v", res)
	} else if asked {
		t.Error("ordinary shell must not prompt when allowed")
	}

	// 2. Sensitive targets escalate: prompt fires, AllowOnce executes.
	for _, cmd := range []string{"cat ~/.ssh/id_rsa", "cat .env", `type "C:\Users\me\.aws\credentials"`} {
		if res := run(cmd); res.IsError {
			t.Fatalf("shell %q with AllowOnce must execute, got %#v", cmd, res)
		} else if !asked {
			t.Errorf("shell %q must have forced an Ask prompt", cmd)
		}
	}

	// DenyOnce on a sensitive command denies instead of executing.
	deny := permissions.AskerFunc(func(context.Context, permissions.Request) (permissions.Prompt, error) {
		return permissions.PromptDenyOnce, nil
	})
	var asked2 bool
	s2, _ := allowScheduler(t, []string{"shell"}, deny, &asked2)
	asked2 = false
	results := s2.Run(context.Background(), []providers.ToolCall{{
		ID: "1", Name: "shell", Arguments: map[string]any{"command": "cat ~/.ssh/id_rsa"},
	}}, func(Event) bool { return true })
	if !asked2 {
		t.Error("sensitive shell must prompt even to deny")
	}
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("denied sensitive shell must error, got %#v", results)
	}
}

// TestScheduler_MCPSensitiveArgsEscalate proves the P1 MCP fix at the
// scheduler boundary: allowed MCP tools run ordinary payloads cleanly,
// but any sensitive path in the arguments (top-level or nested) forces
// Ask. Session-allow from a prior Always still applies to benign calls.
func TestScheduler_MCPSensitiveArgsEscalate(t *testing.T) {
	allow := permissions.AskerFunc(func(context.Context, permissions.Request) (permissions.Prompt, error) {
		return permissions.PromptAllowOnce, nil
	})
	var asked bool
	s, _ := allowScheduler(t, []string{"mcp__smoke__read"}, allow, &asked)
	run := func(args map[string]any) (ToolResult, bool) {
		asked = false
		results := s.Run(context.Background(), []providers.ToolCall{{
			ID: "1", Name: "mcp__smoke__read", Arguments: args,
		}}, func(Event) bool { return true })
		if len(results) != 1 {
			t.Fatalf("MCP call %#v: got %d results, want 1", args, len(results))
		}
		return results[0], asked
	}

	// 3. Normal MCP tool call remains allowed without prompting.
	if res, wasAsked := run(map[string]any{"path": "notes.txt"}); res.IsError {
		t.Fatalf("ordinary MCP call must execute, got %#v", res)
	} else if wasAsked {
		t.Error("ordinary MCP call must not prompt when allowed")
	}

	// 4-5. Sensitive paths (top-level and nested) force Ask.
	for _, args := range []map[string]any{
		{"path": "~/.ssh/id_rsa"},
		{"config": map[string]any{"include": map[string]any{"file": ".env"}}},
		{"files": []any{"a.txt", ".aws/credentials"}},
	} {
		if res, wasAsked := run(args); res.IsError {
			t.Fatalf("MCP call %#v with AllowOnce must execute, got %#v", args, res)
		} else if !wasAsked {
			t.Errorf("MCP call %#v must have forced an Ask prompt", args)
		}
	}

	// 6. Non-sensitive paths never escalate.
	if res, wasAsked := run(map[string]any{"path": "docs/guide.md", "limit": float64(10)}); res.IsError {
		t.Fatalf("non-sensitive MCP call must execute, got %#v", res)
	} else if wasAsked {
		t.Error("non-sensitive MCP call must not prompt when allowed")
	}

	// 7. Session Always-allow still applies to benign calls: with a
	// base Ask decision, answering Always once lets the identical call
	// through silently afterwards, while a sensitive call still
	// escalates past the recorded Always.
	always := permissions.AskerFunc(func(context.Context, permissions.Request) (permissions.Prompt, error) {
		return permissions.PromptAlwaysAllow, nil
	})
	mcpTool := &fixedResultTool{name: "mcp__smoke__read"}
	mcpManager := newTestManager(t, mcpTool)
	askPerms := newTestPermManager(t, permissions.Ask, nil)
	var asked3 bool
	alwaysAsker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		asked3 = true
		return always(ctx, req)
	})
	s3 := newScheduler(mcpManager, askPerms, alwaysAsker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	benign := map[string]any{"path": "notes.txt"}
	if _, wasAsked := runOn(s3, benign, &asked3); !wasAsked {
		t.Fatal("first benign call must prompt to record Always")
	}
	if res, wasAsked := runOn(s3, benign, &asked3); res.IsError || wasAsked {
		t.Errorf("session-allowed benign call must run silently, got %#v asked=%v", res, wasAsked)
	}
	if _, wasAsked := runOn(s3, map[string]any{"path": ".env"}, &asked3); !wasAsked {
		t.Error("sensitive call must escalate even with a session Always on record")
	}
}

// runOn runs one MCP call against an explicit scheduler, reporting the
// result and whether the asker fired.
func runOn(s *scheduler, args map[string]any, asked *bool) (ToolResult, bool) {
	*asked = false
	results := s.Run(context.Background(), []providers.ToolCall{{
		ID: "1", Name: "mcp__smoke__read", Arguments: args,
	}}, func(Event) bool { return true })
	if len(results) != 1 {
		panic("test scheduler returned wrong result count")
	}
	return results[0], *asked
}

// sensitiveBenchCalls are representative inputs for the escalation
// check: ordinary operations, non-sensitive paths, and sensitive
// targets across both newly covered call shapes. The benchmark
// measures isSensitiveCall itself - the exact production function the
// P1 fix changed - with fixed inputs, so results are deterministic and
// machine-relative (compare sub-benchmarks against each other, not
// against absolute numbers from other machines).
var sensitiveBenchCalls = []struct {
	name string
	call providers.ToolCall
}{
	{"shell/ordinary", providers.ToolCall{Name: "shell", Arguments: map[string]any{"command": "ls -la /tmp"}}},
	{"shell/nonsensitive-path", providers.ToolCall{Name: "shell", Arguments: map[string]any{"command": "cat notes.txt"}}},
	{"shell/sensitive-path", providers.ToolCall{Name: "shell", Arguments: map[string]any{"command": "cat ~/.ssh/id_rsa"}}},
	{"mcp/ordinary", providers.ToolCall{Name: "mcp__smoke__echo", Arguments: map[string]any{"text": "hello world"}}},
	{"mcp/nonsensitive-path", providers.ToolCall{Name: "mcp__smoke__read", Arguments: map[string]any{"path": "docs/guide.md"}}},
	{"mcp/sensitive-path", providers.ToolCall{Name: "mcp__smoke__read", Arguments: map[string]any{"path": "~/.ssh/id_rsa"}}},
	{"mcp/nested-sensitive", providers.ToolCall{Name: "mcp__smoke__read", Arguments: map[string]any{
		"config": map[string]any{"include": map[string]any{"files": []any{"a.txt", ".env"}}},
	}}},
}

func BenchmarkIsSensitiveCall(b *testing.B) {
	for _, tc := range sensitiveBenchCalls {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			sink := false
			for i := 0; i < b.N; i++ {
				sink = isSensitiveCall(tc.call, "")
			}
			_ = sink
		})
	}
}

func TestConcurrentAskRequestsDoNotDeadlock(t *testing.T) {
	tool := &fixedResultTool{name: "shell"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	var asked atomic.Int64
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		// Simulate user taking 50ms to answer
		time.Sleep(50 * time.Millisecond)
		asked.Add(1)
		return permissions.PromptAllowOnce, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 4, MaxRetries: 0, BaseBackoff: time.Millisecond})

	calls := []providers.ToolCall{
		{ID: "1", Name: "shell", Arguments: map[string]any{"command": "echo 1"}},
		{ID: "2", Name: "shell", Arguments: map[string]any{"command": "echo 2"}},
		{ID: "3", Name: "shell", Arguments: map[string]any{"command": "echo 3"}},
		{ID: "4", Name: "shell", Arguments: map[string]any{"command": "echo 4"}},
	}

	done := make(chan []ToolResult, 1)
	go func() {
		done <- s.Run(context.Background(), calls, func(Event) bool { return true })
	}()

	select {
	case results := <-done:
		if len(results) != 4 {
			t.Fatalf("results len %d, want 4", len(results))
		}
		for i, r := range results {
			if r.IsError {
				t.Errorf("call %d result IsError true: %#v", i, r)
			}
		}
		if asked.Load() != 4 {
			t.Errorf("asked count %d, want 4 (all prompts delivered)", asked.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlocked: 4 concurrent Asks did not complete within 5s")
	}
}

func TestConcurrentSensitiveAskStillSerialized(t *testing.T) {
	tool := &fixedResultTool{name: "read_file"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	var asked atomic.Int64
	asker := permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		time.Sleep(20 * time.Millisecond)
		asked.Add(1)
		return permissions.PromptAllowOnce, nil
	})
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 4, MaxRetries: 0, BaseBackoff: time.Millisecond})
	calls := []providers.ToolCall{
		{ID: "1", Name: "read_file", Arguments: map[string]any{"path": ".env"}},
		{ID: "2", Name: "read_file", Arguments: map[string]any{"path": ".env.local"}},
		{ID: "3", Name: "read_file", Arguments: map[string]any{"path": "secret.pem"}},
		{ID: "4", Name: "read_file", Arguments: map[string]any{"path": ".ssh/id_rsa"}},
	}
	done := make(chan []ToolResult, 1)
	go func() { done <- s.Run(context.Background(), calls, func(Event) bool { return true }) }()
	select {
	case results := <-done:
		for i, r := range results {
			if r.IsError {
				t.Errorf("sensitive call %d IsError %v", i, r)
			}
		}
		if asked.Load() != 4 {
			t.Errorf("asked %d, want 4", asked.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlocked on sensitive concurrent")
	}
}

func TestScheduler_ScrubsSecretsFromToolResults(t *testing.T) {
	tool := &secretContentTool{content: "here is sk-12345678901234567890abcdef and more"}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Allow, map[string]permissions.Decision{"read_file": permissions.Allow})
	s := newScheduler(manager, perms, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	results := s.Run(context.Background(), []providers.ToolCall{{ID: "1", Name: "read_file", Arguments: map[string]any{"path": "normal.txt"}}}, func(Event) bool { return true })
	if len(results) != 1 {
		t.Fatalf("expected 1 result")
	}
	if strings.Contains(results[0].Content, "sk-") {
		t.Errorf("tool result should be scrubbed, got %q", results[0].Content)
	}
	if !strings.Contains(strings.ToLower(results[0].Content), "redacted") {
		t.Errorf("scrubbed result should contain [redacted], got %q", results[0].Content)
	}
}

type secretContentTool struct {
	content string
}

func (t *secretContentTool) Name() string                { return "read_file" }
func (t *secretContentTool) Description() string         { return "test" }
func (t *secretContentTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (t *secretContentTool) Execute(_ context.Context, _ map[string]any) (tools.Result, error) {
	return tools.Result{Content: t.content}, nil
}
