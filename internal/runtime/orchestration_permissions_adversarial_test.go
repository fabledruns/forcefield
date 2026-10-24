package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// Adversarial permission-isolation tests. Every test here routes attacks
// through the real scheduler path (resolveCheck -> resolveAskWithManager ->
// sessionAllow) with a real rules manager and store: nothing is faked away.
// Duration bounds in these tests are deadlock detectors only, never the
// property being asserted.

// countingEchoTool is a minimal echo with an execution counter so tests can
// assert on actual tool executions, not on transcript text alone.
type countingEchoTool struct {
	id string
	mu sync.Mutex
	n  int
}

func (t *countingEchoTool) Name() string {
	return "echo"
}
func (t *countingEchoTool) Description() string { return "Counts executions." }
func (t *countingEchoTool) InputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"value": map[string]any{"type": "string"},
	}}
}
func (t *countingEchoTool) Execute(_ context.Context, args map[string]any) (tools.Result, error) {
	t.mu.Lock()
	t.n++
	t.mu.Unlock()
	if args == nil || args["value"] == nil {
		return tools.Result{}, nil
	}
	return tools.Result{Content: args["value"].(string)}, nil
}
func (t *countingEchoTool) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.n
}

// gatedAsker is a deterministic asker for serialization proofs: every ask
// parks on its own gate until the test opens it. The current-open counter
// is the discriminator: with a correct shared ask scope, a second ask can
// never be *entered* while the first is parked, so the counter can never
// reach 2 between a park and its resolution. Without the scope both worlds
// differ only by the observed counter, which is timing-free.
type gatedAsker struct {
	mu       sync.Mutex
	gates    []chan struct{}
	current  int
	overlap  atomic.Bool
	requests []permissions.Request
	answered permissions.Prompt
	// entered receives a signal for each asker entry, so the test can
	// observe arrivals without polling.
	entered chan struct{}
}

func newGatedAsker(prompt permissions.Prompt) *gatedAsker {
	return &gatedAsker{
		answered: prompt,
		entered:  make(chan struct{}, 64),
	}
}

func (a *gatedAsker) Ask(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
	a.mu.Lock()
	gate := make(chan struct{})
	a.gates = append(a.gates, gate)
	a.requests = append(a.requests, req)
	a.current++
	overlapNow := a.current > 1
	a.mu.Unlock()
	if overlapNow {
		a.overlap.Store(true)
	}
	select {
	case a.entered <- struct{}{}:
	default:
	}

	select {
	case <-gate:
	case <-ctx.Done():
		a.leave()
		return permissions.PromptDenyOnce, ctx.Err()
	}
	a.leave()
	return a.answered, nil
}

func (a *gatedAsker) leave() {
	a.mu.Lock()
	a.current--
	a.mu.Unlock()
}

// release opens the gate created by the i-th ask.
func (a *gatedAsker) release(i int) {
	a.mu.Lock()
	g := a.gates[i]
	a.mu.Unlock()
	close(g)
}

func (a *gatedAsker) askCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.requests)
}

func (a *gatedAsker) request(i int) permissions.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.requests[i]
}

// newPermissionAdversarialRuntime mirrors newPermissionsOrchestrationRuntime
// (identical production wiring shape) but registers countingEchoTool so
// executions are observable, and returns the tool for counting.
func newPermissionAdversarialRuntime(t *testing.T, p providers.ModelProvider, rules permissions.Rules, asker permissions.Asker, orchCfg config.Orchestration) (*Runtime, *recordingRulesStore, *countingEchoTool) {
	t.Helper()
	full := tools.NewManager(tools.NewRegistry())
	echo := &countingEchoTool{}
	if err := full.Register(echo); err != nil {
		t.Fatalf("register echo: %v", err)
	}
	registry := newChildRegistryForTests()
	orch := newOrchestrator(orchCfg, registry, nil, full)
	if err := full.Register(newDelegateTool(orch)); err != nil {
		t.Fatalf("register delegate: %v", err)
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
		scheduler:   newScheduler(full, manager, asker, DefaultSchedulerConfig),
	}
	orch.parent = r
	r.scheduler.askSerial = orch.childAskSerial()
	return r, store, echo
}

// denyAllRules keeps the shipped ask-by-default posture.
func askAllRules() permissions.Rules {
	return permissions.Rules{Default: permissions.Ask, Tools: map[string]permissions.Decision{}}
}

// delegateAskAllAllowRules lets the parent's own delegate calls through and
// keeps echo on ask so child isolation is exercised on every child turn.
func delegateAllowRules() permissions.Rules {
	return permissions.Rules{
		Default: permissions.Ask,
		Tools:   map[string]permissions.Decision{"delegate": permissions.Allow},
	}
}

func drainEventsForTest(t *testing.T, events <-chan Event) (toolResults map[string]*ToolResult, final *providers.Response) {
	t.Helper()
	toolResults = map[string]*ToolResult{}
	for ev := range events {
		if ev.ToolResult != nil && (ev.Type == EventToolFinish || ev.Type == EventToolFailed || ev.Type == EventToolDenied || ev.Type == EventToolCancelled) {
			toolResults[ev.ToolResult.ToolCallID] = ev.ToolResult
		}
		if ev.Type == EventDone {
			final = ev.Response
		}
	}
	return toolResults, final
}

// singleChildTurns scripts one delegate call with echo turns for the child.
func singleChildTurns(taskText string) [][]providers.StreamEvent {
	return [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{
				ID:   "call-delegate",
				Name: "delegate",
				Arguments: map[string]any{
					"agent": "researcher",
					"task":  taskText,
				},
			}}},
			{Done: true},
		},
		{
			{ToolCalls: []providers.ToolCall{{
				ID:        "call-child-echo-1",
				Name:      "echo",
				Arguments: map[string]any{"value": "child-1"},
			}}},
			{Done: true},
		},
		{
			{ToolCalls: []providers.ToolCall{{
				ID:        "call-child-echo-2",
				Name:      "echo",
				Arguments: map[string]any{"value": "child-2"},
			}}},
			{Done: true},
		},
		{
			{Text: "child done"},
			{Done: true},
		},
		{
			{Text: "parent coordinated."},
			{Done: true},
		},
	}
}

// TestAdversarialParentSessionApprovalCannotAuthorizeChild: the parent's
// session-scoped Always allow must reach only the parent scheduler. The
// child calling the same tool is refused; the parent retains its session
// approval; the child's refusal is visible in the delegated report.
func TestAdversarialParentSessionApprovalCannotAuthorizeChild(t *testing.T) {
	// Turn plan: parent echo (Always allow -> session), same echo again
	// (no ask needed, session approve), delegate, child echo (denied),
	// child final, parent echo again after child (still session-allowed).
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{
				ID:        "p-echo-1",
				Name:      "echo",
				Arguments: map[string]any{"value": "one"},
			}}},
			{Done: true},
		},
		{
			{ToolCalls: []providers.ToolCall{{
				ID:        "p-echo-2",
				Name:      "echo",
				Arguments: map[string]any{"value": "two"},
			}}},
			{Done: true},
		},
		{
			{ToolCalls: []providers.ToolCall{{
				ID:   "call-delegate",
				Name: "delegate",
				Arguments: map[string]any{
					"agent": "researcher",
					"task":  "echo like the parent did",
				},
			}}},
			{Done: true},
		},
		{
			{ToolCalls: []providers.ToolCall{{
				ID:        "call-child-echo",
				Name:      "echo",
				Arguments: map[string]any{"value": "child"},
			}}},
			{Done: true},
		},
		{
			{Text: "child done"},
			{Done: true},
		},
		{
			{ToolCalls: []providers.ToolCall{{
				ID:        "p-echo-3",
				Name:      "echo",
				Arguments: map[string]any{"value": "three"},
			}}},
			{Done: true},
		},
		{
			{Text: "parent done."},
			{Done: true},
		},
	}}
	// Always allow: gives the parent a session approval ("echo" key), and
	// would also have persisted globally in a buggy child path.
	asker := newRecordingAsker(permissions.PromptAlwaysAllow)
	r, store, echo := newPermissionAdversarialRuntime(
		t, provider, delegateAllowRules(), asker, config.Orchestration{Enabled: true})

	before := store.current()
	events, err := r.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "isolate the session"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	toolResults, final := drainEventsForTest(t, events)

	if final == nil {
		t.Fatal("run did not complete")
	}
	if got := echo.count(); got != 3 {
		t.Fatalf("parent echo executions = %d, want 3 (session approval reused twice)", got)
	}
	if asker.askCount() != 1 {
		t.Fatalf("asker prompted %d times, want exactly 1 (second echo reused the session approval)", asker.askCount())
	}

	// The child's echo call in the transcript is denied, not finished: the
	// delegate report's accounting is the honest instrument here, together
	// with the shared executor count (a child echo would push it to 4).
	delegate := toolResults["call-delegate"]
	if delegate == nil {
		t.Fatalf("delegate tool result missing from transcript: results=%v", toolResults)
	}
	if !strings.Contains(delegate.Content, "denied=1") {
		t.Fatalf("delegate report does not show the refused child call: %s", delegate.Content)
	}
	if !strings.Contains(delegate.Content, "(denied: echo)") {
		t.Fatalf("delegate report must name the refused tool: %s", delegate.Content)
	}
	// The child's own status is surfaced honestly (its run, its state),
	// not upgraded to the parent's success.
	if !strings.Contains(delegate.Content, "child status=partial") {
		t.Fatalf("child status must be reported verbatim: %s", delegate.Content)
	}

	// Permission state is byte-identical before and after the attack.
	after := store.current()
	if !rulesEqual(before, after) {
		t.Fatalf("persistent rules changed during the run: before=%+v after=%+v", before, after)
	}
	if store.saveCount() != 0 {
		t.Fatalf("store wrote %d times, want 0 (session decisions are never persisted)", store.saveCount())
	}
}

// TestAdversarialChildAlwaysAllowIsDowngradedToOnce: an interactive child
// answering "always allow" gives exactly one decision, never persistence.
// The second child call prompts again; the parent's own session state and
// global rules remain untouched.
func TestAdversarialChildAlwaysAllowIsDowngradedToOnce(t *testing.T) {
	provider := &scriptedProvider{turns: singleChildTurns("always-allow me")}
	asker := newRecordingAsker(permissions.PromptAlwaysAllow)
	r, store, echo := newPermissionAdversarialRuntime(
		t, provider, delegateAllowRules(), asker, config.Orchestration{Enabled: true, ChildPermissions: config.ChildPermissionsInteractive})

	before := store.current()
	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "try to escalate",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	_, final := drainEventsForTest(t, events)
	if final == nil {
		t.Fatal("run did not complete")
	}

	// The child made TWO askable echo calls: both were answered once, and
	// the downgrade made nothing persistent: otherwise the second would
	// not prompt again.
	if asker.askCount() != 2 {
		t.Fatalf("asker prompted %d times, want 2 (downgraded 'always' must prompt again)", asker.askCount())
	}
	if got := echo.count(); got != 2 {
		t.Fatalf("child echo executions = %d, want 2", got)
	}
	after := store.current()
	if !rulesEqual(before, after) {
		t.Fatalf("child 'always allow' mutated persistent rules: before=%+v after=%+v", before, after)
	}
	if store.saveCount() != 0 {
		t.Fatalf("child answers reached the persistent store %d times", store.saveCount())
	}
}

// rulesEqual compares canonical Rules snapshots. It is deliberately
// explicit (not reflect.DeepEqual on the whole struct) so a stale map entry
// cannot hide in an alias and defeat the byte-identity claim.
func rulesEqual(a, b permissions.Rules) bool {
	if a.Default != b.Default {
		return false
	}
	if len(a.Tools) != len(b.Tools) {
		return false
	}
	for k, v := range a.Tools {
		if b.Tools[k] != v {
			return false
		}
	}
	return true
}

// TestAdversarialChildAlwaysDenyPersistsNothingAndCannotDenyParent: the
// child answers "always deny"; the deny lives only inside the child's own
// session map, so the parent (and the persistent rules) are unaffected.
func TestAdversarialChildAlwaysDenyPersistsNothingAndCannotDenyParent(t *testing.T) {
	provider := &scriptedProvider{turns: [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{
				ID:   "call-delegate",
				Name: "delegate",
				Arguments: map[string]any{
					"agent": "researcher",
					"task":  "deny everything",
				},
			}}},
			{Done: true},
		},
		{
			{ToolCalls: []providers.ToolCall{{
				ID:        "call-child-echo",
				Name:      "echo",
				Arguments: map[string]any{"value": "child"},
			}}},
			{Done: true},
		},
		{
			{Text: "child blocked out"},
			{Done: true},
		},
		{ // the parent then uses echo itself: must still prompt normally
			{ToolCalls: []providers.ToolCall{{
				ID:        "p-echo",
				Name:      "echo",
				Arguments: map[string]any{"value": "parent"},
			}}},
			{Done: true},
		},
		{
			{Text: "parent done."},
			{Done: true},
		},
	}}
	// The asker branches on origin: child asks (stamped origin) are met
	// with the hostile "always deny"; the parent's own asks (nil origin)
	// get a normal one-time allow. This is also a direct check that the
	// origin stamp reaches the surface where isolation swings on it.
	asker := newCountingAsker(func(req permissions.Request) (permissions.Prompt, error) {
		if req.Origin != nil {
			return permissions.PromptAlwaysDeny, nil
		}
		return permissions.PromptAllowOnce, nil
	})
	r, store, echo := newPermissionAdversarialRuntime(
		t, provider, delegateAllowRules(), asker, config.Orchestration{Enabled: true, ChildPermissions: config.ChildPermissionsInteractive})

	beforeRules := store.current()
	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "child tries a global deny",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	results, _ := drainEventsForTest(t, events)

	// Child saw exactly one ask (its own echo) and could not persist a
	// deny, global or session-reachable-from-parent.
	if asker.askCount() != 2 {
		t.Fatalf("asker prompted %d times, want 2 (child echo + parent echo); a persisted child deny would suppress the parent prompt", asker.askCount())
	}
	if got := echo.count(); got != 1 {
		t.Fatalf("tool executions = %d, want 1 (only the parent's own echo after the child denial)", got)
	}
	child := results["call-delegate"]
	if child == nil || !strings.Contains(child.Content, "denied=1") {
		t.Fatalf("child denial not reflected in report: %+v", child)
	}
	after := store.current()
	if !rulesEqual(beforeRules, after) {
		t.Fatalf("child 'always deny' mutated persistent rules: before=%+v after=%+v", beforeRules, after)
	}
	if store.saveCount() != 0 {
		t.Fatalf("child 'always deny' wrote to the persistent store")
	}
	// The recorded child ask carried the origin stamp (that is what the
	// asker's deny branch keyed on), and the parent branch saw none.
	foundChildOrigin := false
	foundParentNilOrigin := false
	for _, req := range asker.seen() {
		if req.Origin != nil && req.Origin.Agent == "researcher" {
			foundChildOrigin = true
		}
		if req.Origin == nil {
			foundParentNilOrigin = true
		}
	}
	if !foundChildOrigin {
		t.Fatal("no stamped child ask reached the per-branch asker; child answers would be indistinguishable from the parent's")
	}
	if !foundParentNilOrigin {
		t.Fatal("parent ask did not appear with a nil origin")
	}
}

// TestAdversarialDenyModeFailsClosedOnEveryAsk: the shipped default. Child
// asks are refused by the built-in deny asker: no operator surface is ever
// touched, nothing is persisted, and the refusals are honestly reported.
func TestAdversarialDenyModeFailsClosedOnEveryAsk(t *testing.T) {
	provider := &scriptedProvider{turns: singleChildTurns("no prompts for you")}
	// The operator asker counts every real interaction; a child prompt
	// leaking into it would bump the count.
	asker := newRecordingAsker(permissions.PromptAllowOnce)
	r, store, echo := newPermissionAdversarialRuntime(
		t, provider, delegateAllowRules(), asker, config.Orchestration{Enabled: true})

	beforeRules := store.current()
	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "deny mode baseline",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	results, _ := drainEventsForTest(t, events)

	if got := echo.count(); got != 0 {
		t.Fatalf("child tool executed %d times in deny mode, want 0", got)
	}
	if asker.askCount() != 0 {
		t.Fatalf("deny-mode child opened %d operator prompts, want 0", asker.askCount())
	}
	if store.saveCount() != 0 {
		t.Fatalf("deny mode persisted state: %d store writes", store.saveCount())
	}
	if !rulesEqual(beforeRules, store.current()) {
		t.Fatalf("deny mode changed persistent rules: before=%+v after=%+v", beforeRules, store.current())
	}
	child := results["call-delegate"]
	if child == nil {
		t.Fatalf("delegate tool result missing: %+v", child)
	}
	if !strings.Contains(child.Content, "denied=2") {
		t.Fatalf("both child echoes must be refused and counted: %s", child.Content)
	}
	if !strings.Contains(child.Content, "(denied: echo)") {
		t.Fatalf("denial names must be listed: %s", child.Content)
	}
}

// TestAdversarialInteractiveAskCarriesDelegationChain: operator asks from
// children are stamped with the exact delegation chain (root first). Parent
// asks carry no fabricated origin.
func TestAdversarialInteractiveAskCarriesDelegationChain(t *testing.T) {
	// maxDepth 2 so the chain has two levels; the grandchild asks too.
	provider := &keyedProvider{
		parentTurns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{{
					ID:   "call-delegate",
					Name: "delegate",
					Arguments: map[string]any{
						"agent": "researcher",
						"task":  "nested attribution",
					},
				}}},
				{Done: true},
			},
			{
				{Text: "parent done."},
				{Done: true},
			},
		},
		childTurns: map[string][][]providers.StreamEvent{
			"nested attribution": {
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-child-echo",
						Name:      "echo",
						Arguments: map[string]any{"value": "c1"},
					}}},
					{Done: true},
				},
				{
					{ToolCalls: []providers.ToolCall{{
						ID:   "call-grandchild-delegate",
						Name: "delegate",
						Arguments: map[string]any{
							"agent": "researcher",
							"task":  "grandchild attribution",
						},
					}}},
					{Done: true},
				},
				{
					{Text: "child done"},
					{Done: true},
				},
			},
			"grandchild attribution": {
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-grandchild-echo",
						Name:      "echo",
						Arguments: map[string]any{"value": "gc"},
					}}},
					{Done: true},
				},
				{
					{Text: "grandchild done"},
					{Done: true},
				},
			},
		},
	}
	asker := newRecordingAsker(permissions.PromptAllowOnce)
	// delegate passes through the real permission path without prompting;
	// echo stays ask-all so both child/grandchild levels ask.
	r, _, _ := newPermissionAdversarialRuntime(
		t, provider, delegateAllowRules(), asker,
		config.Orchestration{Enabled: true, MaxDepth: intPtr(2), ChildPermissions: config.ChildPermissionsInteractive})

	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "attribute me",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	_, final := drainEventsForTest(t, events)
	if final == nil {
		t.Fatal("run did not complete")
	}

	if asker.askCount() != 2 {
		t.Fatalf("expected 2 child asks (child and grandchild), got %d", asker.askCount())
	}
	_, ok1 := asker.lastAsk()
	if !ok1 {
		t.Fatal("no asks recorded")
	}
	first := asker.asks[0]
	second := asker.asks[1]
	if first.Origin == nil {
		t.Fatal("child ask reached the operator without an Origin stamp; attribution is mandatory in interactive mode")
	}
	if first.Origin.Agent != "researcher" || first.Origin.Chain != "root > researcher" {
		t.Fatalf("child origin = %+v, want agent %q chain %q", first.Origin, "researcher", "root > researcher")
	}
	if second.Origin == nil {
		t.Fatal("grandchild ask arrived without origin")
	}
	if second.Origin.Chain != "root > researcher > researcher" {
		t.Fatalf("grandchild chain = %q, want the full path", second.Origin.Chain)
	}
}

// TestAdversarialChildFailClosedWithoutOperatorAsker: with no operator asker
// configured at all, interactive child mode must still fail closed, not
// panic or fail open.
func TestAdversarialChildFailClosedWithoutOperatorAsker(t *testing.T) {
	provider := &scriptedProvider{turns: singleChildTurns("no asker available")}
	r, store, echo := newPermissionAdversarialRuntime(
		t, provider, delegateAllowRules(), nil,
		config.Orchestration{Enabled: true, ChildPermissions: config.ChildPermissionsInteractive})

	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "headless child asks",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	results, final := drainEventsForTest(t, events)
	if final == nil {
		t.Fatal("run did not complete")
	}

	// The parent's delegate tool call itself required no prompt (allow
	// rule), but every child echo must have been refused without prompts.
	if got := echo.count(); got != 0 {
		t.Fatalf("child executed %d tools despite fail-closed asker absence", got)
	}
	if store.saveCount() != 0 {
		t.Fatal("fail-closed run wrote permission state")
	}
	child := results["call-delegate"]
	if child != nil && strings.Contains(child.Content, "denied=0") {
		t.Fatalf("child report claims zero denials though the asker was absent: %s", child.Content)
	}
}

// TestAdversarialFailingAskerDeniesChild: an asker that errors is the same
// as no asker: fail closed, no panic, nothing persisted.
func TestAdversarialFailingAskerDeniesChild(t *testing.T) {
	provider := &scriptedProvider{turns: singleChildTurns("asker explodes")}
	asker := &failingAsker{}
	r, store, echo := newPermissionAdversarialRuntime(
		t, provider, delegateAllowRules(), asker,
		config.Orchestration{Enabled: true, ChildPermissions: config.ChildPermissionsInteractive})

	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "broken surface",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	_, final := drainEventsForTest(t, events)
	if final == nil {
		t.Fatal("run did not complete")
	}
	if got := echo.count(); got != 0 {
		t.Fatalf("child executed %d tools with a failing asker", got)
	}
	if store.saveCount() != 0 {
		t.Fatal("failing asker wrote persists")
	}
}

// failingAsker always errors, simulating a dead approval surface.
type failingAsker struct{}

func (failingAsker) Ask(_ context.Context, _ permissions.Request) (permissions.Prompt, error) {
	return permissions.PromptDenyOnce, errFailingAsker{}
}

type errFailingAsker struct{}

func (errFailingAsker) Error() string { return "asker unavailable" }

// TestAdversarialAskerSwapAfterSpawnDoesNotWidenInFlightChild: the child's
// asker is captured at spawn time. Swapping the parent's asker mid-run
// (performed here deterministically from inside the asker itself, right as
// the child's ask resolves) cannot re-route the in-flight child to the
// permissive surface.
func TestAdversarialAskerSwapAfterSpawnDoesNotWidenInFlightChild(t *testing.T) {
	provider := &scriptedProvider{turns: singleChildTurns("swap during ask")}
	widening := newRecordingAsker(permissions.PromptAlwaysAllow)
	r, _, echo := newPermissionAdversarialRuntime(
		t, provider, delegateAllowRules(), nil,
		config.Orchestration{Enabled: true, ChildPermissions: config.ChildPermissionsInteractive})
	// The meaningful asker is installed before the run starts: the child
	// captures it at spawn time. Its Ask denies the child and, while
	// answering, swaps the parent scheduler's surface to the permissive
	// asker. The in-flight child must not be re-routed to the widened
	// asker.
	swapper := chooseFunc(func(req permissions.Request) (permissions.Prompt, error) {
		r.scheduler.setAsker(widening)
		return permissions.PromptDenyOnce, nil
	})
	r.scheduler.setAsker(swapper)

	events, err := r.StreamChat(context.Background(), []providers.Message{{
		Role: providers.UserRole, Content: "widening mid-flight",
	}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	_, final := drainEventsForTest(t, events)
	if final == nil {
		t.Fatal("run did not complete")
	}

	if widening.askCount() != 0 {
		t.Fatalf("widened asker was consulted %d times in-flight; capture-at-spawn failed", widening.askCount())
	}
	if got := echo.count(); got != 0 {
		t.Fatalf("child executed %d tools despite the deny answer", got)
	}
}

// chooserAsker adapts a plain function to permissions.Asker.
type chooserAsker struct {
	fn func(permissions.Request) (permissions.Prompt, error)
}

func (c chooserAsker) Ask(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
	return c.fn(req)
}

// chooseFunc constructs a chooserAsker.
func chooseFunc(fn func(permissions.Request) (permissions.Prompt, error)) chooserAsker {
	return chooserAsker{fn: fn}
}

// countingAsker is a chooserAsker that records every request it answers,
// so tests can assert on the asks' content (origin stamps, tools) and
// their count.
type countingAsker struct {
	mu    sync.Mutex
	fn    func(permissions.Request) (permissions.Prompt, error)
	seen_ []permissions.Request
}

func newCountingAsker(fn func(permissions.Request) (permissions.Prompt, error)) *countingAsker {
	return &countingAsker{fn: fn}
}

func (a *countingAsker) Ask(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
	a.mu.Lock()
	a.seen_ = append(a.seen_, req)
	fn := a.fn
	a.mu.Unlock()
	return fn(req)
}

func (a *countingAsker) seen() []permissions.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]permissions.Request(nil), a.seen_...)
}

func (a *countingAsker) askCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.seen_)
}

// TestAdversarialConcurrentChildPromptsAreSerialized: two children asking
// simultaneously in interactive mode share one approval scope.
//
// Discrimination design (timing-free on the judged property): the gated
// asker parks the first ask. A second ask that ENTERS while another ask is
// parked sets the overlap flag irreversibly. With the shared scope the
// second child is held before the asker, so no entry can happen while the
// first request is parked; the flag stays false no matter how the goroutine
// scheduler orders events. Without the scope the second child's entry is an
// in-memory path arriving orders of magnitude faster than the 250ms
// detection margin (the same widened-window technique as
// concurrencyProbeProvider), so the flag reliably documents the overlap.
// The judged assertion (overlap never happened; both children answered
// once; rules untouched) is evaluated only after the whole run finishes.
func TestAdversarialConcurrentChildPromptsAreSerialized(t *testing.T) {
	asker := newGatedAsker(permissions.PromptAllowOnce)
	r, store, echo := newPermissionAdversarialRuntime(
		t, parallelChildrenTurns(), delegateAllowRules(), asker,
		config.Orchestration{
			Enabled:               true,
			MaxConcurrentChildren: intPtr(2),
			MaxChildrenPerRun:     intPtr(2),
			ChildPermissions:      config.ChildPermissionsInteractive,
		})

	done := make(chan struct{})
	var toolResults map[string]*ToolResult
	var final *providers.Response
	go func() {
		defer close(done)
		events, err := r.StreamChat(context.Background(), []providers.Message{{
			Role: providers.UserRole, Content: "two children ask together",
		}})
		if err != nil {
			t.Errorf("StreamChat: %v", err)
			return
		}
		toolResults, final = drainEventsForTest(t, events)
	}()

	// The first ask parks. Allow the second child's (unserialized-world)
	// entry margin to expire before judging; the flag itself only ever
	// flips on a real simultaneous entry.
	select {
	case <-asker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock detector: the first child prompt never appeared")
	}
	select {
	case <-asker.entered: // a second ask already entered: overlap observed
	case <-time.After(250 * time.Millisecond):
		// In the correct world the second ask arrives only after the first
		// resolves; open the first gate to release it.
	}

	asker.release(0)
	select {
	case <-asker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock detector: the second child prompt never appeared after the first resolved")
	}
	asker.release(1)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock detector: run did not complete after both gates opened")
	}

	if asker.overlap.Load() {
		t.Fatal("two child prompts were open simultaneously; the shared ask scope did not serialize them")
	}
	if got := echo.count(); got != 2 {
		t.Fatalf("child tool executions = %d, want 2 (each child ran its echo once)", got)
	}
	if store.saveCount() != 0 {
		t.Fatal("interactive child asks persisted state")
	}
	if final == nil {
		t.Fatal("run did not complete")
	}
	for _, id := range []string{"call-delegate-a", "call-delegate-b"} {
		res := toolResults[id]
		if res == nil || !res.Success {
			t.Fatalf("delegate report %s must succeed after both asks resolved: %+v", id, res)
		}
	}
}

// parallelChildrenTurns scripts two delegate calls in one parent batch,
// each child performing one askable echo call.
func parallelChildrenTurns() *keyedProvider {
	return &keyedProvider{
		parentTurns: [][]providers.StreamEvent{
			{
				{ToolCalls: []providers.ToolCall{
					{
						ID:   "call-delegate-a",
						Name: "delegate",
						Arguments: map[string]any{
							"agent": "researcher",
							"task":  "child A task",
						},
					},
					{
						ID:   "call-delegate-b",
						Name: "delegate",
						Arguments: map[string]any{
							"agent": "researcher",
							"task":  "child B task",
						},
					},
				}},
				{Done: true},
			},
			{
				{Text: "parent done."},
				{Done: true},
			},
		},
		childTurns: map[string][][]providers.StreamEvent{
			"child A task": {
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-a-echo",
						Name:      "echo",
						Arguments: map[string]any{"value": "a"},
					}}},
					{Done: true},
				},
				{
					{Text: "child A done"},
					{Done: true},
				},
			},
			"child B task": {
				{
					{ToolCalls: []providers.ToolCall{{
						ID:        "call-b-echo",
						Name:      "echo",
						Arguments: map[string]any{"value": "b"},
					}}},
					{Done: true},
				},
				{
					{Text: "child B done"},
					{Done: true},
				},
			},
		},
	}
}

// keyedProvider routes model turns deterministically for runs that can hold
// parallel children. Parent turns are consumed in parent-call order; child
// turns are keyed by the child's user message (the delegation task text),
// which is unique per child in these tests. The child's system prompt names
// "child-system" via its definition, which distinguishes child calls even
// when a grandchild asks for the same depth-2 shape.
//
// It is safe for the parent and many children to be in-flight at once; each
// call gets its own pre-buffered event channel like scriptedProvider. Out
// of script calls fail fast (context.Canceled) so a bad route can never
// stall a deadline-based deadlock detector silently.
type keyedProvider struct {
	mu          sync.Mutex
	parentTurns [][]providers.StreamEvent
	childTurns  map[string][][]providers.StreamEvent

	parentIdx  int
	childCalls map[string]int

	// log records the observed call identity order for assertions.
	log []string
}

func (p *keyedProvider) StreamChat(_ context.Context, messages []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	var turn []providers.StreamEvent
	identity := ""

	p.mu.Lock()
	if len(messages) > 0 && strings.Contains(messages[0].Content, "child-system") {
		// Child call: key on the last user message (the delegated task).
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == providers.UserRole {
				identity = messages[i].Content
				break
			}
		}
		if p.childCalls == nil {
			p.childCalls = map[string]int{}
		}
		p.childCalls[identity]++
		logged := identity + fmt.Sprintf("#%d", p.childCalls[identity])
		p.log = append(p.log, logged)
		turns, ok := p.childTurns[identity]
		if !ok {
			p.mu.Unlock()
			return nil, context.Canceled // out of script for this child
		}
		idx := p.childCalls[identity] - 1
		if idx >= len(turns) {
			p.mu.Unlock()
			return nil, context.Canceled // out of script for this child
		}
		turn = turns[idx]
	} else {
		// Parent call: strictly sequential script.
		if p.parentIdx >= len(p.parentTurns) {
			p.mu.Unlock()
			return nil, context.Canceled // out of parent script
		}
		turn = p.parentTurns[p.parentIdx]
		p.parentIdx++
		identity = fmt.Sprintf("parent#%d", p.parentIdx)
		p.log = append(p.log, identity)
	}
	p.mu.Unlock()

	events := make(chan providers.StreamEvent, len(turn))
	for _, event := range turn {
		events <- event
	}
	close(events)
	return events, nil
}

// childCallCount reports how many model turns one keyed child consumed.
func (p *keyedProvider) childCallsCount(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.childCalls[key]
}

func (p *keyedProvider) totalCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.log)
}

func (p *keyedProvider) identityLog() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.log...)
}
