package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"forcefield/internal/permissions"
	"forcefield/internal/providers"
)

// TestRun_RepeatCallIDExecutesOnce pins run-scoped idempotency: when a
// later turn repeats an already-executed call ID, the tool does not run
// again. The recorded result is reused with an explicit note, and the
// normal Start/terminal event pair keeps transcript and session paired.
func TestRun_RepeatCallIDExecutesOnce(t *testing.T) {
	counter := &countingTool{}
	p := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{ToolCalls: []providers.ToolCall{{ID: "c1", Name: "count", Arguments: map[string]any{}}}}, {Done: true}},
		{{ToolCalls: []providers.ToolCall{{ID: "c1", Name: "count", Arguments: map[string]any{}}}}, {Done: true}},
		{{Text: "done", Done: true}},
	}}
	rt := newTestRuntimeWithLimits(p, DefaultLimits, counter)

	var types []EventType
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var done bool
	for ev := range events {
		types = append(types, ev.Type)
		if ev.Type == EventDone {
			done = true
		}
	}
	if !done {
		t.Fatal("run did not finish")
	}
	if got := counter.calls.Load(); got != 1 {
		t.Errorf("tool executed %d times, want exactly 1", got)
	}
	// The repeat still surfaces explicitly: two Start events, two
	// finishes, no silent skip.
	var starts, finishes int
	for _, ty := range types {
		switch ty {
		case EventToolStart:
			starts++
		case EventToolFinish:
			finishes++
		}
	}
	if starts != 2 || finishes != 2 {
		t.Errorf("starts=%d finishes=%d, want 2/2 explicit pairs", starts, finishes)
	}
	// The second tool message carries the recorded result plus the
	// cached-reuse note.
	if len(p.messages) != 3 {
		t.Fatalf("provider turns = %d, want 3", len(p.messages))
	}
	var seconds []providers.Message
	for _, m := range p.messages[2] {
		if m.Role == providers.ToolRole {
			seconds = append(seconds, m)
		}
	}
	if len(seconds) == 0 {
		t.Fatal("no tool messages in second turn")
	}
	// The last tool message is the repeat; earlier ones are history.
	last := seconds[len(seconds)-1]
	if !strings.Contains(last.Content, "count") && !strings.Contains(last.Content, "ok") {
		t.Errorf("reused result lost original content: %.200q", last.Content)
	}
	if !strings.Contains(last.Content, "not executed again") {
		t.Errorf("reused result lacks the explicit cached note: %.300q", last.Content)
	}
}

// TestRun_DistinctIDsSameArgsExecuteTwice pins that identity is the
// call ID, not the payload: two different IDs with identical
// tool+arguments are two calls and both run.
func TestRun_DistinctIDsSameArgsExecuteTwice(t *testing.T) {
	counter := &countingTool{}
	p := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{
			ToolCalls: []providers.ToolCall{
				{ID: "c1", Name: "count", Arguments: map[string]any{}},
				{ID: "c2", Name: "count", Arguments: map[string]any{}},
			},
		}, {Done: true}},
		{{Text: "done", Done: true}},
	}}
	rt := newTestRuntimeWithLimits(p, DefaultLimits, counter)

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range events {
	}
	if got := counter.calls.Load(); got != 2 {
		t.Errorf("tool executed %d times, want 2 (distinct IDs are distinct calls)", got)
	}
}

// TestRun_EmptyIDsAlwaysExecute pins the fail-open rule: calls without
// any identity cannot be proven repeats, so they always run.
func TestRun_EmptyIDsAlwaysExecute(t *testing.T) {
	counter := &countingTool{}
	p := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{ToolCalls: []providers.ToolCall{{ID: "", Name: "count", Arguments: map[string]any{}}}}, {Done: true}},
		{{ToolCalls: []providers.ToolCall{{ID: "", Name: "count", Arguments: map[string]any{}}}}, {Done: true}},
		{{Text: "done", Done: true}},
	}}
	rt := newTestRuntimeWithLimits(p, DefaultLimits, counter)

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range events {
	}
	if got := counter.calls.Load(); got != 2 {
		t.Errorf("tool executed %d times, want 2 (empty IDs never dedup)", got)
	}
}

// TestRun_RepeatDeniedKeepsTerminalKind pins that a repeated denied
// call replays denial (not success): the recorded terminal kind is
// part of the identity record, and the tool still never runs.
func TestRun_RepeatDeniedKeepsTerminalKind(t *testing.T) {
	ran := false
	tool := &fnTool{name: "shell", fn: func() { ran = true }}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Allow, map[string]permissions.Decision{"shell": permissions.Deny})
	sched := newScheduler(manager, perms, nil, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: time.Millisecond})
	p := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{ToolCalls: []providers.ToolCall{{ID: "c1", Name: "shell", Arguments: map[string]any{}}}}, {Done: true}},
		{{ToolCalls: []providers.ToolCall{{ID: "c1", Name: "shell", Arguments: map[string]any{}}}}, {Done: true}},
		{{Text: "done", Done: true}},
	}}
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   manager,
		scheduler: sched,
		limits:    DefaultLimits,
	}

	var denied int
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for ev := range events {
		if ev.Type == EventToolDenied {
			denied++
		}
	}
	if denied != 2 {
		t.Errorf("denied events = %d, want 2 (original + replayed denial)", denied)
	}
	if ran {
		t.Error("denied tool executed")
	}
}
