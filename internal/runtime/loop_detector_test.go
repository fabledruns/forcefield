package runtime

import (
	"testing"

	"forcefield/internal/providers"
)

func loopCall(id, name string, args map[string]any) providers.ToolCall {
	if args == nil {
		args = map[string]any{}
	}
	return providers.ToolCall{ID: id, Name: name, Arguments: args}
}

func loopResult(name, content string, success bool) ToolResult {
	return ToolResult{Name: name, Content: content, Success: success}
}

func observeOnce(d *loopDetector, id, name, content string) bool {
	return d.Observe(
		[]providers.ToolCall{loopCall(id, name, nil)},
		[]ToolResult{loopResult(name, content, true)},
	)
}

func TestLoopDetector_AllowsSingleRepeat(t *testing.T) {
	d := newLoopDetector()
	if observeOnce(d, "1", "count", "ok") {
		t.Fatal("first observation must not trip")
	}
	if observeOnce(d, "2", "count", "ok") {
		t.Fatal("a single repeat must not trip the detector")
	}
}

func TestLoopDetector_StopsOnThirdConsecutiveIdentical(t *testing.T) {
	d := newLoopDetector()
	if observeOnce(d, "1", "count", "ok") {
		t.Fatal("first observation must not trip")
	}
	if observeOnce(d, "2", "count", "ok") {
		t.Fatal("second observation must not trip")
	}
	if !observeOnce(d, "3", "count", "ok") {
		t.Fatal("third consecutive identical observation must trip")
	}
}

func TestLoopDetector_InterveningWorkResetsCounts(t *testing.T) {
	d := newLoopDetector()
	// A, A, B, A, A: the repeats are never consecutive, so nothing trips
	// even though the same call ran four times with identical results.
	steps := []struct{ id, name, content string }{
		{"1", "count", "ok"},
		{"2", "count", "ok"},
		{"3", "other", "different"},
		{"4", "count", "ok"},
		{"5", "count", "ok"},
	}
	for i, s := range steps {
		if observeOnce(d, s.id, s.name, s.content) {
			t.Fatalf("step %d (%s) must not trip: intervening work is progress", i+1, s.name)
		}
	}
	// The counter restarted rather than being disabled: a third
	// consecutive identical observation from here still trips.
	if !observeOnce(d, "6", "count", "ok") {
		t.Fatal("third consecutive identical observation after a reset must trip")
	}
}

func TestLoopDetector_ChangedResultResetsCounts(t *testing.T) {
	d := newLoopDetector()
	if observeOnce(d, "1", "count", "v1") {
		t.Fatal("first observation must not trip")
	}
	if observeOnce(d, "2", "count", "v1") {
		t.Fatal("second observation must not trip")
	}
	// New result text is new information: the streak restarts.
	if observeOnce(d, "3", "count", "v2") {
		t.Fatal("changed result must reset the streak, not trip")
	}
	if observeOnce(d, "4", "count", "v2") {
		t.Fatal("second identical v2 observation must not trip")
	}
	if !observeOnce(d, "5", "count", "v2") {
		t.Fatal("third consecutive identical v2 observation must trip")
	}
}

func TestLoopDetector_BatchOrderDoesNotMatter(t *testing.T) {
	d := newLoopDetector()
	batchAB := []providers.ToolCall{loopCall("1", "a", nil), loopCall("2", "b", nil)}
	batchBA := []providers.ToolCall{loopCall("3", "b", nil), loopCall("4", "a", nil)}
	results := []ToolResult{loopResult("a", "ra", true), loopResult("b", "rb", true)}
	// Results must align with the batch order above.
	resultsBA := []ToolResult{loopResult("b", "rb", true), loopResult("a", "ra", true)}
	if d.Observe(batchAB, results) {
		t.Fatal("first batch must not trip")
	}
	// Same pairs in a different order are the same batch, not progress.
	if d.Observe(batchBA, resultsBA) {
		t.Fatal("reordered identical batch must not trip yet")
	}
	if !d.Observe(batchAB, results) {
		t.Fatal("third consecutive identical batch must trip")
	}
}

func TestLoopDetector_GuardsEmptyInput(t *testing.T) {
	d := newLoopDetector()
	if d.Observe(nil, nil) {
		t.Error("empty observation must not trip")
	}
	if d.Observe(
		[]providers.ToolCall{loopCall("1", "a", nil)},
		[]ToolResult{loopResult("a", "x", true), loopResult("b", "y", true)},
	) {
		t.Error("mismatched calls/results must not trip")
	}
	var nilDetector *loopDetector
	if nilDetector.Observe(
		[]providers.ToolCall{loopCall("1", "a", nil)},
		[]ToolResult{loopResult("a", "x", true)},
	) {
		t.Error("nil detector must not trip")
	}
}
