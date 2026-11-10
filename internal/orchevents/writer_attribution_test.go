package orchevents

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Scenario 7: Generation mismatch through the headless writer.
// Verifies that orchevents.Writer in headless runs preserves run attribution
// and sequencing, dropping any events from mismatched run IDs without
// cross-run event pollution.
func TestOrchLifecycle_HeadlessWriterRunAttribution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.events.jsonl")

	w, err := NewWriter(path, "run-active")
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	// 1. Mismatched straggler from an old run: must be dropped.
	w.Emit(Event{
		Type:       AgentSpawned,
		RunID:      "run-old",
		Agent:      "old-agent",
		Chain:      "root > old-agent",
		Depth:      1,
		CallID:     "call-old",
		ChildRunID: "child-old",
	})

	// 2. Event for the active run with explicit matching RunID.
	w.Emit(Event{
		Type:      RunStarted,
		RunID:     "run-active",
		Agent:     "root",
		TaskChars: 42,
	})

	// 3. Event for the active run with empty RunID: writer stamps w.runID.
	w.Emit(Event{
		Type:       AgentSpawned,
		Agent:      "researcher",
		Chain:      "root > researcher",
		Depth:      1,
		CallID:     "call-1",
		ChildRunID: "child-1",
	})

	// 4. Another mismatched straggler from another run: must be dropped.
	w.Emit(Event{
		Type:       ChildCompleted,
		RunID:      "run-different",
		Agent:      "diff-agent",
		Chain:      "root > diff-agent",
		Depth:      1,
		CallID:     "call-diff",
		ChildRunID: "child-diff",
		Outcome:    OutcomeDone,
	})

	// 5. Normal completion for the active run.
	w.Emit(Event{
		Type:    RunCompleted,
		RunID:   "run-active",
		Outcome: OutcomeDone,
	})

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Read back lines and verify:
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var events []Event
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}

	// Exactly 3 events written: RunStarted, AgentSpawned, RunCompleted.
	if len(events) != 3 {
		t.Fatalf("got %d events in file, want 3; mismatched runs were not dropped: %+v", len(events), events)
	}

	// Verify contiguous sequencing:
	for i, ev := range events {
		if want := int64(i + 1); ev.Seq != want {
			t.Fatalf("event %d carries seq %d, want %d", i, ev.Seq, want)
		}
		if ev.RunID != "run-active" {
			t.Fatalf("event %d carries run %q, want run-active", i, ev.RunID)
		}
	}

	if events[0].Type != RunStarted || events[1].Type != AgentSpawned || events[2].Type != RunCompleted {
		t.Fatalf("unexpected event sequence: %+v", events)
	}
}
