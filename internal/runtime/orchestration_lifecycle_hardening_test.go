package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"forcefield/internal/config"
	"forcefield/internal/orchevents"
	"forcefield/internal/tools"
)

// TestOrchestrationScopeSinkIsolation verifies that orchestration scopes capture
// the sink and execution identity at run entry, preventing late emissions from
// reaching a newly attached sink when the orchestrator's sink is swapped.
func TestOrchestrationScopeSinkIsolation(t *testing.T) {
	full := tools.NewManager(tools.NewRegistry())
	registry := newChildRegistryForTests()
	orch := newOrchestrator(config.Orchestration{Enabled: true}, registry, nil, full)

	sink1 := &hardeningSink{}
	orch.setSink(sink1)

	// Entry into run 1: rootScope captures sink1 and generates an execution ID.
	snap := &runSnapshot{agent: nil}
	scope1 := orch.rootScope(context.Background(), snap)
	if scope1.sink != sink1 {
		t.Fatalf("scope1.sink = %v, want sink1 %v", scope1.sink, sink1)
	}
	if scope1.execID == "" {
		t.Fatal("scope1.execID must not be empty")
	}

	// Child scope is spawned under run 1.
	childScope1 := scope1.childScope("researcher")
	if childScope1.sink != sink1 {
		t.Fatalf("childScope1.sink = %v, want sink1 %v", childScope1.sink, sink1)
	}
	if childScope1.execID != scope1.execID {
		t.Fatalf("childScope1.execID = %q, want %q", childScope1.execID, scope1.execID)
	}

	// Session reset / new run: orchestrator sink is swapped to sink2.
	sink2 := &hardeningSink{}
	orch.setSink(sink2)

	// Run 2 enters and captures sink2.
	scope2 := orch.rootScope(context.Background(), snap)
	if scope2.sink != sink2 {
		t.Fatalf("scope2.sink = %v, want sink2 %v", scope2.sink, sink2)
	}
	if scope2.execID == scope1.execID {
		t.Fatalf("scope2.execID must be unique, got duplicate %q", scope2.execID)
	}

	// Child from run 1 now emits completion during unwinding.
	ctx := withOrchestrationScope(context.Background(), childScope1)
	orch.emitChildRun(ctx, childScope1, orchevents.Event{
		Type:       orchevents.ChildCompleted,
		Agent:      "researcher",
		ChildRunID: "child-r1",
		CallID:     "call-1",
		Outcome:    orchevents.OutcomeCancelled,
	})

	// Verify sink1 received the event with scope1.execID.
	events1 := sink1.events
	if len(events1) != 1 {
		t.Fatalf("sink1 received %d events, want 1", len(events1))
	}
	if events1[0].RunID != scope1.execID {
		t.Fatalf("emitted event RunID = %q, want %q", events1[0].RunID, scope1.execID)
	}

	// Crucial: sink2 MUST NOT receive the event from run 1's child!
	events2 := sink2.events
	if len(events2) != 0 {
		t.Fatalf("sink2 was polluted by run 1's unwinding child: %+v", events2)
	}
}

type bareSinkWrapper struct {
	s orchevents.Sink
}

func (b *bareSinkWrapper) Emit(e orchevents.Event) {
	if b.s != nil {
		b.s.Emit(e)
	}
}

type scopedSinkWrapper struct {
	bareSinkWrapper
	id string
}

func (s *scopedSinkWrapper) RunID() string {
	return s.id
}

// TestOrchestrationScope_WrappedSinkRunAttribution verifies Finding F4:
// 1. Bare Writer preserves its RunID in rootScope.
// 2. Wrapper implementing only Sink uses explicit runtime execution identity.
// 3. Wrapper implementing RunScopedSink exposes run identity to rootScope.
// 4. Mismatched RunID is rejected by Writer.Emit.
// 5. Empty RunID is stamped by Writer.Emit with writer's RunID.
func TestOrchestrationScope_WrappedSinkRunAttribution(t *testing.T) {
	full := tools.NewManager(tools.NewRegistry())
	registry := newChildRegistryForTests()
	orch := newOrchestrator(config.Orchestration{Enabled: true}, registry, nil, full)
	snap := &runSnapshot{agent: nil}

	// 1. Bare Writer
	path1 := filepath.Join(t.TempDir(), "bare.jsonl")
	w1, err := orchevents.NewWriter(path1, "run-bare-123")
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w1.Close()

	orch.setSink(w1)
	scope1 := orch.rootScope(context.Background(), snap)
	if scope1.execID != "run-bare-123" {
		t.Fatalf("bare writer: scope.execID = %q, want run-bare-123", scope1.execID)
	}

	// 2. Wrapper implementing only Sink + context-scoped explicit execution ID
	wrappedBare := &bareSinkWrapper{s: w1}
	orch.setSink(wrappedBare)
	ctxExplicit := WithExecutionID(context.Background(), "run-explicit-456")
	scope2 := orch.rootScope(ctxExplicit, snap)
	if scope2.execID != "run-explicit-456" {
		t.Fatalf("wrapper only + explicit ID: scope.execID = %q, want run-explicit-456", scope2.execID)
	}

	// 3. Wrapper explicitly exposing RunScopedSink
	scopedWrap := &scopedSinkWrapper{
		bareSinkWrapper: bareSinkWrapper{s: w1},
		id:              "run-scoped-789",
	}
	orch.setSink(scopedWrap)
	scope3 := orch.rootScope(context.Background(), snap)
	if scope3.execID != "run-scoped-789" {
		t.Fatalf("wrapper with RunScopedSink: scope.execID = %q, want run-scoped-789", scope3.execID)
	}

	// 4. Mismatched RunID rejection in Writer.Emit
	path4 := filepath.Join(t.TempDir(), "mismatch.jsonl")
	w4, err := orchevents.NewWriter(path4, "run-target")
	if err != nil {
		t.Fatalf("NewWriter 4: %v", err)
	}
	w4.Emit(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "run-different",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-1",
		ChildRunID: "child-1",
	})
	if err := w4.Close(); err != nil {
		t.Fatalf("Close 4: %v", err)
	}

	f4, err := os.Open(path4)
	if err != nil {
		t.Fatalf("Open 4: %v", err)
	}
	scanner4 := bufio.NewScanner(f4)
	mismatchedCount := 0
	for scanner4.Scan() {
		mismatchedCount++
	}
	f4.Close()
	if mismatchedCount != 0 {
		t.Fatalf("Writer.Emit persisted %d mismatched events, want 0", mismatchedCount)
	}

	// 5. Empty RunID behavior: Writer stamps its own RunID
	path5 := filepath.Join(t.TempDir(), "empty.jsonl")
	w5, err := orchevents.NewWriter(path5, "run-stamp-target")
	if err != nil {
		t.Fatalf("NewWriter 5: %v", err)
	}
	w5.Emit(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "call-1",
		ChildRunID: "child-1",
	})
	if err := w5.Close(); err != nil {
		t.Fatalf("Close 5: %v", err)
	}

	f5, err := os.Open(path5)
	if err != nil {
		t.Fatalf("Open 5: %v", err)
	}
	scanner5 := bufio.NewScanner(f5)
	var events5 []orchevents.Event
	for scanner5.Scan() {
		var ev orchevents.Event
		if err := json.Unmarshal(scanner5.Bytes(), &ev); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		events5 = append(events5, ev)
	}
	f5.Close()
	if len(events5) != 1 {
		t.Fatalf("expected 1 stamped event, got %d", len(events5))
	}
	if events5[0].RunID != "run-stamp-target" {
		t.Fatalf("stamped event RunID = %q, want run-stamp-target", events5[0].RunID)
	}
}

// TestOrchestrationScope_InvalidIdentityOrMissingScope verifies Finding F5:
// Orchestrator never emits without a valid execution identity or when scope/sink is missing.
func TestOrchestrationScope_InvalidIdentityOrMissingScope(t *testing.T) {
	full := tools.NewManager(tools.NewRegistry())
	registry := newChildRegistryForTests()
	orch := newOrchestrator(config.Orchestration{Enabled: true}, registry, nil, full)
	ctx := context.Background()

	ev := orchevents.Event{
		Type:  orchevents.DelegationRequested,
		Agent: "researcher",
	}

	// 1. Nil scope: no panic, no emission.
	orch.emitDelegation(ctx, nil, "researcher", ev)
	orch.emitChildRun(ctx, nil, ev)

	// 2. Nil sink: no panic, no emission.
	scopeNilSink := &orchestrationScope{
		execID: "run-valid",
		sink:   nil,
	}
	orch.emitDelegation(ctx, scopeNilSink, "researcher", ev)
	orch.emitChildRun(ctx, scopeNilSink, ev)

	// 3. Empty execID: must NEVER emit an orchestration event without valid identity.
	sink := &hardeningSink{}
	scopeEmptyID := &orchestrationScope{
		execID: "",
		sink:   sink,
		chain:  []string{"root"},
	}
	orch.emitDelegation(ctx, scopeEmptyID, "researcher", ev)
	orch.emitChildRun(ctx, scopeEmptyID, ev)
	if len(sink.events) != 0 {
		t.Fatalf("emitted %d events without valid execution identity, want 0", len(sink.events))
	}

	// 4. Valid scope: emits with execution identity preserved.
	scopeValid := &orchestrationScope{
		execID: "run-valid-999",
		sink:   sink,
		chain:  []string{"root"},
		depth:  1,
	}
	orch.emitDelegation(ctx, scopeValid, "researcher", ev)
	orch.emitChildRun(ctx, scopeValid, orchevents.Event{
		Type:       orchevents.ChildCompleted,
		Agent:      "researcher",
		ChildRunID: "child-999",
	})
	if len(sink.events) != 2 {
		t.Fatalf("valid scope: emitted %d events, want 2", len(sink.events))
	}
	if sink.events[0].RunID != "run-valid-999" || sink.events[1].RunID != "run-valid-999" {
		t.Fatalf("emitted events did not carry valid RunID: %+v", sink.events)
	}
}

// 1. Explicit execution identity on a single run.
func TestF4_ExplicitExecutionIdentitySingleRun(t *testing.T) {
	full := tools.NewManager(tools.NewRegistry())
	orch := newOrchestrator(config.Orchestration{Enabled: true}, newChildRegistryForTests(), nil, full)
	snap := &runSnapshot{agent: nil}

	ctx := WithExecutionID(context.Background(), "exec-single-run-001")
	scope := orch.rootScope(ctx, snap)
	if scope.execID != "exec-single-run-001" {
		t.Fatalf("scope.execID = %q, want exec-single-run-001", scope.execID)
	}
}

// 2. A second run on the same Runtime without an explicit identity.
func TestF4_SecondRunWithoutExplicitIdentity(t *testing.T) {
	full := tools.NewManager(tools.NewRegistry())
	orch := newOrchestrator(config.Orchestration{Enabled: true}, newChildRegistryForTests(), nil, full)
	snap := &runSnapshot{agent: nil}

	// First run sets explicit ID
	ctx1 := WithExecutionID(context.Background(), "run-explicit-first")
	scope1 := orch.rootScope(ctx1, snap)
	if scope1.execID != "run-explicit-first" {
		t.Fatalf("run 1: scope.execID = %q, want run-explicit-first", scope1.execID)
	}

	// Second run uses context.Background() on the SAME orchestrator
	ctx2 := context.Background()
	scope2 := orch.rootScope(ctx2, snap)
	if scope2.execID == "run-explicit-first" {
		t.Fatalf("run 2 leaked execution identity from run 1: %q", scope2.execID)
	}
	if scope2.execID == "" {
		t.Fatal("run 2 must generate a non-empty execution identity")
	}
}

// 3. Two sequential runs with different explicit identities.
func TestF4_SequentialRunsWithDifferentExplicitIdentities(t *testing.T) {
	full := tools.NewManager(tools.NewRegistry())
	orch := newOrchestrator(config.Orchestration{Enabled: true}, newChildRegistryForTests(), nil, full)
	snap := &runSnapshot{agent: nil}

	ctxA := WithExecutionID(context.Background(), "seq-run-alpha")
	scopeA := orch.rootScope(ctxA, snap)
	if scopeA.execID != "seq-run-alpha" {
		t.Fatalf("scopeA.execID = %q, want seq-run-alpha", scopeA.execID)
	}

	ctxB := WithExecutionID(context.Background(), "seq-run-beta")
	scopeB := orch.rootScope(ctxB, snap)
	if scopeB.execID != "seq-run-beta" {
		t.Fatalf("scopeB.execID = %q, want seq-run-beta", scopeB.execID)
	}

	// Third run has no explicit ID
	scopeC := orch.rootScope(context.Background(), snap)
	if scopeC.execID == "seq-run-alpha" || scopeC.execID == "seq-run-beta" || scopeC.execID == "" {
		t.Fatalf("scopeC has unexpected execID: %q", scopeC.execID)
	}
}

// 4. Two concurrent runs with different explicit identities.
func TestF4_ConcurrentRunsWithDifferentExplicitIdentities(t *testing.T) {
	full := tools.NewManager(tools.NewRegistry())
	orch := newOrchestrator(config.Orchestration{Enabled: true}, newChildRegistryForTests(), nil, full)
	snap := &runSnapshot{agent: nil}

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)

	startBarrier := make(chan struct{})
	results := make([]string, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			id := fmt.Sprintf("concurrent-run-%d", idx)
			ctx := WithExecutionID(context.Background(), id)
			// Wait for barrier release so all goroutines call rootScope concurrently
			<-startBarrier
			scope := orch.rootScope(ctx, snap)
			results[idx] = scope.execID
		}(i)
	}

	// Release all goroutines simultaneously
	close(startBarrier)
	wg.Wait()

	// Assert every run received exactly its own explicit identity without collision
	for i := 0; i < goroutines; i++ {
		want := fmt.Sprintf("concurrent-run-%d", i)
		if results[i] != want {
			t.Fatalf("goroutine %d got execID %q, want %q", i, results[i], want)
		}
	}
}

// 5. Wrapped Writer attribution (via RunScopedSink and WithExecutionID).
func TestF4_WrappedWriterAttribution(t *testing.T) {
	full := tools.NewManager(tools.NewRegistry())
	orch := newOrchestrator(config.Orchestration{Enabled: true}, newChildRegistryForTests(), nil, full)
	snap := &runSnapshot{agent: nil}

	path := filepath.Join(t.TempDir(), "writer_attr.jsonl")
	w, err := orchevents.NewWriter(path, "writer-target-id")
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	// Case A: Writer wrapped in bareSinkWrapper (implements only Sink),
	// but context carries WithExecutionID
	wrappedBare := &bareSinkWrapper{s: w}
	orch.setSink(wrappedBare)
	ctx := WithExecutionID(context.Background(), w.RunID())
	scopeA := orch.rootScope(ctx, snap)
	if scopeA.execID != "writer-target-id" {
		t.Fatalf("bare wrapper with context ID: got %q, want writer-target-id", scopeA.execID)
	}

	// Case B: Writer wrapped in scopedSinkWrapper (implements RunScopedSink)
	// without explicit context ID
	scopedWrap := &scopedSinkWrapper{
		bareSinkWrapper: bareSinkWrapper{s: w},
		id:              "scoped-wrapper-id",
	}
	orch.setSink(scopedWrap)
	scopeB := orch.rootScope(context.Background(), snap)
	if scopeB.execID != "scoped-wrapper-id" {
		t.Fatalf("RunScopedSink wrapper without context ID: got %q, want scoped-wrapper-id", scopeB.execID)
	}
}

// 6. Nested child identity inheritance.
func TestF4_NestedChildIdentityInheritance(t *testing.T) {
	full := tools.NewManager(tools.NewRegistry())
	orch := newOrchestrator(config.Orchestration{Enabled: true}, newChildRegistryForTests(), nil, full)
	sink := &hardeningSink{}
	orch.setSink(sink)
	snap := &runSnapshot{agent: nil}

	ctx := WithExecutionID(context.Background(), "parent-exec-007")
	root := orch.rootScope(ctx, snap)
	if root.execID != "parent-exec-007" {
		t.Fatalf("root.execID = %q, want parent-exec-007", root.execID)
	}

	// Depth 1 child inherits parent execID and sink
	child1 := root.childScope("agent-1")
	if child1.execID != "parent-exec-007" {
		t.Fatalf("child1.execID = %q, want parent-exec-007", child1.execID)
	}
	if child1.sink != sink || child1.depth != 1 {
		t.Fatalf("child1 sink=%v depth=%d", child1.sink, child1.depth)
	}

	// Depth 2 grandchild inherits parent execID and sink
	child2 := child1.childScope("agent-2")
	if child2.execID != "parent-exec-007" {
		t.Fatalf("child2.execID = %q, want parent-exec-007", child2.execID)
	}
	if child2.sink != sink || child2.depth != 2 {
		t.Fatalf("child2 sink=%v depth=%d", child2.sink, child2.depth)
	}
}

// 7. Missing identity behavior (empty string or whitespace).
func TestF4_MissingIdentityBehavior(t *testing.T) {
	full := tools.NewManager(tools.NewRegistry())
	orch := newOrchestrator(config.Orchestration{Enabled: true}, newChildRegistryForTests(), nil, full)
	snap := &runSnapshot{agent: nil}

	// Empty string in WithExecutionID
	ctxEmpty := WithExecutionID(context.Background(), "")
	scopeEmpty := orch.rootScope(ctxEmpty, snap)
	if scopeEmpty.execID == "" {
		t.Fatal("empty identity must generate a fresh non-empty UUID")
	}

	// Whitespace-only in WithExecutionID
	ctxWhitespace := WithExecutionID(context.Background(), "   \t\n  ")
	scopeWhitespace := orch.rootScope(ctxWhitespace, snap)
	if scopeWhitespace.execID == "" || strings.TrimSpace(scopeWhitespace.execID) == "" {
		t.Fatal("whitespace identity must generate a fresh non-empty UUID")
	}
}

// 8. Writer rejection of genuinely mismatched RunIDs.
func TestF4_WriterRejectionMismatchedRunIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writer_reject.jsonl")
	w, err := orchevents.NewWriter(path, "target-run-id")
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	// Mismatched RunID: must be dropped
	w.Emit(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "wrong-run-id",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "c1",
		ChildRunID: "ch1",
	})

	// Empty RunID: must be stamped with writer's RunID
	w.Emit(orchevents.Event{
		Type:       orchevents.AgentSpawned,
		RunID:      "",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "c2",
		ChildRunID: "ch2",
	})

	// Matching RunID: must be accepted
	w.Emit(orchevents.Event{
		Type:       orchevents.ChildCompleted,
		RunID:      "target-run-id",
		Agent:      "coder",
		Chain:      "root > coder",
		Depth:      1,
		CallID:     "c2",
		ChildRunID: "ch2",
		Outcome:    orchevents.OutcomeDone,
	})

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Read file: should have exactly 2 events (empty-stamped and matching), 0 mismatched
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	var events []orchevents.Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev orchevents.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		events = append(events, ev)
	}

	if len(events) != 2 {
		t.Fatalf("got %d events in log, want 2: %+v", len(events), events)
	}
	for i, ev := range events {
		if ev.RunID != "target-run-id" {
			t.Fatalf("event %d has RunID %q, want target-run-id", i, ev.RunID)
		}
	}
}
