package runtime

import (
	"context"
	"sync"
	"testing"
)

// TestSharedTurnBudgetCountsAndFiresOnce pins the aggregate
//
//	child-turn budget semantics: every turn counts, crossing the limit
//
// is reported exactly once, but every over-limit charge stays
// over-limit for cancellation purposes.
func TestSharedTurnBudgetCountsAndFiresOnce(t *testing.T) {
	sh := &orchestrationShared{}
	const limit = 5

	for i := 1; i <= limit; i++ {
		total, over, fire := sh.consumeTurn(limit)
		if total != i || over || fire {
			t.Fatalf("charge %d: total=%d over=%v fire=%v, expected in-budget", i, total, over, fire)
		}
	}
	total, over, fire := sh.consumeTurn(limit)
	if total != limit+1 || !over || !fire {
		t.Fatalf("first crossing: total=%d over=%v fire=%v", total, over, fire)
	}
	// Every subsequent charge stays over-limit but reports no more.
	total2, over2, fire2 := sh.consumeTurn(limit)
	if total2 != limit+2 || !over2 || fire2 {
		t.Fatalf("second crossing: total=%d over=%v fire=%v", total2, over2, fire2)
	}
}

// TestChildRecorderAggregateBudgetCancelsChild pins the recorder-side
// wiring: the offending child's context is cancelled once the shared
// pool is crossed, the recorder remembers that for the report, and
// other children consuming later keep the same honest accounting.
func TestChildRecorderAggregateBudgetCancelsChild(t *testing.T) {
	shared := &orchestrationShared{}
	cctx, cancel := context.WithCancel(context.Background())
	rec := &childRunRecorder{
		budget:       maxChildProgressLines,
		agentName:    "worker",
		shared:       shared,
		turnLimit:    2,
		budgetCancel: cancel,
		childCtx:     cctx,
		childRunID:   "c1",
	}

	// Two in-budget turns do not cancel. Reasoning deltas streaming
	// inside an inference must not charge: wave-1 counted deltas as
	// turns, which exhausted the pool mid-model-turn.
	rec.On(Event{Type: EventThinking})
	rec.On(Event{Type: EventThinking, Thinking: "first inference deltas"})
	rec.On(Event{Type: EventThinking})
	rec.On(Event{Type: EventThinking, Thinking: "second inference deltas"})
	if cctx.Err() != nil {
		t.Fatal("child cancelled before the limit was crossed; reasoning deltas leaked into the budget")
	}
	if rec.turns != 2 {
		t.Errorf("turns = %d, want 2 (markers count, deltas do not)", rec.turns)
	}
	if rec.overBudget {
		t.Error("budget flag set before the limit was crossed")
	}

	// The third turn crosses the shared pool: the offending child
	// cancels and the flag lands for the report.
	rec.On(Event{Type: EventThinking})
	rec.On(Event{Type: EventThinking, Thinking: "post-budget delta ignored"})
	if cctx.Err() == nil {
		t.Fatal("expected child cancellation once the aggregate budget was crossed")
	}
	if !rec.overBudget {
		t.Error("recorder must remember the budget cancellation for the report")
	}
	if rec.turns != 3 {
		t.Errorf("turns = %d, want 3 (post-budget delta must not count)", rec.turns)
	}
	if shared.turns != 3 {
		t.Errorf("shared turns = %d, want 3", shared.turns)
	}

	// Another child sharing the pool: its over-limit charge also
	// cancels itself (honest), but only the first crossing reported.
	otherCtx, otherCancel := context.WithCancel(context.Background())
	t.Cleanup(otherCancel)
	other := &childRunRecorder{
		shared:       shared,
		turnLimit:    2,
		agentName:    "b",
		budgetCancel: otherCancel,
		childCtx:     otherCtx,
		childRunID:   "c2",
	}
	other.On(Event{Type: EventThinking})
	if otherCtx.Err() == nil {
		t.Fatal("expected the second over-limit child to cancel too")
	}
	if !other.overBudget {
		t.Error("second child must also record its budget cancellation")
	}
}

// TestBudgetConcurrencyNoLostCharges races the shared counter to prove
// the mutex keeps the aggregate exact under parallel schedulers.
func TestBudgetConcurrencyNoLostCharges(t *testing.T) {
	shared := &orchestrationShared{}
	const limit = 10

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				shared.consumeTurn(limit)
			}
		}()
	}
	wg.Wait()

	if got := shared.turns; got != 8*25 {
		t.Errorf("lost charges: turns = %d, want %d", got, 8*25)
	}
}
