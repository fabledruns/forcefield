package runtime

import (
	"context"
	"strings"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// newChildRegistryForTests returns a registry holding one specialist child
// agent whose tool list resolves inside the hand-built test universe
// (echo). Builtins are deliberately untouched so the test never depends
// on native tool availability.
func newChildRegistryForTests() *agent.Registry {
	registry := agent.NewRegistry()
	if err := registry.Register(agent.Definition{
		Name:         "researcher",
		Description:  "test child agent",
		SystemPrompt: "child-system",
		Tools:        []string{"echo"},
		Skills:       []string{},
	}); err != nil {
		panic(err)
	}
	return registry
}

// newOrchestrationTestRuntime is the orchestration counterpart of
// newTestRuntime: the same hand-built single-tool universe, plus the
// delegate tool and an orchestrator wired to the runtime exactly the way
// newRuntime wires them (delegate registered once on the full universe,
// orchestration enabled by default bounds). The parent scheduler carries
// no permissions manager: the deny-mode asker and the scheduler's own
// nil-guards provide the same behavior tests assert on.
func newOrchestrationTestRuntime(t *testing.T, p providers.ModelProvider) *Runtime {
	t.Helper()
	full := tools.NewManager(tools.NewRegistry())
	if err := full.Register(echoTool{}); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	registry := newChildRegistryForTests()
	orch := newOrchestrator(config.Orchestration{Enabled: true}, registry, nil, full)
	if err := full.Register(newDelegateTool(orch)); err != nil {
		t.Fatalf("register delegate: %v", err)
	}

	r := &Runtime{
		provider:    p,
		agent:       agent.New("root", "root-system", ""),
		activeAgent: "root",
		manager:     full,
		fullManager: full,
		orch:        orch,
		scheduler:   newScheduler(full, nil, nil, DefaultSchedulerConfig),
	}
	orch.parent = r
	return r
}

// orchestrationTurns scripts one full parent+child exchange on the single
// shared test provider: parent turn 1 issues a delegate call, the child
// runs echo then reports, and the parent closes with its own answer.
func orchestrationTurns(taskText string) [][]providers.StreamEvent {
	return [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{
				ID:   "call-delegate",
				Name: "delegate",
				Arguments: map[string]any{
					"agent": "researcher",
					"task":  taskText,
				},
			}}},
			{Done: true},
		},
		{
			{ToolCalls: []providers.ToolCall{{
				ID:        "call-echo",
				Name:      "echo",
				Arguments: map[string]any{"value": "child-tool-output"},
			}}},
			{Done: true},
		},
		{
			{Text: "child final answer"},
			{Done: true},
		},
		{
			{Text: "coordinated."},
			{Done: true},
		},
	}
}

func TestDelegateOutsideRunFailsClosed(t *testing.T) {
	registry := newChildRegistryForTests()
	full := tools.NewManager(tools.NewRegistry())
	if err := full.Register(echoTool{}); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	orch := newOrchestrator(config.Orchestration{Enabled: true}, registry, nil, full)
	// parent present but no scope in the context: a direct Manager caller
	// (no active agent loop) must be rejected, never silently executed.
	orch.parent = &Runtime{}

	tool := delegateTool{orch: orch}
	result, err := tool.Execute(context.Background(), map[string]any{
		"agent": "researcher",
		"task":  "do something",
	})
	if err != nil {
		t.Fatalf("Execute() returned a Go error for an out-of-run call: %v", err)
	}
	if !result.IsError {
		t.Fatalf("result.IsError = false, want fail-closed soft error")
	}
	if !strings.Contains(result.Content, "delegation is only available inside an agent run") {
		t.Fatalf("result.Content = %q, want out-of-run failure message", result.Content)
	}
}

func TestDelegateToolValidation(t *testing.T) {
	full := tools.NewManager(tools.NewRegistry())
	if err := full.Register(echoTool{}); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	registry := newChildRegistryForTests()
	orch := newOrchestrator(config.Orchestration{Enabled: true}, registry, nil, full)
	orch.parent = &Runtime{
		agent:       agent.New("root", "root-system", ""),
		activeAgent: "root",
		manager:     full,
		scheduler:   newScheduler(full, nil, nil, DefaultSchedulerConfig),
	}
	snap := runSnapshot{agent: orch.parent.agent, scheduler: orch.parent.scheduler}
	tool := delegateTool{orch: orch}

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "unknown agent",
			args: map[string]any{"agent": "does-not-exist", "task": "x"},
			want: "unknown agent",
		},
		{
			name: "oversized task",
			args: map[string]any{"agent": "researcher", "task": strings.Repeat("a", 8001)},
			want: "exceeding the limit of 8000",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := withOrchestrationScope(context.Background(), orch.rootScope(&snap))
			result, err := tool.Execute(ctx, tc.args)
			if err != nil {
				t.Fatalf("Execute() error = %v, want soft error", err)
			}
			if !result.IsError {
				t.Fatalf("result.IsError = false, want soft failure")
			}
			if !strings.Contains(result.Content, tc.want) {
				t.Fatalf("result.Content = %q, want substring %q", result.Content, tc.want)
			}
		})
	}

	t.Run("missing fields", func(t *testing.T) {
		ctx := withOrchestrationScope(context.Background(), orch.rootScope(&snap))
		for _, args := range []map[string]any{
			{"agent": "researcher"},
			{"task": "x"},
			{"agent": "", "task": ""},
		} {
			result, err := tool.Execute(ctx, args)
			if err != nil {
				t.Fatalf("Execute(%v) error = %v, want soft error", args, err)
			}
			if !result.IsError {
				t.Fatalf("Execute(%v) IsError = false, want soft failure", args)
			}
			if !strings.Contains(result.Content, "missing") {
				t.Fatalf("result.Content = %q, want missing-field failure", result.Content)
			}
		}
	})
}

func TestDelegateRunsChildAgent(t *testing.T) {
	taskText := "summarize the repo naming conventions"
	provider := &scriptedProvider{turns: orchestrationTurns(taskText)}
	runtime := newOrchestrationTestRuntime(t, provider)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "coordinate a subtask"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	delegateFinish, final := delegateRunSummary(events)

	if provider.calls != 4 {
		t.Fatalf("provider calls = %d, want 4 (parent, child x2, parent)", provider.calls)
	}
	if final == nil || final.Content != "coordinated." {
		t.Fatalf("final response = %#v, want parent's own closing answer", final)
	}
	if delegateFinish == nil {
		t.Fatalf("delegate call never produced a terminal tool result")
	}
	if delegateFinish.Name != "delegate" {
		t.Fatalf("finish name = %q, want delegate", delegateFinish.Name)
	}
	if !strings.Contains(delegateFinish.Content, "[delegate] agent=\"researcher\"") {
		t.Fatalf("delegate result = %q, want attribution header", delegateFinish.Content)
	}
	if !strings.Contains(delegateFinish.Content, "turns=2") {
		t.Fatalf("delegate result = %q, want child turn count 2", delegateFinish.Content)
	}
	if !strings.Contains(delegateFinish.Content, "tool_calls=1") {
		t.Fatalf("delegate result = %q, want child tool call count 1", delegateFinish.Content)
	}
	if !strings.Contains(delegateFinish.Content, "child final answer") {
		t.Fatalf("delegate result = %q, want child's final report", delegateFinish.Content)
	}
}

func TestDelegateTaskEntersAsUserMessageOnly(t *testing.T) {
	taskText := "secret-task-payload: rotate the certificates"
	provider := &scriptedProvider{turns: orchestrationTurns(taskText)}
	runtime := newOrchestrationTestRuntime(t, provider)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "coordinate a subtask"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	for range events { // drain: assertion happens on the captured provider calls
	}

	if len(provider.messages) != 4 {
		t.Fatalf("provider calls captured = %d, want 4", len(provider.messages))
	}
	childFirst := provider.messages[1] // child turn 1
	if len(childFirst) < 2 {
		t.Fatalf("child turn 1 has %d messages, want at least system + task", len(childFirst))
	}
	sys := childFirst[0]
	if sys.Role != providers.SystemRole {
		t.Fatalf("child first message role = %v, want system", sys.Role)
	}
	if !strings.Contains(sys.Content, "child-system") {
		t.Fatalf("child system prompt = %q, want the child agent's own identity", sys.Content)
	}
	if strings.Contains(sys.Content, taskText) {
		t.Fatalf("child system prompt contains the task text: isolation violated")
	}
	for _, m := range childFirst[1:] {
		if strings.Contains(m.Content, taskText) && m.Role != providers.UserRole {
			t.Fatalf("task text found in non-user child message (role %v)", m.Role)
		}
	}
	var taskAsUser bool
	for _, m := range childFirst {
		if m.Role == providers.UserRole && strings.Contains(m.Content, taskText) {
			taskAsUser = true
		}
	}
	if !taskAsUser {
		t.Fatalf("task text never reached the child as a user message")
	}
	// The parent conversation itself never enters the child context: the
	// only task message is the delegated payload, nothing else parenty.
	for _, m := range childFirst {
		if strings.Contains(m.Content, "coordinate a subtask") {
			t.Fatalf("parent conversation leaked into the child context")
		}
	}
}

func TestWithDelegateToolVisibility(t *testing.T) {
	orch := newOrchestrator(config.Orchestration{Enabled: true}, newChildRegistryForTests(), nil, nil)
	if got := withDelegateTool(nil, []string{"echo"}); len(got) != 1 || got[0] != "echo" {
		t.Fatalf("withDelegateTool(nil, [echo]) = %v, want unchanged list", got)
	}
	if got := withDelegateTool(orch, []string{}); len(got) != 0 {
		t.Fatalf("withDelegateTool(orch, []) = %v, want unchanged explicit-no-tools list", got)
	}
	got := withDelegateTool(orch, []string{"echo"})
	if len(got) != 2 || got[0] != "echo" || got[1] != "delegate" {
		t.Fatalf("withDelegateTool(orch, [echo]) = %v, want [echo delegate]", got)
	}
	if got := withDelegateTool(orch, []string{"echo", "delegate"}); len(got) != 2 {
		t.Fatalf("withDelegateTool(orch, [echo delegate]) = %v, want list unchanged", got)
	}
}

// delegateRunSummary drains one run's event stream and returns the
// terminal tool result for the delegate call (finish or failure) plus
// the final response.
func delegateRunSummary(events <-chan Event) (*ToolResult, *providers.Response) {
	var finish *ToolResult
	var final *providers.Response
	for e := range events {
		if e.ToolResult != nil && e.ToolResult.Name == "delegate" {
			switch e.Type {
			case EventToolFinish, EventToolFailed, EventToolCancelled, EventToolDenied:
				finish = e.ToolResult
			}
		}
		if e.Type == EventDone {
			final = e.Response
		}
	}
	return finish, final
}
