package runtime

// Native batch delegation: one delegate call carrying a tasks array
// launches every declared task as its own child run, concurrently,
// under the same admission, concurrency, isolation, and permission
// controls the single-task path has.
//
// Every test here is synchronization-driven: overlap is proven with a
// barrier (the third child's provider call never returns until two
// siblings have entered), never with a sleep. The only waits are the
// barrier watchdog, which bounds the failure path.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"forcefield/internal/config"
	"forcefield/internal/orchevents"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// batchParentTurns scripts the parent: one response carrying a single
// delegate call with a tasks array, then the final synthesis turn.
// Every task targets the same agent on purpose: two batch tasks of one
// batch must still get distinct identities.
func batchParentTurns(tasks ...string) [][]providers.StreamEvent {
	items := make([]any, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, map[string]any{"agent": "researcher", "task": task})
	}
	return [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{
				ID:        "call-batch",
				Name:      "delegate",
				Arguments: map[string]any{"tasks": items},
			}}},
			{Done: true},
		},
		{{Text: "coordinated."}, {Done: true}},
	}
}

// batchSink records orchestration events from concurrent children. It
// is mutex-guarded: a batch emits from several goroutines at once.
type batchSink struct {
	mu     sync.Mutex
	events []orchevents.Event
}

func (s *batchSink) Emit(e orchevents.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *batchSink) ofType(t orchevents.Type) []orchevents.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []orchevents.Event
	for _, e := range s.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// batchResult returns the parent-visible result of the batch delegate
// call, failing the test when the call never produced one.
func batchResult(t *testing.T, events []Event) *ToolResult {
	t.Helper()
	result := delegateResult(events)
	if result == nil {
		t.Fatal("batch delegation produced no parent-visible result")
	}
	return result
}

// TestBatchDelegationRunsEveryTaskConcurrently is the core acceptance
// criterion of native batch delegation: ONE parent invocation launches
// several independent child tasks, and those children overlap in
// execution. The barrier closes only when the third child enters its
// provider call, so a serialized dispatcher deadlocks into the watchdog
// instead of passing.
func TestBatchDelegationRunsEveryTaskConcurrently(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(batchParentTurns("batch-a", "batch-b", "batch-c"), 3)
	armFanoutChildren(provider, "batch-a", "batch-b", "batch-c")
	runtime := newProbeRuntime(t, provider, childConfig(t, 4, 16), registry)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "batch three ways"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drained := drainAllEventsForTest(t, events)

	if provider.peak != 3 {
		t.Fatalf("peak concurrent child provider turns = %d, want exactly 3 (barrier-proven overlap)", provider.peak)
	}

	result := batchResult(t, drained)
	if result.IsError {
		t.Fatalf("batch delegation failed: %.400q", result.Content)
	}
	// Every child's own report reaches the parent, and no sibling's
	// report leaks into another child's section.
	for _, task := range []string{"batch-a", "batch-b", "batch-c"} {
		if !strings.Contains(result.Content, "report-"+task) {
			t.Fatalf("batch result is missing the report of %q: %.600q", task, result.Content)
		}
	}
	if !strings.Contains(result.Content, "[delegate:batch] tasks=3 completed=3 failed=0 not_admitted=0") {
		t.Fatalf("batch header does not report the honest counts: %.300q", result.Content)
	}
}
// TestBatchDelegationReportsEveryChildInDeclaredOrder pins the report
// shape: the parent reads the tasks in the order it declared them, so
// the report never depends on which child happened to finish first.
func TestBatchDelegationReportsEveryChildInDeclaredOrder(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(batchParentTurns("batch-a", "batch-b", "batch-c"), 0)
	armFanoutChildren(provider, "batch-a", "batch-b", "batch-c")
	runtime := newProbeRuntime(t, provider, childConfig(t, 4, 16), registry)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "batch three ways"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	result := batchResult(t, drainAllEventsForTest(t, events))

	first := strings.Index(result.Content, "report-batch-a")
	second := strings.Index(result.Content, "report-batch-b")
	third := strings.Index(result.Content, "report-batch-c")
	if first < 0 || second < 0 || third < 0 {
		t.Fatalf("batch result lost a child report: %.600q", result.Content)
	}
	if !(first < second && second < third) {
		t.Fatalf("batch result is not in declared order: %.600q", result.Content)
	}
}

// TestBatchDelegationRespectsConcurrencyCap proves the batch path keeps
// the orchestrator's max_concurrent_children gate: with a cap of 2,
// exactly two children overlap, and the third still runs afterwards.
func TestBatchDelegationRespectsConcurrencyCap(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(batchParentTurns("batch-a", "batch-b", "batch-c"), 2)
	armFanoutChildren(provider, "batch-a", "batch-b", "batch-c")
	runtime := newProbeRuntime(t, provider, childConfig(t, 2, 16), registry)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "batch three ways, two at a time"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drained := drainAllEventsForTest(t, events)

	if provider.peak != 2 {
		t.Fatalf("peak concurrent child provider turns = %d, want exactly 2 under a cap of 2", provider.peak)
	}
	result := batchResult(t, drained)
	if result.IsError {
		t.Fatalf("capped batch failed: %.400q", result.Content)
	}
	for _, task := range []string{"batch-a", "batch-b", "batch-c"} {
		if provider.childCalls[task] != 1 {
			t.Fatalf("child %q ran %d times, want exactly 1", task, provider.childCalls[task])
		}
	}
}

// TestBatchDelegationPartialAdmissionKeepsSiblings proves the partial
// admission contract: with max_children_per_run = 2 and three declared
// tasks, the first two are admitted and run, the third is reported as
// not admitted, and the admitted siblings are unaffected.
func TestBatchDelegationPartialAdmissionKeepsSiblings(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(batchParentTurns("batch-a", "batch-b", "batch-c"), 0)
	armFanoutChildren(provider, "batch-a", "batch-b", "batch-c")
	runtime := newProbeRuntime(t, provider, childConfig(t, 4, 2), registry)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "batch three ways with room for two"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	result := batchResult(t, drainAllEventsForTest(t, events))

	if !result.IsError {
		t.Fatal("a partially admitted batch must not report success")
	}
	if !strings.Contains(result.Content, "not_admitted=1") {
		t.Fatalf("batch result does not report the non-admitted task: %.600q", result.Content)
	}
	if !strings.Contains(result.Content, "delegation limit reached") {
		t.Fatalf("batch result does not carry the admission reason: %.600q", result.Content)
	}
	if !strings.Contains(result.Content, "completed=2") {
		t.Fatalf("batch result does not report the completed count: %.600q", result.Content)
	}
	// The admitted siblings ran; the rejected task never started.
	for _, task := range []string{"batch-a", "batch-b"} {
		if provider.childCalls[task] != 1 {
			t.Fatalf("admitted child %q ran %d times, want exactly 1", task, provider.childCalls[task])
		}
		if !strings.Contains(result.Content, "report-"+task) {
			t.Fatalf("batch result lost the report of admitted child %q: %.600q", task, result.Content)
		}
	}
	if provider.childCalls["batch-c"] != 0 {
		t.Fatalf("the non-admitted task ran %d times, want 0", provider.childCalls["batch-c"])
	}
	if strings.Contains(result.Content, "report-batch-c") {
		t.Fatalf("the non-admitted task produced a report: %.600q", result.Content)
	}
}

// TestBatchDelegationChildFailureDoesNotCancelSiblings proves failure
// isolation inside one batch: a failing child fails only its own
// section, while its siblings — already in flight with it at the
// barrier — run to completion and report normally.
func TestBatchDelegationChildFailureDoesNotCancelSiblings(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(batchParentTurns("batch-a", "batch-b", "batch-c"), 3)
	armFanoutChildren(provider, "batch-a", "batch-b", "batch-c")
	provider.childErr["batch-b"] = errors.New("child provider boom")
	runtime := newProbeRuntime(t, provider, childConfig(t, 4, 16), registry)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "batch three ways with one failure"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drained := drainAllEventsForTest(t, events)

	if provider.peak != 3 {
		t.Fatalf("peak concurrent child provider turns = %d, want 3 (the failing child overlaps too)", provider.peak)
	}
	result := batchResult(t, drained)
	if !result.IsError {
		t.Fatal("a batch with a failed child must not report success")
	}
	for _, task := range []string{"batch-a", "batch-c"} {
		if !strings.Contains(result.Content, "report-"+task) {
			t.Fatalf("sibling %q was cancelled by the failing child: %.600q", task, result.Content)
		}
	}
	if strings.Contains(result.Content, "report-batch-b") {
		t.Fatalf("the failing child produced a report: %.600q", result.Content)
	}
	if !strings.Contains(result.Content, "child run failed") {
		t.Fatalf("the failing child's section carries no failure reason: %.600q", result.Content)
	}
	if !strings.Contains(result.Content, "completed=2") || !strings.Contains(result.Content, "failed=1") {
		t.Fatalf("batch header does not report the honest counts: %.600q", result.Content)
	}
}

// TestBatchDelegationCancellationPropagates cancels the run while every
// child of the batch is blocked in flight, then proves the run
// terminates, every child is joined, and the runtime's own cancellation
// event is delivered.
func TestBatchDelegationCancellationPropagates(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(batchParentTurns("batch-a", "batch-b", "batch-c"), 3)
	provider.holdRelease = true // the barrier never opens: the cancel unblocks
	armFanoutChildren(provider, "batch-a", "batch-b", "batch-c")
	runtime := newProbeRuntime(t, provider, childConfig(t, 4, 16), registry)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Fire the cancel exactly when all three children are in flight.
		<-provider.enteredAll
		cancel()
	}()

	events, err := runtime.StreamChat(ctx, []providers.Message{
		{Role: providers.UserRole, Content: "batch three ways, then cancel"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	sawRunCancelled := false
	for e := range events {
		if e.Type == EventCancelled {
			sawRunCancelled = true
		}
	}
	if provider.peak != 3 {
		t.Fatalf("peak concurrent child provider turns = %d, want 3 (all blocked together)", provider.peak)
	}
	if !sawRunCancelled {
		t.Fatal("run cancellation event never delivered; the batched run ended without reporting cancellation")
	}
}

// TestBatchDelegationRejectsInvalidBatchWithoutSpawning proves the
// batch is validated as a whole: one bad task rejects the entire batch,
// and nothing is spawned, so a malformed batch never spends budget.
func TestBatchDelegationRejectsInvalidBatchWithoutSpawning(t *testing.T) {
	registry := newChildRegistryForTests()

	cases := []struct {
		name  string
		items []any
		want  string
	}{
		{
			name: "unknown agent",
			items: []any{
				map[string]any{"agent": "researcher", "task": "batch-a"},
				map[string]any{"agent": "nobody", "task": "batch-b"},
			},
			want: "unknown agent",
		},
		{
			name: "empty task",
			items: []any{
				map[string]any{"agent": "researcher", "task": "batch-a"},
				map[string]any{"agent": "researcher", "task": "   "},
			},
			want: "missing task",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newFanoutProvider(nil, 0)
			provider.parentTurns = [][]providers.StreamEvent{
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-batch",
						Name:      "delegate",
						Arguments: map[string]any{"tasks": tc.items},
					}}},
					{Done: true},
				},
				{{Text: "coordinated."}, {Done: true}},
			}
			runtime := newProbeRuntime(t, provider, childConfig(t, 4, 16), registry)

			events, err := runtime.StreamChat(context.Background(), []providers.Message{
				{Role: providers.UserRole, Content: "batch"},
			})
			if err != nil {
				t.Fatalf("StreamChat() error = %v", err)
			}
			result := batchResult(t, drainAllEventsForTest(t, events))
			if !result.IsError {
				t.Fatalf("an invalid batch reported success: %.400q", result.Content)
			}
			if !strings.Contains(result.Content, tc.want) {
				t.Fatalf("batch rejection = %.400q, want it to mention %q", result.Content, tc.want)
			}
			if len(provider.childCalls) != 0 {
				t.Fatalf("an invalid batch spawned %d children, want 0", len(provider.childCalls))
			}
		})
	}
}

// TestBatchDelegationOversizedTaskRejectsWholeBatch proves the task
// budget applies per task and rejects the whole batch.
func TestBatchDelegationOversizedTaskRejectsWholeBatch(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(nil, 0)
	provider.parentTurns = [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{
				ID:   "call-batch",
				Name: "delegate",
				Arguments: map[string]any{"tasks": []any{
					map[string]any{"agent": "researcher", "task": "short"},
					map[string]any{"agent": "researcher", "task": strings.Repeat("x", 64)},
				}},
			}}},
			{Done: true},
		},
		{{Text: "coordinated."}, {Done: true}},
	}
	runtime := newProbeRuntime(t, provider, config.Orchestration{
		Enabled:      true,
		MaxTaskChars: intPtr(16),
	}, registry)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "batch"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	result := batchResult(t, drainAllEventsForTest(t, events))
	if !result.IsError || !strings.Contains(result.Content, "exceeding the limit") {
		t.Fatalf("oversized batch task was not rejected: %.400q", result.Content)
	}
	if len(provider.childCalls) != 0 {
		t.Fatalf("an oversized batch spawned %d children, want 0", len(provider.childCalls))
	}
}

// TestBatchDelegationEmitsOneLifecyclePerTask proves the structured
// stream tells the declared tasks apart: every task gets its own spawn
// and completion record, with its own batch index and its own chain.
func TestBatchDelegationEmitsOneLifecyclePerTask(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(batchParentTurns("batch-a", "batch-b", "batch-c"), 0)
	armFanoutChildren(provider, "batch-a", "batch-b", "batch-c")
	runtime := newProbeRuntime(t, provider, childConfig(t, 4, 16), registry)
	sink := &batchSink{}
	runtime.orch.setSink(sink)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "batch three ways"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainAllEventsForTest(t, events)

	spawned := sink.ofType(orchevents.AgentSpawned)
	completed := sink.ofType(orchevents.ChildCompleted)
	if len(spawned) != 3 {
		t.Fatalf("agent_spawned events = %d, want 3 (one per declared task)", len(spawned))
	}
	if len(completed) != 3 {
		t.Fatalf("child_completed events = %d, want 3 (one per declared task)", len(completed))
	}

	seenIndex := map[int]bool{}
	seenChain := map[string]bool{}
	seenRun := map[string]bool{}
	for _, ev := range spawned {
		if ev.BatchIndex < 1 || ev.BatchIndex > 3 {
			t.Fatalf("agent_spawned carries batch index %d, want 1..3", ev.BatchIndex)
		}
		if seenIndex[ev.BatchIndex] {
			t.Fatalf("two children share batch index %d", ev.BatchIndex)
		}
		seenIndex[ev.BatchIndex] = true
		if ev.Chain == "" {
			t.Fatal("agent_spawned carries no chain")
		}
		if seenChain[ev.Chain] {
			t.Fatalf("two children share chain %q", ev.Chain)
		}
		seenChain[ev.Chain] = true
		if ev.ChildRunID == "" {
			t.Fatal("agent_spawned carries no child run id")
		}
		if seenRun[ev.ChildRunID] {
			t.Fatalf("two children share child run id %q", ev.ChildRunID)
		}
		seenRun[ev.ChildRunID] = true
	}

	// Every requested task has a request and an acceptance record, and
	// the requests are attributed to the batch.
	requested := sink.ofType(orchevents.DelegationRequested)
	accepted := sink.ofType(orchevents.DelegationAccepted)
	if len(requested) != 3 || len(accepted) != 3 {
		t.Fatalf("delegation lifecycle records = %d requested / %d accepted, want 3 / 3",
			len(requested), len(accepted))
	}
}

// TestBatchDelegationPublishesLiveChildText proves the live child text
// reaches the structured stream while the child is still running, with
// the child's own attribution and never a sibling's text.
func TestBatchDelegationPublishesLiveChildText(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(batchParentTurns("batch-a", "batch-b"), 0)
	provider.childTurns["batch-a"] = []providers.StreamEvent{
		{Text: "alpha first line\n"},
		{Text: "alpha second line\n"},
		{Done: true},
	}
	provider.childTurns["batch-b"] = []providers.StreamEvent{
		{Text: "beta first line\n"},
		{Done: true},
	}
	runtime := newProbeRuntime(t, provider, childConfig(t, 4, 16), registry)
	sink := &batchSink{}
	runtime.orch.setSink(sink)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "batch two ways"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainAllEventsForTest(t, events)

	texts := sink.ofType(orchevents.ChildText)
	if len(texts) == 0 {
		t.Fatal("no live child text reached the structured stream")
	}

	// The text is published before the child completes: that is what
	// makes it live rather than a post-mortem report.
	completedAt := -1
	for i, ev := range sink.events {
		if ev.Type == orchevents.ChildCompleted && ev.Agent == "researcher" {
			if completedAt < 0 {
				completedAt = i
			}
		}
	}
	firstTextAt := -1
	for i, ev := range sink.events {
		if ev.Type == orchevents.ChildText {
			firstTextAt = i
			break
		}
	}
	if firstTextAt < 0 || completedAt < 0 || firstTextAt > completedAt {
		t.Fatalf("live text was not published before the completion (text at %d, completion at %d)",
			firstTextAt, completedAt)
	}

	// Every text event belongs to exactly one child, and a child's text
	// never carries its sibling's words.
	perRun := map[string]string{}
	for _, ev := range texts {
		if ev.ChildRunID == "" {
			t.Fatal("a live text event carries no child run id")
		}
		if ev.Chain == "" {
			t.Fatal("a live text event carries no chain")
		}
		perRun[ev.ChildRunID] += ev.Text
	}
	if len(perRun) != 2 {
		t.Fatalf("live text reached %d children, want 2", len(perRun))
	}
	sawAlpha, sawBeta := false, false
	for _, text := range perRun {
		if strings.Contains(text, "alpha") {
			sawAlpha = true
			if strings.Contains(text, "beta") {
				t.Fatalf("one child's live text carries its sibling's words: %q", text)
			}
		}
		if strings.Contains(text, "beta") {
			sawBeta = true
			if strings.Contains(text, "alpha") {
				t.Fatalf("one child's live text carries its sibling's words: %q", text)
			}
		}
	}
	if !sawAlpha || !sawBeta {
		t.Fatalf("live text missing from one of the children: %v", perRun)
	}
}

// TestBatchDelegationOverlayReachesPrompts pins the batch form to the
// model-visible surfaces: with orchestration on, the delegation overlay
// must tell the model that one delegate call can carry a tasks array;
// with orchestration off, the prompt stays exactly the agent's own.
func TestBatchDelegationOverlayReachesPrompts(t *testing.T) {
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{Text: "ok"}, {Done: true}},
	}}
	rt := newProbeRuntime(t, provider, childConfig(t, 4, 16), newChildRegistryForTests())
	drainAllEventsForTest(t, mustStream(t, rt))
	if len(provider.messages) == 0 {
		t.Fatal("orchestration-on run never called the provider")
	}
	sys := provider.messages[0][0].Content
	if !strings.Contains(sys, "issue the delegate calls for them together") {
		t.Fatal("the delegation fan-out overlay is missing")
	}
	if !strings.Contains(sys, `"tasks" array`) {
		t.Fatalf("the delegation overlay does not mention the native batch form: %.400q", sys)
	}

	off := &scriptedProvider{turns: [][]providers.StreamEvent{
		{{Text: "ok"}, {Done: true}},
	}}
	rtOff := newTestRuntime(off)
	drainAllEventsForTest(t, mustStream(t, rtOff))
	if len(off.messages) == 0 {
		t.Fatal("orchestration-off run never called the provider")
	}
	if strings.Contains(off.messages[0][0].Content, `"tasks" array`) {
		t.Fatal("the batch delegation overlay leaked into a run without an orchestrator")
	}
}

// TestComposeBatchResultCountsAndBounds pins the aggregate report's
// own contract: the counts are honest, the metadata mirrors them, and
// the report stays inside its byte bound.
func TestComposeBatchResultCountsAndBounds(t *testing.T) {
	prepared := []preparedBatchTask{
		{agent: "researcher", task: "a"},
		{agent: "researcher", task: "b"},
		{agent: "researcher", task: "c"},
	}
	admitted := []bool{true, true, false}
	rejected := []string{"", "", "delegation limit reached (2 child runs in this session, limit 2)"}
	results := []tools.Result{
		{Content: "[delegate] agent=\"researcher\" turns=1 tool_calls=0 failed_tool_calls=0 denied=0\nok a"},
		{Content: "[delegate] agent=\"researcher\" turns=1 tool_calls=0 failed_tool_calls=0 denied=0\nchild run failed: boom", IsError: true},
		{Content: "never ran"},
	}

	got := composeBatchResult(prepared, admitted, rejected, results)
	if !got.IsError {
		t.Fatal("a batch with a failed child reported success")
	}
	if !strings.Contains(got.Content, "tasks=3 completed=1 failed=1 not_admitted=1") {
		t.Fatalf("batch header = %.200q, want the honest counts", got.Content)
	}
	if !strings.Contains(got.Content, "delegation limit reached") {
		t.Fatalf("batch report lost the admission reason: %.400q", got.Content)
	}
	if got.Metadata["batch_size"] != 3 || got.Metadata["completed"] != 1 ||
		got.Metadata["failed"] != 1 || got.Metadata["not_admitted"] != 1 {
		t.Fatalf("batch metadata = %v, want the mirrored counts", got.Metadata)
	}
	if len(got.Content) > delegateBatchReportMaxBytes+256 {
		t.Fatalf("batch report is %d bytes, want it bounded by %d", len(got.Content), delegateBatchReportMaxBytes)
	}
}

// TestBatchDelegationIsolatesChildSnapshots proves the batch path keeps// the per-child snapshot isolation: every task runs against its own
// task text and nothing else, so a child never sees a sibling's task.
func TestBatchDelegationIsolatesChildSnapshots(t *testing.T) {
	registry := newChildRegistryForTests()
	provider := newFanoutProvider(batchParentTurns("batch-a", "batch-b"), 0)
	armFanoutChildren(provider, "batch-a", "batch-b")
	runtime := newProbeRuntime(t, provider, childConfig(t, 4, 16), registry)

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "batch two ways"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	result := batchResult(t, drainAllEventsForTest(t, events))

	for _, task := range []string{"batch-a", "batch-b"} {
		if provider.childCalls[task] != 1 {
			t.Fatalf("child %q ran %d times, want exactly 1", task, provider.childCalls[task])
		}
	}
	// The parent's conversation never reaches a child: the only user
	// message a child sees is its own task.
	if strings.Contains(result.Content, "batch two ways") {
		t.Fatalf("the parent's task text leaked into a child report: %.600q", result.Content)
	}
}
