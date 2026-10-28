package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/orchevents"
	"forcefield/internal/permissions"
	"forcefield/internal/skills"
	"forcefield/internal/tools"
)

// Orchestration bounds resolved from config. Omitted values keep the
// documented defaults; explicit values are already validated by the
// config package against the same ceilings this struct enforces.
type effectiveOrchestration struct {
	maxDepth       int
	maxConcurrent  int
	maxPerRun      int
	maxTaskChars   int
	maxTotalTurns  int
	childPermsMode string // config.ChildPermissionsDeny or ...Interactive
}

// resolveOrchestration fills omitted bounds with the documented defaults.
// Validation already happened at config load; this only resolves defaults.
func resolveOrchestration(cfg config.Orchestration) effectiveOrchestration {
	const (
		defaultDepth      = 1
		defaultConc       = 4
		defaultPerRun     = 16
		defaultTaskChars  = 8000
		defaultTotalTurns = 2048
	)
	eff := effectiveOrchestration{
		maxDepth:       defaultDepth,
		maxConcurrent:  defaultConc,
		maxPerRun:      defaultPerRun,
		maxTaskChars:   defaultTaskChars,
		maxTotalTurns:  defaultTotalTurns,
		childPermsMode: config.ChildPermissionsDeny,
	}
	if cfg.MaxDepth != nil {
		eff.maxDepth = *cfg.MaxDepth
	}
	if cfg.MaxConcurrentChildren != nil {
		eff.maxConcurrent = *cfg.MaxConcurrentChildren
	}
	if cfg.MaxChildrenPerRun != nil {
		eff.maxPerRun = *cfg.MaxChildrenPerRun
	}
	if cfg.MaxTaskChars != nil {
		eff.maxTaskChars = *cfg.MaxTaskChars
	}
	if cfg.MaxTotalChildTurns != nil {
		eff.maxTotalTurns = *cfg.MaxTotalChildTurns
	}
	switch cfg.ChildPermissions {
	case "", config.ChildPermissionsDeny:
		eff.childPermsMode = config.ChildPermissionsDeny
	case config.ChildPermissionsInteractive:
		eff.childPermsMode = config.ChildPermissionsInteractive
	}
	return eff
}

// Orchestrator owns every child-agent run. It holds immutable references
// into the parent Runtime's shared assets (registry, full tool manager,
// skill store) and the runtime itself; all mutable per-run bookkeeping
// lives in orchestration scopes attached to context values, so a child
// never shares authorization or lifecycle state with its parent beyond
// what the scope explicitly carries.
type Orchestrator struct {
	// parent is the owning Runtime, assigned right after construction.
	// Delegate execution must tolerate a nil parent only as a
	// programming error: it reports a soft failure, never a panic.
	parent *Runtime

	registry *agent.Registry
	full     *tools.Manager
	store    *skills.Store

	resolved effectiveOrchestration

	// sem caps child runs running at once, process-wide. Acquired before
	// a child spawns, released after the child joins.
	sem chan struct{}

	// askLock serializes interactive permission asks across the parent
	// scheduler and every child scheduler: the single approval surface
	// is never overwritten by concurrent prompts. nil usage for deny
	// mode: children never open prompts there, so sharing is moot.
	askLock sync.Mutex

	// sinkMu guards orchestration-event sink swaps (set through
	// Runtime.SetOrchestrationEventSink) against emission reads inside
	// an active run. Emissions are best-effort: a nil sink disables.
	sinkMu sync.Mutex
	sink   orchevents.Sink
}

// newOrchestrator builds the process-wide orchestrator with its bounds
// resolved. registry is the frozen agent registry; skills is the shared
// skill store; full is the frozen full tool manager.
func newOrchestrator(cfg config.Orchestration, registry *agent.Registry, skillsStore *skills.Store, full *tools.Manager) *Orchestrator {
	resolved := resolveOrchestration(cfg)
	return &Orchestrator{
		registry: registry,
		store:    skillsStore,
		full:     full,
		resolved: resolved,
		sem:      make(chan struct{}, resolved.maxConcurrent),
	}
}

// setSink swaps the orchestration event sink (nil clears it). See
// Runtime.SetOrchestrationEventSink.
func (o *Orchestrator) setSink(s orchevents.Sink) {
	if o == nil {
		return
	}
	o.sinkMu.Lock()
	defer o.sinkMu.Unlock()
	o.sink = s
}

// emitDelegation records a pre-spawn delegation event (requested,
// accepted, or rejected), attributed to the requested child's path.
// The sink is read under the mutex but never held across the emission
// itself, and a nil sink disables the whole path.
func (o *Orchestrator) emitDelegation(ctx context.Context, scope *orchestrationScope, target string, ev orchevents.Event) {
	if o == nil || ctx == nil || scope == nil {
		return
	}
	o.sinkMu.Lock()
	s := o.sink
	o.sinkMu.Unlock()
	if s == nil {
		return
	}
	chain := scope.chain
	if target != "" {
		extended := make([]string, len(chain), len(chain)+1)
		copy(extended, chain)
		chain = append(extended, strings.ToLower(target))
	}
	ev.Chain = strings.Join(chain, " > ")
	ev.Depth = scope.depth
	if ev.CallID == "" {
		ev.CallID = tools.ToolCallIDFromContext(ctx)
	}
	s.Emit(ev)
}

// emitChildRun records a child-run lifecycle event (spawned, progress,
// completed) with the child's own path attribution; childScope already
// carries the full chain including the child name.
func (o *Orchestrator) emitChildRun(ctx context.Context, childScope *orchestrationScope, ev orchevents.Event) {
	if o == nil || ctx == nil || childScope == nil {
		return
	}
	o.sinkMu.Lock()
	s := o.sink
	o.sinkMu.Unlock()
	if s == nil {
		return
	}
	ev.Chain = strings.Join(childScope.chain, " > ")
	ev.Depth = childScope.depth
	if ev.CallID == "" {
		ev.CallID = tools.ToolCallIDFromContext(ctx)
	}
	s.Emit(ev)
}

// orchestrationShared is the spawn counter shared by every orchestration
// scope in one top-level run: all levels count against
// max_children_per_run, so nesting cannot multiply the budget.
type orchestrationShared struct {
	mu      sync.Mutex
	spawned int
	// turns counts aggregate child model turns across every nesting
	// level of one top-level run; fired dedups the BudgetExhausted
	// emission so the first crossing reports exactly once. Cancellation
	// still applies to every over-limit child that keeps recording.
	turns int
	fired bool
}

// consumeTurn charges one child model turn against the aggregate
// budget. It reports whether this charge crossed the limit and whether
// this charge is the one that reports it.
func (sh *orchestrationShared) consumeTurn(limit int) (total int, over, fire bool) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.turns++
	total = sh.turns
	over = limit > 0 && total > limit
	if over && !sh.fired {
		sh.fired = true
		fire = true
	}
	return total, over, fire
}

// orchestrationScope is the per-parent-run orchestration context. It
// travels through context values: the root scope is attached at
// StreamChat entry; each spawned child carries a scope one level deeper
// sharing the same counters pointer.
type orchestrationScope struct {
	orch   *Orchestrator
	shared *orchestrationShared
	// depth is this scope's nesting level: 0 for the top-level run.
	depth int
	// chain records the agent names along the delegation path, root
	// first, for attribution in approval surfaces.
	chain []string
	// snap is the parent run's fixed snapshot: provider, caps, budget.
	// The child reads its immutable parts; nothing writes to it.
	snap *runSnapshot
}

type orchestrationScopeKey struct{}

func withOrchestrationScope(ctx context.Context, sc *orchestrationScope) context.Context {
	if sc == nil {
		return ctx
	}
	return context.WithValue(ctx, orchestrationScopeKey{}, sc)
}

func orchestrationScopeFrom(ctx context.Context) *orchestrationScope {
	if ctx == nil {
		return nil
	}
	sc, _ := ctx.Value(orchestrationScopeKey{}).(*orchestrationScope)
	return sc
}

// rootScope builds the scope for a top-level run: depth 0, chain seeded
// with the active agent's registry name.
func (o *Orchestrator) rootScope(snap *runSnapshot) *orchestrationScope {
	root := agentNameFromSnapshot(o, snap)
	return &orchestrationScope{
		orch:   o,
		shared: &orchestrationShared{},
		depth:  0,
		chain:  []string{root},
		snap:   snap,
	}
}

func agentNameFromSnapshot(o *Orchestrator, snap *runSnapshot) string {
	if o == nil || o.registry == nil || snap.agent == nil {
		return ""
	}
	// The active agent's registry name; fall back to the prompt identity.
	names := o.registry.Names()
	current := ""
	if o.parent != nil {
		current = strings.ToLower(strings.TrimSpace(o.parent.CurrentAgent()))
	}
	for _, n := range names {
		if n == current {
			return n
		}
	}
	return current
}

// childScope derives a scope for the spawned child: one level deeper,
// sharing the counters, chain extended with the child agent's name. It
// copies the parent snapshot pointer reference; child overrides are
// applied to a separate copy inside spawnChild.
func (sc *orchestrationScope) childScope(childAgent string) *orchestrationScope {
	chain := make([]string, len(sc.chain), len(sc.chain)+1)
	copy(chain, sc.chain)
	chain = append(chain, childAgent)
	return &orchestrationScope{
		orch:   sc.orch,
		shared: sc.shared,
		depth:  sc.depth + 1,
		chain:  chain,
		snap:   sc.snap,
	}
}

// childSpawnAllowed reports whether one more spawn at this scope's depth
// is permitted, and if so registers it atomically. Per-run counts are
// cumulative: completed children never free budget by design, so total
// work stays bounded by max_children_per_run regardless of timing.
func (sc *orchestrationScope) admitSpawn() (newDepth int, ok bool) {
	newDepth = sc.depth + 1
	if newDepth > sc.orch.resolved.maxDepth {
		return newDepth, false
	}
	sc.shared.mu.Lock()
	defer sc.shared.mu.Unlock()
	if sc.shared.spawned >= sc.orch.resolved.maxPerRun {
		return newDepth, false
	}
	sc.shared.spawned++
	return newDepth, true
}

// spawnedTotal reports the cumulative child-run count for this scope's
// top-level run, across every nesting level. Used in honest error text.
func (sc *orchestrationScope) spawnedTotal() int {
	sc.shared.mu.Lock()
	defer sc.shared.mu.Unlock()
	return sc.shared.spawned
}

// childAgentManager builds the child's filtered tool universe: the child
// definition's tool set resolved against the frozen full manager (MCP
// names resolved same as parents; unknown natives fail in Filtered),
// with delegate removed at the leaf level, and load_skill replaced by a
// definition-scoped instance so a child can load only its own assignment
// (the shared instance is scoped to the runtime's *active* agent and
// must never gate a child).
func (o *Orchestrator) childAgentManager(def agent.Definition, allowDelegate bool) (*tools.Manager, error) {
	if o == nil || o.full == nil {
		return nil, fmt.Errorf("orchestration: tool universe is not initialized")
	}
	// Same visibility rule as the parent path (withDelegateTool): agents
	// that opted into tools gain delegate when the depth allows; an
	// explicitly empty list is a pure-text agent and stays one. delegate
	// and load_skill never pass straight from def.Tools: delegate is
	// re-added below only where the depth allows it (the leaf drop is the
	// structural depth bound), load_skill is replaced by the
	// child-scoped instance so a child can load only its own assignment.
	childTools := make([]string, 0, len(def.Tools)+1)
	for _, name := range def.Tools {
		switch name {
		case "delegate":
			continue
		case "load_skill":
			continue
		}
		childTools = append(childTools, name)
	}
	if allowDelegate && len(def.Tools) > 0 {
		childTools = append(childTools, "delegate")
	}
	keep := childTools
	// Same tool-resolution rule as the parent path: requested MCP names
	// absent from the universe are omitted (warned at the parent layer);
	// unknown native names would fail strictly in Filtered.
	resolved, _ := o.parentResolveTools(keep)
	mgr, err := o.full.Filtered(resolved)
	if err != nil {
		return nil, fmt.Errorf("build child tool set for agent %q: %w", def.Name, err)
	}
	if hasTool(def.Tools, "load_skill") {
		childSkillTool := newLoadSkillTool(o.store)
		childSkillTool.allowed = defSkillSet(o.store, def)
		name := def.Name
		childSkillTool.agentName = func() string { return name }
		if err := mgr.Register(childSkillTool); err != nil {
			return nil, fmt.Errorf("register child load_skill for agent %q: %w", def.Name, err)
		}
	}
	return mgr, nil
}

// parentResolveTools resolves a tool name list against the frozen full
// manager, omitting unavailable MCP names the way the parent agent path
// does. Unknown native names pass through so Filtered fails strictly.
func (o *Orchestrator) parentResolveTools(want []string) ([]string, []string) {
	return resolveAgentTools(o.full, want)
}

// withDelegateTool extends an agent's tool allow-list with "delegate"
// when orchestration is enabled. Agents that opted into a non-empty tool
// set gain delegation (bounded at call time by the orchestrator's depth,
// fan-out, and concurrency caps); an explicitly empty list is a
// pure-text agent and stays one. A list already naming delegate passes
// through unchanged for parents; children get the depth-dependent rule
// in childAgentManager instead.
func withDelegateTool(orch *Orchestrator, tools []string) []string {
	if orch == nil || len(tools) == 0 || hasTool(tools, "delegate") {
		return tools
	}
	out := make([]string, len(tools), len(tools)+1)
	copy(out, tools)
	return append(out, "delegate")
}

func hasTool(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// defSkillSet returns the skill IDs a child agent may load: its own
// assignment intersected with the store, or the full catalog under
// AllSkills. Mirrors the runtime's per-agent rules exactly.
func defSkillSet(store *skills.Store, def agent.Definition) func() map[string]bool {
	set := make(map[string]bool)
	if store != nil {
		if def.AllSkills {
			for _, s := range store.Catalog() {
				set[s.ID] = true
			}
		} else {
			for _, id := range def.Skills {
				if _, ok := store.Get(id); ok {
					set[id] = true
				}
			}
		}
	}
	return func() map[string]bool { return set }
}

// childAsker returns the permission asker for child runs. Deny mode is
// the default and is strictly fail-closed: any call that would prompt is
// denied, nothing is persisted, and no approval surface is touched.
// Interactive mode routes to the operator's real asker with an Origin
// stamp and downgrades "always" answers to one-time decisions.
func (o *Orchestrator) childAsker(def agent.Definition, chain []string) permissions.Asker {
	if o.resolved.childPermsMode != config.ChildPermissionsInteractive {
		return denyAllAsker
	}
	base := o.parentCurrentAsker()
	if base == nil {
		return denyAllAsker
	}
	return permissions.AskerFunc(func(ctx context.Context, req permissions.Request) (permissions.Prompt, error) {
		req.Origin = &permissions.Origin{
			Agent: def.Name,
			Chain: strings.Join(chain, " > "),
		}
		prompt, err := base.Ask(ctx, req)
		if err != nil {
			return permissions.PromptDenyOnce, err
		}
		if prompt.Persist() {
			// A child must never persist any decision: "always" answers
			// are downgraded to one-time so the child's grant cannot
			// outlive its own run, belt-and-braces on top of the fact
			// that child scheduler session decisions are its own map.
			if prompt == permissions.PromptAlwaysAllow {
				return permissions.PromptAllowOnce, nil
			}
			return permissions.PromptDenyOnce, nil
		}
		return prompt, nil
	})
}

// parentCurrentAsker returns the parent scheduler's current asker (the
// operator's approval surface), captured at child-spawn time. A scheduled
// SetPermissionAsker after spawn does not re-route child asks — documented
// limitation; child runs are bounded to the delegation timeout.
func (o *Orchestrator) parentCurrentAsker() permissions.Asker {
	if o == nil || o.parent == nil || o.parent.scheduler == nil {
		return nil
	}
	return o.parent.scheduler.getAsker()
}

// denyAllAsker fails closed for every request. It never touches an
// approval surface and never persists anything.
var denyAllAsker = permissions.AskerFunc(func(_ context.Context, _ permissions.Request) (permissions.Prompt, error) {
	return permissions.PromptDenyOnce, nil
})
