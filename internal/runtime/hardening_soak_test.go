package runtime

import (
	"context"
	"testing"

	"forcefield/internal/providers"
)

// P1.15 lab: 60-iteration soak proving termination + bounded provider view.
// Uses the same-package seam (scriptedProvider, newTestRuntime).
func TestHardeningSoakSixtyIterationsTerminates(t *testing.T) {
	turns := make([][]providers.StreamEvent, 0, 61)
	for i := 0; i < 60; i++ {
		// Varying args per iteration: identical repeats would correctly
		// trip the loop detector at 3 (tested separately).
		turns = append(turns, []providers.StreamEvent{
			{ToolCalls: []providers.ToolCall{{ID: callID(i), Name: "echo", Arguments: map[string]any{"value": itoa(i)}}}},
			{Done: true},
		})
	}
	turns = append(turns, []providers.StreamEvent{{Text: "done"}, {Done: true}})
	p := &scriptedProvider{turns: turns}
	tool := &fixedResultTool{name: "echo"}
	manager := newTestManager(t, tool)
	rt := &Runtime{
		provider:  p,
		agent:     newTestRuntime(p).agent,
		manager:   manager,
		scheduler: newScheduler(manager, nil, nil, DefaultSchedulerConfig),
		limits:    Limits{MaxIterations: 70, MaxToolCalls: 300, MaxConsecutiveFailures: 5},
	}
	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "soak"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var toolFinishes, blocked, done int
	var maxSeen int
	for ev := range events {
		switch ev.Type {
		case EventToolFinish, EventToolFailed:
			toolFinishes++
		case EventBlocked:
			blocked++
		case EventDone:
			done++
		}
		// Provider must never see unbounded history: window is 100 msgs.
		_ = maxSeen
	}
	if toolFinishes != 60 {
		t.Fatalf("soak executed %d tools, want 60", toolFinishes)
	}
	if done != 1 || blocked != 0 {
		t.Fatalf("soak terminal: done=%d blocked=%d, want done=1 blocked=0", done, blocked)
	}
	if p.calls != 61 {
		t.Fatalf("provider turns=%d, want 61", p.calls)
	}
	// Every provider call must carry a bounded message window.
	for i, msgs := range p.messages {
		if len(msgs) > maxContextMessages+8 {
			t.Fatalf("turn %d provider view unbounded: %d messages", i, len(msgs))
		}
	}
}

func callID(i int) string {
	return string(rune('a'+i%26)) + string(rune('0'+i/26%10)) + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [8]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
