package runtime

import (
	"context"
	"strings"
	"testing"

	"forcefield/internal/providers"
)

// TestTurnBytesRunawayStreamFailsTheTurn pins the per-turn stream cap: a
// provider that never terminates its turn must fail with EventError,
// never accumulate without bound and never report success. The script
// holds a single turn, so any turn-level retry would index past it and
// panic — passing also proves no retry.
func TestTurnBytesRunawayStreamFailsTheTurn(t *testing.T) {
	chunk := strings.Repeat("x", 64*1024)
	var flood []providers.StreamEvent
	for len(flood)*len(chunk) <= maxTurnBytes+1024 {
		flood = append(flood, providers.StreamEvent{Text: chunk})
	}
	p := &scriptedProvider{turns: [][]providers.StreamEvent{flood}}
	tool := &fixedResultTool{name: "echo"}
	manager := newTestManager(t, tool)
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   manager,
		scheduler: newScheduler(manager, nil, nil, DefaultSchedulerConfig),
		limits:    DefaultLimits,
	}
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var sawDone, sawError bool
	var errText string
	for ev := range events {
		switch ev.Type {
		case EventDone:
			sawDone = true
		case EventError:
			sawError = true
			if ev.Err != nil {
				errText = ev.Err.Error()
			}
		}
	}
	if sawDone {
		t.Fatal("runaway stream reported as EventDone (unbounded output confused with success)")
	}
	if !sawError {
		t.Fatal("runaway stream must surface as EventError")
	}
	if !strings.Contains(errText, "runaway") {
		t.Errorf("error = %q, want it to name the runaway-stream cap", errText)
	}
	if p.calls != 1 {
		t.Errorf("provider turns = %d, want exactly 1 (no retry after emitted output)", p.calls)
	}
}

// TestTurnBytesNormalTurnUnaffected pins that the cap sits far above
// legitimate traffic: a large-but-sane turn completes normally.
func TestTurnBytesNormalTurnUnaffected(t *testing.T) {
	big := strings.Repeat("y", 512*1024)
	p := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{Text: big}, {Text: "tail", Done: true}},
	}}
	tool := &fixedResultTool{name: "echo"}
	manager := newTestManager(t, tool)
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   manager,
		scheduler: newScheduler(manager, nil, nil, DefaultSchedulerConfig),
		limits:    DefaultLimits,
	}
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "hi"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var sawDone, sawError bool
	for ev := range events {
		if ev.Type == EventDone {
			sawDone = true
		}
		if ev.Type == EventError {
			sawError = true
		}
	}
	if !sawDone || sawError {
		t.Errorf("done = %v, error = %v; legitimate turn must complete", sawDone, sawError)
	}
}

func TestStreamEventBytesSizesNesting(t *testing.T) {
	e := providers.StreamEvent{
		Text:     "abc",
		Thinking: "de",
		ToolCalls: []providers.ToolCall{{
			ID:   "1",
			Name: "shell",
			Arguments: map[string]any{
				"command": "echo hi",
				"nested":  map[string]any{"k": "v"},
				"list":    []any{"a", "b"},
				"n":       42,
			},
		}},
	}
	got := streamEventBytes(e)
	// 3 + 2 text + (1 + 5 ids) + args content; exact value is
	// implementation detail, but it must account for nested content.
	if got < 3+2+1+5+7+1+1+2 {
		t.Errorf("streamEventBytes = %d, want at least the visible content length", got)
	}
}
