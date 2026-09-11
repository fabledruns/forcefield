package runtime

import (
	"context"
	"io"
	"strings"
	"testing"

	"forcefield/internal/permissions"
	"forcefield/internal/providers"
)

// Note: countingTool (agentic_test.go) records executions; reused here
// to prove denied tools never run. Its fixed name still falls under the
// default Ask policy used below.

// TestScheduler_HeadlessAskFailsClosedOnEOF pins the exact headless
// path (`ff run`, `ff run --resume`, supervised children with no
// terminal): an ask-gated tool whose stdin is closed must be denied,
// never executed and never allowed. Ask.Err on EOF surfaces as
// EventToolDenied through the normal scheduler flow.
func TestScheduler_HeadlessAskFailsClosedOnEOF(t *testing.T) {
	tool := &countingTool{}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	asker := &permissions.StdinAsker{In: strings.NewReader(""), Out: io.Discard}
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: 0})

	denied := false
	results := s.Run(context.Background(),
		[]providers.ToolCall{{ID: "1", Name: "count", Arguments: map[string]any{"command": "rm -rf /"}}},
		func(e Event) bool {
			if e.Type == EventToolDenied {
				denied = true
			}
			return true
		})
	if len(results) != 1 {
		t.Fatalf("results = %+v, want one", results)
	}
	if !results[0].IsError {
		t.Errorf("result = %+v, want a denied error, never a success", results[0])
	}
	if !denied {
		t.Error("no EventToolDenied emitted for an EOF ask")
	}
	if got := tool.calls.Load(); got != 0 {
		t.Errorf("tool executed %d times on a denied ask, want 0", got)
	}
	// Denial must not widen future policy: still Ask, not Allow.
	if got := perms.Check("count"); got != permissions.Ask {
		t.Errorf("Check(count) after denial = %v, want Ask", got)
	}
}

// TestScheduler_HeadlessAskFailsClosedOnGarbage pins the same property
// for unreadable stdin: an ask that cannot be answered is a denial, and
// the run continues to a blocked/error end rather than executing.
func TestScheduler_HeadlessAskFailsClosedOnGarbage(t *testing.T) {
	tool := &countingTool{}
	manager := newTestManager(t, tool)
	perms := newTestPermManager(t, permissions.Ask, nil)
	asker := &permissions.StdinAsker{In: strings.NewReader("maybe-later\n"), Out: io.Discard}
	s := newScheduler(manager, perms, asker, SchedulerConfig{MaxConcurrency: 1, MaxRetries: 0, BaseBackoff: 0})

	// "maybe-later" is not a valid answer and the stream then ends: the
	// asker re-prompts once, hits EOF, and fails closed.
	results := s.Run(context.Background(),
		[]providers.ToolCall{{ID: "1", Name: "count"}},
		func(Event) bool { return true })
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("results = %+v, want one denied error", results)
	}
	if got := tool.calls.Load(); got != 0 {
		t.Errorf("tool executed %d times on an unanswerable ask, want 0", got)
	}
}
