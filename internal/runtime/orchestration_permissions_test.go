package runtime

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// cloneTestRules deep-copies r (Rules.clone is permissions-internal).
func cloneTestRules(r permissions.Rules) permissions.Rules {
	out := r
	out.Tools = make(map[string]permissions.Decision, len(r.Tools))
	for k, v := range r.Tools {
		out.Tools[k] = v
	}
	return out
}

// recordingRulesStore is an in-memory permissions.Store (no disk) that
// counts saves so tests can prove delegation never persists globally.
type recordingRulesStore struct {
	mu    sync.Mutex
	rules permissions.Rules
	saves int
}

func newRecordingRulesStore(rules permissions.Rules) *recordingRulesStore {
	return &recordingRulesStore{rules: cloneTestRules(rules)}
}

func (s *recordingRulesStore) Load() (permissions.Rules, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneTestRules(s.rules), nil
}

func (s *recordingRulesStore) Save(rules permissions.Rules) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = cloneTestRules(rules)
	s.saves++
	return nil
}

func (s *recordingRulesStore) saveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves
}

func (s *recordingRulesStore) current() permissions.Rules {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneTestRules(s.rules)
}

// recordingAsker records every request it receives and replies with a
// scripted prompt. Mirrors what the real approval surfaces do without a
// UI.
type recordingAsker struct {
	mu     sync.Mutex
	asks   []permissions.Request
	prompt permissions.Prompt
	err    error
}

func newRecordingAsker(prompt permissions.Prompt) *recordingAsker {
	return &recordingAsker{prompt: prompt}
}

func (a *recordingAsker) Ask(_ context.Context, req permissions.Request) (permissions.Prompt, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asks = append(a.asks, req)
	return a.prompt, a.err
}

func (a *recordingAsker) askCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.asks)
}

func (a *recordingAsker) lastAsk() (permissions.Request, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.asks) == 0 {
		return permissions.Request{}, false
	}
	return a.asks[len(a.asks)-1], true
}

// newPermissionsOrchestrationRuntime is newOrchestrationTestRuntime with
// a real permissions manager (in-memory store), parent asker, and
// configurable orchestration options, wired exactly as newRuntime wires
// them: one full universe holding echo+delegate, one scheduler sharing
// the manager, orchestrator parent assigned last, ask scope shared with
// the parent scheduler in interactive mode.
func newPermissionsOrchestrationRuntime(t *testing.T, p providers.ModelProvider, rules permissions.Rules, asker permissions.Asker, orchCfg config.Orchestration) (*Runtime, *recordingRulesStore) {
	t.Helper()
	full := tools.NewManager(tools.NewRegistry())
	if err := full.Register(echoTool{}); err != nil {
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
	return r, store
}

// askRules makes every unlisted tool ask (the shipped default), so any
// echo call exercises the ask path on both parent and child sides.
func askRules() permissions.Rules {
	return permissions.Rules{Default: permissions.Ask, Tools: map[string]permissions.Decision{}}
}

// permissionTurns scripts: p1 parent echoes itself (one prompt),
// p2 parent delegates, c1 child echoes, c2 child finishes, p3 parent
// answers. Single shared provider, strictly sequential.
func permissionTurns() [][]providers.StreamEvent {
	return [][]providers.StreamEvent{
		{
			{ToolCalls: []providers.ToolCall{{
				ID:        "call-parent-echo",
				Name:      "echo",
				Arguments: map[string]any{"value": "parent"},
			}}},
			{Done: true},
		},
		{
			{ToolCalls: []providers.ToolCall{{
				ID:   "call-delegate",
				Name: "delegate",
				Arguments: map[string]any{
					"agent": "researcher",
					"task":  "use echo for the payload",
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
			{Text: "coordinated."},
			{Done: true},
		},
	}
}

// TestDenyModeChildAsksFailClosedAndPersistNothing: the shipped child
// permission mode denies every would-be prompt, records the denial
// honestly in the delegate result, and never touches the global rule
// set. The parent's own echo was allowed once via a real prompt; the
// child's echo was denied despite that same approval existing on the
// parent scheduler: session approvals do not flow into children.
func TestDenyModeChildAsksFailClosedAndPersistNothing(t *testing.T) {
	provider := &scriptedProvider{turns: permissionTurns()}
	runtime, store := newPermissionsOrchestrationRuntime(
		t, provider, askRules(), newRecordingAsker(permissions.PromptAlwaysAllow),
		config.Orchestration{Enabled: true})

	before := runtime.scheduler.permissions.Rules()

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "coordinate"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	delegateFinish, final := delegateRunSummary(events)
	if provider.calls != 5 {
		t.Fatalf("provider calls = %d, want 5", provider.calls)
	}
	if final == nil || final.Content != "coordinated." {
		t.Fatalf("final response = %#v, want the parent's own closing answer", final)
	}
	if delegateFinish == nil {
		t.Fatalf("delegate call never produced a terminal tool result")
	}
	if !strings.Contains(delegateFinish.Content, "denied=1") {
		t.Fatalf("delegate result = %q, want the child echo denied once", delegateFinish.Content)
	}
	if !strings.Contains(delegateFinish.Content, "tool_calls=1") {
		t.Fatalf("delegate result = %q, want the single child echo call recorded", delegateFinish.Content)
	}

	// The parent has a session decision for echo (its own "always
	// allow"): proves the approval happened on the parent side.
	if d, ok := runtime.scheduler.getSessionDecision("echo"); !ok || d != permissions.Allow {
		t.Fatalf("parent session decision for echo = (%v, %v), want (allow, true)", d, ok)
	}

	// Deny mode shares no ask scope with the parent scheduler: children
	// never open prompts there at all.
	if runtime.scheduler.askSerial != nil {
		t.Fatal("deny mode must not wire a shared ask scope into child schedulers")
	}

	// Nothing was persisted globally: unchanged rules, zero store writes.
	if store.saveCount() != 0 {
		t.Fatalf("permission store saves = %d, want 0 (children never persist)", store.saveCount())
	}
	if after := runtime.scheduler.permissions.Rules(); !reflect.DeepEqual(after, before) {
		t.Fatalf("permission rules changed during the run: %v, want unchanged", after)
	}
	if got := store.current(); !reflect.DeepEqual(got, before) {
		t.Fatalf("stored rules = %v, want unchanged", got)
	}
}

// TestChildAskerDowngradesAlwaysAnswers verifies the interactive-mode
// wrapper: origin stamping and the always→once downgrade for both allow
// and deny prompts.
func TestChildAskerDowngradesAlwaysAnswers(t *testing.T) {
	orch := newOrchestrator(config.Orchestration{
		Enabled:          true,
		ChildPermissions: config.ChildPermissionsInteractive,
	}, newChildRegistryForTests(), nil, nil)
	base := newRecordingAsker(permissions.PromptAlwaysAllow)
	orch.parent = &Runtime{
		agent:     agent.New("root", "root-system", ""),
		scheduler: newScheduler(nil, nil, base, DefaultSchedulerConfig),
	}
	def, err := orch.registry.Get("researcher")
	if err != nil {
		t.Fatalf("registry get researcher: %v", err)
	}

	asker := orch.childAsker(def, []string{"root", "researcher"})

	prompt, askErr := asker.Ask(context.Background(), permissions.Request{Tool: "echo"})
	if askErr != nil {
		t.Fatalf("child asker error = %v", askErr)
	}
	if prompt != permissions.PromptAllowOnce {
		t.Fatalf("child prompt = %v, want always-allow downgraded to allow-once", prompt)
	}
	req, ok := base.lastAsk()
	if !ok {
		t.Fatalf("base asker never saw the child request")
	}
	if req.Origin == nil {
		t.Fatalf("child ask carried no Origin stamp")
	}
	if req.Origin.Agent != "researcher" {
		t.Fatalf("origin agent = %q, want researcher", req.Origin.Agent)
	}
	if req.Origin.Chain != "root > researcher" {
		t.Fatalf("origin chain = %q, want root > researcher", req.Origin.Chain)
	}

	denyBase := newRecordingAsker(permissions.PromptAlwaysDeny)
	orch.parent.scheduler.asker = denyBase
	asker2 := orch.childAsker(def, []string{"root", "researcher"})
	prompt2, askErr2 := asker2.Ask(context.Background(), permissions.Request{Tool: "echo"})
	if askErr2 != nil {
		t.Fatalf("child asker error = %v", askErr2)
	}
	if prompt2 != permissions.PromptDenyOnce {
		t.Fatalf("child prompt = %v, want always-deny downgraded to deny-once", prompt2)
	}
}

// TestInteractiveChildAsksDelegateToOperatorSurface: in interactive
// mode child asks route to the parent asker with the Origin stamp, the
// downgraded decision is honored (the child's echo ran on allow-once),
// and nothing is persisted globally.
func TestInteractiveChildAsksDelegateToOperatorSurface(t *testing.T) {
	provider := &scriptedProvider{turns: permissionTurns()}
	recordParent := newRecordingAsker(permissions.PromptAlwaysAllow)
	runtime, store := newPermissionsOrchestrationRuntime(
		t, provider, askRules(), recordParent,
		config.Orchestration{Enabled: true, ChildPermissions: config.ChildPermissionsInteractive})

	events, err := runtime.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "coordinate"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	delegateFinish, final := delegateRunSummary(events)
	if provider.calls != 5 {
		t.Fatalf("provider calls = %d, want 5", provider.calls)
	}
	if final == nil || final.Content != "coordinated." {
		t.Fatalf("final response = %#v, want the parent's own closing answer", final)
	}
	if delegateFinish == nil {
		t.Fatalf("delegate call never produced a terminal tool result")
	}
	if !strings.Contains(delegateFinish.Content, "tool_calls=1") {
		t.Fatalf("delegate result = %q, want the child's echoed call executed exactly once", delegateFinish.Content)
	}
	if !strings.Contains(delegateFinish.Content, "denied=0") {
		t.Fatalf("delegate result = %q, want no denials", delegateFinish.Content)
	}

	// All three asks reached the operator's surface: the parent's own
	// echo, the parent's delegate call itself (default-ask rules), and
	// the child's echo (stamped Origin, downgraded to one-time).
	if n := recordParent.askCount(); n != 3 {
		t.Fatalf("asker saw %d asks, want 3 (own echo + delegate + child echo)", n)
	}
	var childAsk permissions.Request
	for _, req := range recordParent.asks {
		if req.Origin != nil {
			childAsk = req
		}
	}
	if childAsk.Origin == nil {
		t.Fatalf("no child ask carried an Origin stamp")
	}
	if childAsk.Origin.Agent != "researcher" {
		t.Fatalf("origin agent = %q, want researcher", childAsk.Origin.Agent)
	}
	if childAsk.Origin.Chain != "root > researcher" {
		t.Fatalf("origin chain = %q, want root > researcher", childAsk.Origin.Chain)
	}

	// Shared ask scope exists in interactive mode (serialized with the
	// parent): deny mode leaves it nil.
	if runtime.scheduler.askSerial == nil {
		t.Fatal("interactive mode must share the orchestrator ask scope with the parent scheduler")
	}

	// Nothing was persisted globally even though the operator pressed
	// "always allow": the downgrade kept it one-time, and the child
	// scheduler's own session map is its only ledger.
	if store.saveCount() != 0 {
		t.Fatalf("permission store saves = %d, want 0", store.saveCount())
	}
	if got := store.current(); got.Default != permissions.Ask || len(got.Tools) != 0 {
		t.Fatalf("stored rules = %v, want unchanged ask rules", got)
	}
}
