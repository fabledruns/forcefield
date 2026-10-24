package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// Adversarial lifecycle tests for delegation joins and sibling
// attribution. The generic join semantics (child joins before the tool
// result, goroutine settle, duplicate-ID replay) are pinned in
// orchestration_lifecycle_test.go; the tests here cover the two
// parent-stream observables of the same invariants that no other test
// reaches: nothing child-derived may ever arrive after the delegate
// terminal result, and concurrent siblings' reports and branded progress
// lines must attribute to their own child only.

// newTwoAgentAdversarialRuntime wires the same production shape as
// newPermissionAdversarialRuntime but registers two child agents with
// distinct names, so delegate progress lines carry an attributable
// agent name and every sibling's accounting can be checked separately.
// delegate is allowed for the parent; children never need an operator.
func newTwoAgentAdversarialRuntime(t *testing.T, p providers.ModelProvider) *Runtime {
	t.Helper()
	full := tools.NewManager(tools.NewRegistry())
	echo := &countingEchoTool{}
	if err := full.Register(echo); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	registry := agent.NewRegistry()
	for _, name := range []string{"alpha", "beta"} {
		if err := registry.Register(agent.Definition{
			Name:         name,
			Description:  "test child agent " + name,
			SystemPrompt: "child-system",
			Tools:        []string{"echo"},
			Skills:       []string{},
		}); err != nil {
			t.Fatalf("register agent %s: %v", name, err)
		}
	}
	orch := newOrchestrator(
		config.Orchestration{Enabled: true, ChildPermissions: config.ChildPermissionsInteractive},
		registry, nil, full)
	if err := full.Register(newDelegateTool(orch)); err != nil {
		t.Fatalf("register delegate: %v", err)
	}

	// Allow the child's own echo calls, keep the parent's delegate open;
	// everything else keeps the ask-by-default posture.
	rules := permissions.Rules{
		Default: permissions.Ask,
		Tools: map[string]permissions.Decision{
			"delegate": permissions.Allow,
			"echo":     permissions.Allow,
		},
	}
	store := newRecordingRulesStore(rules)
	manager := permissions.NewManagerWithRules(rules, store)

	r := &Runtime{
		provider:    p,
		agent:       agent.New("root", "root-system", ""),
		activeAgent: "root",
		manager:     full,
		fullManager: full,
		orch:        orch,
		scheduler:   newScheduler(full, manager, nil, DefaultSchedulerConfig),
	}
	orch.parent = r
	r.scheduler.askSerial = orch.childAskSerial()
	return r
}

// TestAdversarialNoChildProgressAfterDelegateTerminal [R]: join-before-
// return observed from the parent's stream. Every "[delegate: agent ...]"
// progress line the child produced must precede the delegate tool's
// terminal result event. A composition or join failure that lets the
// child keep streaming after the parent accepted its result would show
// up as a post-terminal line on the parent channel — structurally
// impossible with the unconditional join, so the property is timing-free.
func TestAdversarialNoChildProgressAfterDelegateTerminal(t *testing.T) {
	provider := &keyedProvider{
		parentTurns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{delegateCall("call-quiet-join", "researcher", "quiet join")}},
				{Done: true},
			},
			stringTurns("parent read the report."),
		},
		// Three turns require tool calls (a text-turn ends the run), so
		// the child calls echo twice and reports on the third turn.
		childTurns: map[string][][]providers.StreamEvent{
			"quiet join": {
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-child-echo-a1",
						Name:      "echo",
						Arguments: map[string]any{"value": "a1"},
					}}},
					{Done: true},
				},
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-child-echo-a2",
						Name:      "echo",
						Arguments: map[string]any{"value": "a2"},
					}}},
					{Done: true},
				},
				stringTurns("child burst three"),
			},
		},
	}
	r := newProbeRuntime(t, provider, config.Orchestration{Enabled: true}, newChildRegistryForTests())

	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "observe the quiet join",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	drained := drainAllEventsForTest(t, events)

	terminalAt := -1
	for i, ev := range drained {
		if ev.ToolResult != nil && ev.ToolResult.ToolCallID == "call-quiet-join" &&
			(ev.Type == EventToolFinish || ev.Type == EventToolFailed) {
			terminalAt = i
			break
		}
	}
	if terminalAt < 0 {
		t.Fatal("delegate terminal never reached the parent transcript")
	}
	if !strings.Contains(drained[terminalAt].ToolResult.Content, "child burst three") {
		t.Fatalf("terminal report body lost the child's final turn:\n%.300q", drained[terminalAt].ToolResult.Content)
	}
	if h := drained[terminalAt].ToolResult.Content; !strings.Contains(h, "turns=3 tool_calls=2") {
		t.Fatalf("delegate header lost the child's full run:\n%.200q", h)
	}

	// The child's full turn history, each line the recorder produced for
	// exactly one of the three turns, all strictly before the terminal.
	var before []string
	var after []string
	for i, ev := range drained {
		if ev.Type == EventToolProgress && ev.ToolProgress != nil &&
			ev.ToolProgress.Stream == "progress" && strings.HasPrefix(ev.ToolProgress.Data, "[delegate: agent") {
			if i < terminalAt {
				before = append(before, ev.ToolProgress.Data)
			} else {
				after = append(after, ev.ToolProgress.Data)
			}
		}
	}
	if len(after) != 0 {
		t.Fatalf("child progress reported after the delegate terminal - join-before-return broken: %v", after)
	}
	if len(before) != 3 {
		t.Fatalf("progress lines before terminal = %d (%v), want one per child turn (3)", len(before), before)
	}
}

// TestAdversarialSiblingReportsAttributeTheirOwnTurns [R]: two sibling
// children with different turn counts run from one parent turn. Each
// sibling's report carries its own accounting and body cross-checked
// against its own scripted turns only - a crossed or shared recorder
// would show beta's count or text inside alpha's report or vice versa.
// Progress lines keyed by agent name count exactly one line per turn and
// arrive in per-agent order 1..N.
func TestAdversarialSiblingReportsAttributeTheirOwnTurns(t *testing.T) {
	provider := &keyedProvider{
		parentTurns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{
					delegateCall("call-alpha", "alpha", "alpha leg"),
					delegateCall("call-beta", "beta", "beta leg"),
				}},
				{Done: true},
			},
			stringTurns("parent coordinated both legs."),
		},
		// Alpha: one text turn, no tools. Beta: three turns (two echo
		// calls with distinct arguments, then the report text).
		childTurns: map[string][][]providers.StreamEvent{
			"alpha leg": {stringTurns("alpha marker report A11 done.")},
			"beta leg": {
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-child-echo-b1",
						Name:      "echo",
						Arguments: map[string]any{"value": "b1"},
					}}},
					{Done: true},
				},
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-child-echo-b2",
						Name:      "echo",
						Arguments: map[string]any{"value": "b2"},
					}}},
					{Done: true},
				},
				stringTurns("beta burst three. B33"),
			},
		},
	}
	r := newTwoAgentAdversarialRuntime(t, provider)

	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "watch two legs run",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	drained := drainAllEventsForTest(t, events)

	results := map[string]*ToolResult{}
	for _, ev := range drained {
		if ev.ToolResult != nil && (ev.Type == EventToolFinish || ev.Type == EventToolFailed) {
			results[ev.ToolResult.ToolCallID] = ev.ToolResult
		}
	}
	alpha, ok := results["call-alpha"]
	if !ok {
		t.Fatal("alpha's delegate result is missing")
	}
	beta, ok := results["call-beta"]
	if !ok {
		t.Fatal("beta's delegate result is missing")
	}

	// Each body is exactly its own child's final answer.
	if !strings.Contains(alpha.Content, "alpha marker report A11 done.") {
		t.Fatalf("alpha report lost its own body:\n%.300q", alpha.Content)
	}
	if strings.Contains(alpha.Content, "beta") {
		t.Fatalf("alpha report swallowed sibling material:\n%.300q", alpha.Content)
	}
	if !strings.Contains(beta.Content, "beta burst three. B33") {
		t.Fatalf("beta report lost its final turn:\n%.300q", beta.Content)
	}
	if strings.Contains(beta.Content, "alpha") {
		t.Fatalf("beta report swallowed sibling material:\n%.300q", beta.Content)
	}
	// Each header counts its own turns: honest per-child accounting.
	if !strings.Contains(alpha.Content, "turns=1") || !strings.Contains(beta.Content, "turns=3 tool_calls=2") {
		t.Fatalf("turn accounting not per-child (alpha %.120q / beta %.120q)", alpha.Content, beta.Content)
	}

	// Progress lines carry the agent name and count one line per turn,
	// in order, per agent - interleaving with another child must not
	// commit a shared-counter mixup.
	type agentLines struct {
		name  string
		turns []int
	}
	lines := map[string]*agentLines{"alpha": {name: "alpha"}, "beta": {name: "beta"}}
	for _, ev := range drained {
		if ev.Type != EventToolProgress || ev.ToolProgress == nil ||
			ev.ToolProgress.Stream != "progress" || !strings.HasPrefix(ev.ToolProgress.Data, "[delegate: agent") {
			continue
		}
		var name string
		var turn int
		if n, err := fmt.Sscanf(ev.ToolProgress.Data, "[delegate: agent %q turn %d]", &name, &turn); err != nil || n != 2 {
			continue // other tools may stream [delegate: ...] shaped lines
		}
		a, ok := lines[name]
		if !ok {
			t.Fatalf("progress line for unknown agent %q", name)
		}
		a.turns = append(a.turns, turn)
	}
	if got := lines["alpha"].turns; len(got) != 1 || got[0] != 1 {
		t.Fatalf("alpha progress turns = %v, want [1]", got)
	}
	if got := lines["beta"].turns; len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("beta progress turns = %v, want [1 2 3] in arrival order", got)
	}
}
