// Package runtime coordinates the agent loop and its dependencies.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/memory"
	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/sandbox"
	"forcefield/internal/session"
	"forcefield/internal/skills"
	"forcefield/internal/task"
	"forcefield/internal/tools"
	"forcefield/internal/tools/builtin"
)

// Limits bounds a single run so a long-horizon task can't spin forever.
// A value <= 0 means "no limit" for that dimension.
type Limits struct {
	// MaxIterations caps how many model turns a single run may take.
	MaxIterations int
	// MaxToolCalls caps the total number of tool calls across the run.
	MaxToolCalls int
	// MaxConsecutiveFailures caps how many tool calls in a row may fail
	// (denied, errored, or timed out) before the runtime concludes the
	// agent is stuck and stops rather than looping indefinitely.
	MaxConsecutiveFailures int
}

// DefaultLimits are generous enough for real multi-step coding tasks
// while still guaranteeing every run terminates.
var DefaultLimits = Limits{
	MaxIterations:          60,
	MaxToolCalls:           300,
	MaxConsecutiveFailures: 5,
}

// maxContextMessages is the bounded sliding window for history sent to the
// provider. It keeps the system prompt, the first user message (goal), and
// the most recent history. This prevents unbounded growth over long-horizon
// runs while preserving the task's intent and recent context.
const maxContextMessages = 100

// Runtime is the main execution point for Forcefield.
type Runtime struct {
	// mu guards the switchable run state below (agent, manager, provider,
	// cfg, auth state, activeAgent). StreamChat snapshots this state under
	// RLock so a background run never observes a mid-run SetAgent /
	// SetModel / SetProvider mutation; switches take effect on the next
	// StreamChat, never mid-turn. Callers (e.g. the TUI) still cancel any
	// active stream before switching.
	mu        sync.RWMutex
	cfg       *config.Config
	provider  providers.ModelProvider
	agent     *agent.Agent
	manager   *tools.Manager
	skills    *skills.Store
	scheduler *scheduler
	limits    Limits

	// discovery caches model listings per provider for the process
	// lifetime. It performs network I/O only when DiscoverModels is
	// called; construction and ModelCatalog stay offline.
	discovery *providers.Discovery

	// authRequired/authEnvVar describe whether the active provider needs
	// an API key and where it would come from. A missing key does not stop
	// construction (users can still browse, switch providers, or run
	// doctor); it fails the model turn with an actionable error instead.
	authRequired bool
	authEnvVar   string

	reasoningMu         sync.RWMutex
	reasoningSelections map[string]providers.ReasoningConfig

	// agents holds the specialised agent registry (instance-scoped,
	// immutable after construction).
	agents *agent.Registry
	// activeAgent is the lowercased name of the currently selected agent.
	activeAgent string
	// fullManager holds every tool (before filtering) so filtered managers
	// can reuse the same Tool instances.
	fullManager *tools.Manager
	// projectMemoryText is cached for rebuilding the agent prompt on
	// switches without re-reading disk. Skill catalogs are computed per
	// agent from the shared store via catalogFor (no cache: the catalog
	// is small and switches are infrequent).
	projectMemoryText string
}

func New() (*Runtime, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return NewFromConfig(cfg)
}

// NewFromConfig builds a Runtime from an already-loaded Config, reusing it
// instead of loading a second time. This eliminates the duplicate
// config.Load + config.Dir before the first frame in the TUI path while
// keeping a global cache unnecessary; callers that already have a Config
// (like tui.Start) should use this.
func NewFromConfig(cfg *config.Config) (*Runtime, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	return newRuntime(cfg)
}

func newRuntime(cfg *config.Config) (*Runtime, error) {
	forcefieldHome, err := config.Dir()
	if err != nil {
		return nil, fmt.Errorf("resolve forcefield home: %w", err)
	}

	skillStore, err := skills.New(forcefieldHome)
	if err != nil {
		return nil, fmt.Errorf("load skill store: %w", err)
	}

	projectMemory, err := memory.CurrentProjectStore(forcefieldHome)
	if err != nil {
		return nil, fmt.Errorf("resolve project memory store: %w", err)
	}
	memoryEntries, err := projectMemory.Load()
	if err != nil {
		return nil, fmt.Errorf("load project memory: %w", err)
	}
	memoryText := memory.FormatForPrompt(memoryEntries)

	provider, err := newProvider(cfg)
	if err != nil {
		return nil, err
	}

	policy, err := newPolicy(cfg)
	if err != nil {
		return nil, err
	}
	executor, err := sandbox.NewExecutor(policy)
	if err != nil {
		return nil, fmt.Errorf("create %s executor (sandbox.mode = %q): %w", policy.Mode, cfg.Sandbox.Mode, err)
	}

	fullManager, err := builtin.NewManager(builtin.WithExecutor(executor), builtin.WithPolicy(policy))
	if err != nil {
		return nil, fmt.Errorf("create tool manager: %w", err)
	}
	loadSkill := newLoadSkillTool(skillStore)
	if err := fullManager.Register(loadSkill); err != nil {
		return nil, fmt.Errorf("register load_skill tool: %w", err)
	}
	if err := fullManager.Register(newUpdateTaskStateTool()); err != nil {
		return nil, fmt.Errorf("register update_task_state tool: %w", err)
	}
	if err := fullManager.Register(newAddProjectMemoryTool(projectMemory)); err != nil {
		return nil, fmt.Errorf("register add_project_memory tool: %w", err)
	}

	// Build specialised agent registry with config overrides.
	registry := agent.DefaultRegistry()
	if err := applyAgentOverrides(registry, cfg.Agents); err != nil {
		return nil, err
	}

	activeName := resolveInitialAgent(cfg, registry)
	def, err := registry.Get(activeName)
	if err != nil {
		// Fallback to general if somehow still unknown.
		def, _ = registry.Get("general")
		activeName = "general"
	}

	filtered, err := fullManager.Filtered(def.Tools)
	if err != nil {
		return nil, fmt.Errorf("build tool set for agent %q: %w", def.Name, err)
	}

	permManager, err := permissions.NewManager(permissions.NewConfigStore())
	if err != nil {
		return nil, fmt.Errorf("load permissions: %w", err)
	}

	asker := permissions.NewStdinAsker()

	r := &Runtime{
		cfg:                 cfg,
		provider:            provider,
		manager:             filtered,
		fullManager:         fullManager,
		skills:              skillStore,
		scheduler:           newScheduler(filtered, permManager, asker, DefaultSchedulerConfig),
		limits:              limitsFromConfig(cfg),
		discovery:           providers.NewDiscovery(providers.DefaultFactories()),
		reasoningSelections: make(map[string]providers.ReasoningConfig),
		agents:              registry,
		activeAgent:         def.Name,
		projectMemoryText:   memoryText,
	}
	r.agent = r.buildAgent(def)
	// Scope load_skill to the active agent's skill set. The closures read
	// live runtime state, so agent switches need no re-registration and
	// the same tool instance stays shared across filtered managers.
	loadSkill.allowed = r.agentSkillSet
	loadSkill.agentName = r.CurrentAgent
	r.refreshAuthState()
	r.applyReasoning()

	// Apply per-agent model/provider hints in-memory (best effort). If they
	// fail, construction still succeeds; the error will surface on SetAgent.
	if def.Provider != "" && def.Provider != cfg.Model.Provider {
		_ = r.SetProvider(def.Provider)
	}
	if def.Model != "" && def.Model != r.cfg.Model.Name {
		_ = r.SetModel(def.Model)
	}
	return r, nil
}

// buildAgent constructs the prompt-building agent for def: per-agent
// skill catalog, domain constraints, legacy display name/prompt handling,
// and shared project memory.
func (r *Runtime) buildAgent(def agent.Definition) *agent.Agent {
	if r == nil {
		return agent.New(def.Name, def.SystemPrompt, "")
	}
	r.mu.RLock()
	cfg := r.cfg
	registry := r.agents
	memoryText := r.projectMemoryText
	r.mu.RUnlock()
	return agent.New(
		legacyDisplayName(cfg, registry, def.Name),
		effectivePromptFor(cfg, def),
		r.catalogFor(def),
	).WithConstraints(def.Constraints).WithProjectMemory(memoryText)
}

// catalogFor renders the skill catalog text for def: the full store
// catalog when def wants all skills, otherwise the store catalog
// intersected with the assigned IDs in store order. IDs absent from the
// store are omitted (see SkillWarnings); never fabricated.
func (r *Runtime) catalogFor(def agent.Definition) string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	store := r.skills
	r.mu.RUnlock()
	if store == nil {
		return ""
	}
	if def.AllSkills {
		return skills.FormatCatalog(store.Catalog())
	}
	if len(def.Skills) == 0 {
		return ""
	}
	keep := make(map[string]struct{}, len(def.Skills))
	for _, id := range def.Skills {
		keep[id] = struct{}{}
	}
	var filtered []skills.Skill
	for _, s := range store.Catalog() {
		if _, ok := keep[s.ID]; ok {
			filtered = append(filtered, s)
		}
	}
	return skills.FormatCatalog(filtered)
}

// agentSkillSet returns the exact skill IDs the active agent may load via
// load_skill. Exact-match only (no normalization fallthrough), and only
// IDs present in the store — a missing assigned skill grants nothing.
// A skill body can never grant a tool: tools resolve exclusively through
// the filtered tool manager.
func (r *Runtime) agentSkillSet() map[string]bool {
	set := make(map[string]bool)
	if r == nil {
		return set
	}
	r.mu.RLock()
	store := r.skills
	registry := r.agents
	active := r.activeAgent
	r.mu.RUnlock()
	if store == nil || registry == nil {
		return set
	}
	def, err := registry.Get(active)
	if err != nil {
		return set
	}
	if def.AllSkills {
		for _, s := range store.Catalog() {
			set[s.ID] = true
		}
		return set
	}
	for _, id := range def.Skills {
		if _, ok := store.Get(id); ok {
			set[id] = true
		}
	}
	return set
}

// SkillWarnings reports assigned-but-missing skill IDs per agent, so users
// can understand why an agent shows fewer skills than its definition
// lists (e.g. an example skill never installed into ~/.forcefield/skills).
// Missing skills degrade gracefully everywhere else; this is diagnostics.
func (r *Runtime) SkillWarnings() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	store := r.skills
	registry := r.agents
	r.mu.RUnlock()
	if store == nil || registry == nil {
		return nil
	}
	return SkillAssignmentWarnings(registry, store)
}

// SkillAssignmentWarnings reports assigned-but-missing skill IDs for a
// registry/store pair without needing a full Runtime. Used by ff doctor.
func SkillAssignmentWarnings(registry *agent.Registry, store *skills.Store) []string {
	if registry == nil || store == nil {
		return nil
	}
	var out []string
	for _, def := range registry.List() {
		if def.AllSkills {
			continue
		}
		for _, id := range def.Skills {
			if _, ok := store.Get(id); !ok {
				out = append(out, fmt.Sprintf("agent %q references missing skill %q (not installed in the skill store)", def.Name, id))
			}
		}
	}
	return out
}

// applyAgentOverrides merges cfg.Agents into registry. Unknown agent names
// have already been rejected by config validation; this is defence-in-depth.
func applyAgentOverrides(registry *agent.Registry, overrides map[string]config.AgentConfig) error {
	return ApplyAgentOverrides(registry, overrides)
}

// ApplyAgentOverrides merges cfg.Agents into registry. Exported for
// diagnostics (ff doctor) that need effective assignments without a full
// Runtime. The registry must still be under construction (not yet shared).
func ApplyAgentOverrides(registry *agent.Registry, overrides map[string]config.AgentConfig) error {
	for name, o := range overrides {
		def, err := registry.Get(name)
		if err != nil {
			return fmt.Errorf("agents.%s: %w", name, err)
		}
		ao := agent.AgentOverride{
			Description:  o.Description,
			SystemPrompt: o.SystemPrompt,
			Tools:        o.Tools,
			Skills:       o.Skills,
			Constraints:  o.Constraints,
			Provider:     o.Provider,
			Model:        o.Model,
		}
		newDef := def.ApplyOverride(ao)
		if err := newDef.Validate(); err != nil {
			return fmt.Errorf("agents.%s: %w", name, err)
		}
		// Replace via Update (registry is still mutable during construction).
		if err := registry.Update(newDef); err != nil {
			return err
		}
		// Validate tool set against full tool universe when possible is done
		// later when building the filtered manager (unknown tool error).
	}
	return nil
}

// resolveInitialAgent picks the starting agent per precedence: cfg.Agent.Name
// (legacy) -> general. CLI flag handling is done outside newRuntime via
// SetAgent. "default" is treated as "general" for backwards compat. Unknown
// legacy names also resolve to "general" (their display name is preserved
// separately by legacyDisplayName).
func resolveInitialAgent(cfg *config.Config, registry *agent.Registry) string {
	raw := strings.ToLower(strings.TrimSpace(cfg.Agent.Name))
	if raw == "" || raw == "default" {
		return "general"
	}
	if _, err := registry.Get(raw); err == nil {
		return raw
	}
	return "general"
}

// effectivePromptFor resolves which system prompt an agent runs with.
// Precedence: agents.<name>.system_prompt (already merged into def via
// overrides) > legacy agent.system_prompt > built-in prompt. This keeps
// pre-feature configs that customized agent.system_prompt behaving as
// before, regardless of which agent is active.
func effectivePromptFor(cfg *config.Config, def agent.Definition) string {
	if cfg == nil {
		return def.SystemPrompt
	}
	if o, ok := cfg.Agents[def.Name]; ok && strings.TrimSpace(o.SystemPrompt) != "" {
		return strings.TrimSpace(def.SystemPrompt)
	}
	if strings.TrimSpace(cfg.Agent.SystemPrompt) != "" {
		return strings.TrimSpace(cfg.Agent.SystemPrompt)
	}
	return def.SystemPrompt
}

// legacyDisplayName preserves a pre-feature custom agent.name as the
// display label. Known agent keys and "default"/"" use the definition
// name; anything else is a legacy custom label shown in the header while
// the "general" definition provides behaviour.
func legacyDisplayName(cfg *config.Config, registry *agent.Registry, active string) string {
	if cfg == nil {
		return active
	}
	raw := strings.TrimSpace(cfg.Agent.Name)
	if raw == "" {
		return active
	}
	if strings.EqualFold(raw, "default") {
		return active
	}
	if registry != nil {
		if _, err := registry.Get(raw); err == nil {
			return active
		}
	}
	return raw
}

// refreshAuthState re-reads the active provider's authentication
// requirements after construction, provider switch, or model switch.
func (r *Runtime) refreshAuthState() {
	if r == nil {
		return
	}
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()
	if cfg == nil {
		r.mu.Lock()
		r.authRequired, r.authEnvVar = false, ""
		r.mu.Unlock()
		return
	}
	resolved, err := cfg.ResolveProvider(cfg.Model.Provider, cfg.Model.Name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.authRequired, r.authEnvVar = false, ""
		return
	}
	r.authRequired = resolved.AuthRequired
	r.authEnvVar = resolved.AuthEnvVar
}

// authSnapshot copies the auth state plus provider display name under RLock
// so background model turns never read live switchable fields.
func (r *Runtime) authSnapshot() (required bool, envVar, provider string) {
	if r == nil {
		return false, "", ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider = ""
	if r.cfg != nil {
		provider = r.cfg.Model.Provider
	}
	return r.authRequired, r.authEnvVar, provider
}

// checkAuth reports an actionable error when the active provider needs an
// API key that was not found.
func (r *Runtime) checkAuth() error {
	required, envVar, provider := r.authSnapshot()
	if !required || envVar == "" {
		return nil
	}
	if key, _, err := config.ResolveEnvValue(envVar); err == nil && key != "" {
		return nil
	}
	return fmt.Errorf(
		"%s requires an API key - set %s in your environment or .env file and restart Forcefield",
		providers.DisplayName(provider), envVar,
	)
}

// checkAuthWithSnapshot is checkAuth against an explicit snapshot so a run
// uses one consistent view even if a switch lands mid-run.
func checkAuthWithSnapshot(required bool, envVar, provider string) error {
	if !required || envVar == "" {
		return nil
	}
	if key, _, err := config.ResolveEnvValue(envVar); err == nil && key != "" {
		return nil
	}
	return fmt.Errorf(
		"%s requires an API key - set %s in your environment or .env file and restart Forcefield",
		providers.DisplayName(provider), envVar,
	)
}

// limitsFromConfig builds Limits from cfg.Agent, falling back to
// DefaultLimits for any dimension left at its zero value.
func limitsFromConfig(cfg *config.Config) Limits {
	limits := DefaultLimits
	if cfg.Agent.MaxIterations > 0 {
		limits.MaxIterations = cfg.Agent.MaxIterations
	}
	if cfg.Agent.MaxToolCalls > 0 {
		limits.MaxToolCalls = cfg.Agent.MaxToolCalls
	}
	if cfg.Agent.MaxConsecutiveFailures > 0 {
		limits.MaxConsecutiveFailures = cfg.Agent.MaxConsecutiveFailures
	}
	return limits
}

// SetPermissionAsker replaces how "ask" permission decisions are
// resolved. The default (set in New) prompts on stdin/stdout, which
// isn't usable once something like the TUI has taken over the terminal;
// callers with their own interactive surface should call this before the
// first StreamChat/Run.
func (r *Runtime) SetPermissionAsker(asker permissions.Asker) {
	if r == nil || r.scheduler == nil {
		return
	}
	r.scheduler.setAsker(asker)
}

// Permissions returns the runtime's permission manager, e.g. for a
// "/permissions" command that lists or edits the current rules.
func (r *Runtime) Permissions() *permissions.Manager {
	if r == nil || r.scheduler == nil {
		return nil
	}
	return r.scheduler.permissions
}

// CurrentModel returns the active model name.
func (r *Runtime) CurrentModel() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.cfg == nil {
		return ""
	}
	return r.cfg.Model.Name
}

// CurrentProvider returns the active provider name.
func (r *Runtime) CurrentProvider() string {
	if r == nil {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.cfg == nil {
		return ""
	}
	return r.cfg.Model.Provider
}

// CurrentAgent returns the active agent definition key (e.g. "coding").
// Use AgentDisplayName for the header label, which may preserve a legacy
// custom agent.name from config.
func (r *Runtime) CurrentAgent() string {
	if r == nil {
		return "general"
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.activeAgent != "" {
		return r.activeAgent
	}
	return "general"
}

// AgentDisplayName returns the label shown in the UI header. It is the
// definition name, except when a pre-feature config set a custom
// agent.name, which is preserved verbatim.
func (r *Runtime) AgentDisplayName() string {
	if r == nil {
		return "general"
	}
	r.mu.RLock()
	name := ""
	if r.agent != nil {
		name = r.agent.Name
	}
	active := r.activeAgent
	r.mu.RUnlock()
	if strings.TrimSpace(name) != "" {
		return name
	}
	if active != "" {
		return active
	}
	return "general"
}

// AgentSummary describes one available agent for pickers and listings.
type AgentSummary struct {
	Name        string
	Description string
	Tools       []string
	// Skills lists assigned skill IDs; AllSkills reports full-catalog access.
	Skills      []string
	AllSkills   bool
	Constraints []string
	Provider    string
	Model       string
}

// AgentSummaries returns every known agent for listings and pickers.
func (r *Runtime) AgentSummaries() []AgentSummary {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	registry := r.agents
	r.mu.RUnlock()
	if registry == nil {
		return nil
	}
	out := make([]AgentSummary, 0)
	for _, d := range registry.List() {
		out = append(out, AgentSummary{
			Name:        d.Name,
			Description: d.Description,
			Tools:       append([]string(nil), d.Tools...),
			Skills:      append([]string(nil), d.Skills...),
			AllSkills:   d.AllSkills,
			Constraints: append([]string(nil), d.Constraints...),
			Provider:    d.Provider,
			Model:       d.Model,
		})
	}
	return out
}

// ListAgents is an alias for AgentSummaries for convenience.
func (r *Runtime) ListAgents() []AgentSummary { return r.AgentSummaries() }

// CurrentAgentDefinition returns the active agent definition.
func (r *Runtime) CurrentAgentDefinition() (agent.Definition, error) {
	if r == nil {
		return agent.Definition{}, fmt.Errorf("agent registry not available")
	}
	r.mu.RLock()
	registry := r.agents
	active := r.activeAgent
	r.mu.RUnlock()
	if registry == nil {
		return agent.Definition{}, fmt.Errorf("agent registry not available")
	}
	if active == "" {
		active = "general"
	}
	return registry.Get(active)
}

// SetAgent switches the active agent to name. It rebuilds the system
// prompt, filtered tool set, and applies any provider/model hints. The
// switch is atomic: on any failure the previous agent, tools, and
// provider/model remain unchanged.
func (r *Runtime) SetAgent(name string) error {
	if r == nil {
		return fmt.Errorf("runtime not available")
	}
	r.mu.RLock()
	registry := r.agents
	full := r.fullManager
	curActive := r.activeAgent
	cfgProvider := ""
	cfgModel := ""
	if r.cfg != nil {
		cfgProvider = r.cfg.Model.Provider
		cfgModel = r.cfg.Model.Name
	}
	r.mu.RUnlock()
	if registry == nil || full == nil {
		return fmt.Errorf("agent system not initialized")
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return fmt.Errorf("agent name cannot be empty")
	}
	def, err := registry.Get(key)
	if err != nil {
		return err
	}
	// Compare against the snapshot; the final commit re-checks under Lock
	// so a concurrent switch cannot interleave silently. The common
	// no-change case returns early here without taking the write lock.
	if key == curActive {
		return nil
	}

	// Build new filtered manager and agent before mutating to ensure
	// tool set is valid.
	filtered, err := full.Filtered(def.Tools)
	if err != nil {
		return fmt.Errorf("agent %q has invalid tool set: %w", def.Name, err)
	}

	// NOTE: buildAgent must run before provider/model hints because
	// legacyDisplayName/effectivePromptFor read the pre-switch cfg.
	// The agent/tools commit below happens only after hints succeed.
	newAgent := r.buildAgent(def)

	// Snapshot provider/model state for rollback. The agent/tools swap
	// below only happens after hints succeed, so it needs no rollback.
	r.mu.RLock()
	var oldCfg config.Config
	var haveCfg bool
	var oldProvider providers.ModelProvider
	var oldAuthReq bool
	var oldAuthEnv string
	if r.cfg != nil {
		oldCfg = *r.cfg
		haveCfg = true
	}
	oldProvider = r.provider
	oldAuthReq = r.authRequired
	oldAuthEnv = r.authEnvVar
	curProvider := cfgProvider
	curModel := cfgModel
	r.mu.RUnlock()

	// Apply provider hint first, if any.
	if def.Provider != "" && def.Provider != curProvider {
		if err := r.SetProvider(def.Provider); err != nil {
			return fmt.Errorf("agent %q provider %q: %w", def.Name, def.Provider, err)
		}
		r.mu.RLock()
		if r.cfg != nil {
			curModel = r.cfg.Model.Name
		}
		r.mu.RUnlock()
	}
	// Apply model hint.
	if def.Model != "" && def.Model != curModel {
		if err := r.SetModel(def.Model); err != nil {
			// Roll back provider switch if model fails after provider succeeded.
			if haveCfg {
				r.mu.Lock()
				r.cfg = &oldCfg
				r.provider = oldProvider
				r.authRequired = oldAuthReq
				r.authEnvVar = oldAuthEnv
				r.mu.Unlock()
				r.applyReasoning()
			}
			return fmt.Errorf("agent %q model %q: %w", def.Name, def.Model, err)
		}
	}

	// Commit agent/tools switch.
	r.mu.Lock()
	r.agent = newAgent
	r.manager = filtered
	r.activeAgent = def.Name
	sched := r.scheduler
	r.mu.Unlock()
	if sched != nil {
		sched.setManager(filtered)
	}
	return nil
}

// Agent returns the current agent name, satisfying command.Context.
func (r *Runtime) Agent() string { return r.CurrentAgent() }

// Agents returns summaries, satisfying command.Context.
func (r *Runtime) Agents() []AgentSummary { return r.AgentSummaries() }

// ProviderSummary describes one selectable provider for pickers and
// status output: who it is, what it supports, and whether it is usable
// right now. Availability is checked without network I/O - a cloud
// provider with its key missing is reported as unavailable rather than
// silently failing later.
type ProviderSummary struct {
	ID   string
	Name string
	// Detail is the compact descriptor shown under picker rows, e.g.
	// "local · tools · streaming" or "cloud · tools · api key missing".
	Detail string
	// Models lists known model IDs (configured or catalog defaults).
	Models []string
	// Available reports whether switching to this provider would work.
	Available bool
}

// ProviderSummaries describes every configured or known provider in
// catalog order (custom entries last). The active provider is included;
// callers mark it current.
func (r *Runtime) ProviderSummaries() []ProviderSummary {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()
	if cfg == nil {
		return nil
	}
	resolved, _ := cfg.ResolveAll(cfg.Model.Name)
	out := make([]ProviderSummary, 0, len(resolved))
	for _, p := range resolved {
		caps := providers.CapabilitiesFor(p.Type)
		scope := "local"
		if preset, ok := providers.PresetByID(p.ID); ok {
			scope = string(preset.Scope)
		}
		detail := scope + capsSuffix(caps)
		available := true
		if p.AuthRequired && p.APIKey == "" {
			detail += " · api key missing"
			available = false
		} else if p.AuthRequired {
			detail += " · key set"
		}
		models := append([]string(nil), p.Models...)
		if p.Model != "" && !contains(models, p.Model) {
			models = append([]string{p.Model}, models...)
		}
		out = append(out, ProviderSummary{
			ID:        p.ID,
			Name:      p.Label,
			Detail:    detail,
			Models:    models,
			Available: available,
		})
	}
	return out
}

func capsSuffix(caps providers.Capabilities) string {
	if detail := caps.Detail(); detail != "" {
		return " · " + detail
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// ToolSummaries returns one "name: description" line per registered
// tool, in registration order, for /tools-style reporting.
func (r *Runtime) ToolSummaries() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	manager := r.manager
	r.mu.RUnlock()
	if manager == nil {
		return nil
	}
	defs := manager.Definitions()
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name+": "+d.Description)
	}
	return out
}

// Skills returns a copy of the global skill catalog, sorted deterministically.
func (r *Runtime) Skills() []skills.Skill {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	store := r.skills
	r.mu.RUnlock()
	if store == nil {
		return nil
	}
	return store.Catalog()
}

// LoadSkill returns the Markdown body for a skill id.
func (r *Runtime) LoadSkill(id string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("skill %q: %w", id, skills.ErrSkillNotFound)
	}
	r.mu.RLock()
	store := r.skills
	r.mu.RUnlock()
	if store == nil {
		return "", fmt.Errorf("skill %q: %w", id, skills.ErrSkillNotFound)
	}
	return store.Load(id)
}

// SetModel switches the active model, keeping the current provider and
// endpoint, and takes effect starting with the next request. The change is
// in-memory only; it does not write config.yaml. This matches AGENTS.md's
// contract that runtime switching is temporary unless explicitly persisted,
// avoids silently stripping user comments via yaml.Marshal, and keeps the
// TUI picker fast and non-destructive. Call SaveConfig to persist.
func (r *Runtime) SetModel(name string) error {
	if r == nil {
		return fmt.Errorf("runtime not available")
	}
	if name == "" {
		return fmt.Errorf("model name cannot be empty")
	}
	r.mu.RLock()
	if r.cfg == nil {
		r.mu.RUnlock()
		return fmt.Errorf("no config to switch model")
	}
	cfgCopy := *r.cfg
	r.mu.RUnlock()
	cfgCopy.Model.Name = name
	provider, err := newProvider(&cfgCopy)
	if err != nil {
		return err
	}
	var authReq bool
	var authEnv string
	if resolved, rerr := cfgCopy.ResolveProvider(cfgCopy.Model.Provider, cfgCopy.Model.Name); rerr == nil {
		authReq, authEnv = resolved.AuthRequired, resolved.AuthEnvVar
	}
	r.mu.Lock()
	r.cfg = &cfgCopy
	r.provider = provider
	r.authRequired, r.authEnvVar = authReq, authEnv
	r.mu.Unlock()
	r.applyReasoning()
	return nil
}

// SetProvider switches the active provider, keeping the current model
// name, and takes effect starting with the next request. If the provider
// has a known default endpoint (from its configuration entry or the
// built-in catalog), it is adopted too, so switching providers never
// leaves the endpoint pointed at the previous one. The change is in-memory
// only; it does not write config.yaml. See SetModel for rationale.
func (r *Runtime) SetProvider(name string) error {
	if r == nil {
		return fmt.Errorf("runtime not available")
	}
	if name == "" {
		return fmt.Errorf("provider name cannot be empty")
	}
	r.mu.RLock()
	if r.cfg == nil {
		r.mu.RUnlock()
		return fmt.Errorf("no config to switch provider")
	}
	cfgCopy := *r.cfg
	r.mu.RUnlock()
	resolved, err := cfgCopy.ResolveProvider(name, cfgCopy.Model.Name)
	if err != nil {
		return err
	}
	if resolved.AuthRequired && resolved.APIKey == "" {
		return fmt.Errorf(
			"%s requires an API key - set %s in your environment or .env file and restart Forcefield",
			resolved.Label, resolved.AuthEnvVar,
		)
	}
	cfgCopy.Model.Provider = name
	cfgCopy.Model.Endpoint = resolved.BaseURL
	provider, err := providers.DefaultFactories().Create(resolved.Spec(cfgCopy.Model.Name))
	if err != nil {
		// Multi-protocol routers reject models outside their catalog, but
		// the current model name still belongs to the previous provider
		// at this point. Fall back to an unconfigured router so the
		// switch succeeds and the model picker can open; any turn before
		// a model is picked fails locally with guidance instead of
		// sending a request down the wrong protocol.
		if !errors.Is(err, providers.ErrUnknownModel) {
			return fmt.Errorf("create provider %q: %w", name, err)
		}
		emptySpec := resolved.Spec("")
		emptySpec.Model = ""
		provider, err = providers.DefaultFactories().Create(emptySpec)
		if err != nil {
			return fmt.Errorf("create provider %q: %w", name, err)
		}
	}
	var authReq bool
	var authEnv string
	if resolved, rerr := cfgCopy.ResolveProvider(cfgCopy.Model.Provider, cfgCopy.Model.Name); rerr == nil {
		authReq, authEnv = resolved.AuthRequired, resolved.AuthEnvVar
	}
	r.mu.Lock()
	r.cfg = &cfgCopy
	r.provider = provider
	r.authRequired, r.authEnvVar = authReq, authEnv
	r.mu.Unlock()
	r.applyReasoning()
	return nil
}

// SaveConfig persists the current in-memory Config to config.yaml atomically.
// Runtime switching via SetModel/SetProvider is temporary by default; call
// this explicitly when the user opts into persistence (e.g. /save or
// ff config). It never writes secrets.
func (r *Runtime) SaveConfig() error {
	if r == nil {
		return fmt.Errorf("no config to save")
	}
	r.mu.RLock()
	cfg := r.cfg
	r.mu.RUnlock()
	if cfg == nil {
		return fmt.Errorf("no config to save")
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// reasoningKey returns the map key for per-model reasoning storage.
func reasoningKey(provider, model string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "\x00" + strings.ToLower(strings.TrimSpace(model))
}

// CurrentReasoningCapabilities returns the capabilities for the active model.
func (r *Runtime) CurrentReasoningCapabilities() providers.ReasoningCapabilities {
	if r == nil {
		return providers.ReasoningCapabilities{}
	}
	r.mu.RLock()
	if r.cfg == nil {
		r.mu.RUnlock()
		return providers.ReasoningCapabilities{}
	}
	provider, model := r.cfg.Model.Provider, r.cfg.Model.Name
	r.mu.RUnlock()
	return providers.ModelReasoningCapabilities(provider, model)
}

// currentModelKey copies the active provider/model key under RLock.
func (r *Runtime) currentModelKey() string {
	if r == nil {
		return reasoningKey("", "")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.cfg == nil {
		return reasoningKey("", "")
	}
	return reasoningKey(r.cfg.Model.Provider, r.cfg.Model.Name)
}

// CurrentEffort returns the selected effort for the active model, or empty
// when none is set or the model does not support effort.
func (r *Runtime) CurrentEffort() string {
	if r == nil {
		return ""
	}
	caps := r.CurrentReasoningCapabilities()
	if caps.Effort == nil {
		return ""
	}
	key := r.currentModelKey()
	r.reasoningMu.RLock()
	cfg, ok := r.reasoningSelections[key]
	r.reasoningMu.RUnlock()
	if !ok {
		return ""
	}
	// Only return if still valid for current capability (levels may have changed).
	if err := caps.ValidateEffort(cfg.Effort); err != nil {
		return ""
	}
	return cfg.Effort
}

// SetEffort validates and stores the effort level for the active model.
func (r *Runtime) SetEffort(level string) error {
	if r == nil {
		return fmt.Errorf("runtime not available")
	}
	caps := r.CurrentReasoningCapabilities()
	if err := caps.ValidateEffort(level); err != nil {
		return err
	}
	canonical := caps.CanonicalEffort(level)
	key := r.currentModelKey()
	r.reasoningMu.Lock()
	if r.reasoningSelections == nil {
		r.reasoningSelections = make(map[string]providers.ReasoningConfig)
	}
	cfg := r.reasoningSelections[key]
	cfg.Effort = canonical
	r.reasoningSelections[key] = cfg
	r.reasoningMu.Unlock()
	r.applyReasoning()
	return nil
}

// CurrentThinking returns the thinking config for the active model, or nil.
func (r *Runtime) CurrentThinking() *providers.ThinkingConfig {
	if r == nil {
		return nil
	}
	caps := r.CurrentReasoningCapabilities()
	if caps.Thinking == nil {
		return nil
	}
	key := r.currentModelKey()
	r.reasoningMu.RLock()
	cfg, ok := r.reasoningSelections[key]
	r.reasoningMu.RUnlock()
	if !ok || cfg.Thinking == nil {
		return nil
	}
	if err := caps.ValidateThinking(*cfg.Thinking); err != nil {
		return nil
	}
	deep := providers.ThinkingConfig{Level: cfg.Thinking.Level}
	if cfg.Thinking.Enabled != nil {
		v := *cfg.Thinking.Enabled
		deep.Enabled = &v
	}
	if cfg.Thinking.Budget != nil {
		v := *cfg.Thinking.Budget
		deep.Budget = &v
	}
	return &deep
}

// SetThinking validates and stores the thinking config for the active model.
func (r *Runtime) SetThinking(tc providers.ThinkingConfig) error {
	if r == nil {
		return fmt.Errorf("runtime not available")
	}
	caps := r.CurrentReasoningCapabilities()
	if err := caps.ValidateThinking(tc); err != nil {
		return err
	}
	// Canonicalize enum level casing.
	if caps.Thinking != nil && caps.Thinking.Kind == providers.ThinkingKindEnum && tc.Level != "" {
		tc.Level = caps.CanonicalThinkingLevel(tc.Level)
	}
	// Deep copy pointers to avoid aliasing the caller's variable.
	deep := providers.ThinkingConfig{Level: tc.Level}
	if tc.Enabled != nil {
		v := *tc.Enabled
		deep.Enabled = &v
	}
	if tc.Budget != nil {
		v := *tc.Budget
		deep.Budget = &v
	}
	key := r.currentModelKey()
	r.reasoningMu.Lock()
	if r.reasoningSelections == nil {
		r.reasoningSelections = make(map[string]providers.ReasoningConfig)
	}
	cfg := r.reasoningSelections[key]
	cfg.Thinking = &deep
	r.reasoningSelections[key] = cfg
	r.reasoningMu.Unlock()
	r.applyReasoning()
	return nil
}

// ClearThinking removes thinking configuration for the active model.
func (r *Runtime) ClearThinking() {
	if r == nil {
		return
	}
	key := r.currentModelKey()
	r.reasoningMu.Lock()
	if cfg, ok := r.reasoningSelections[key]; ok {
		cfg.Thinking = nil
		r.reasoningSelections[key] = cfg
	}
	r.reasoningMu.Unlock()
	r.applyReasoning()
}

// ToggleThinking flips the boolean thinking state for models with bool kind.
func (r *Runtime) ToggleThinking() (bool, error) {
	caps := r.CurrentReasoningCapabilities()
	if caps.Thinking == nil || caps.Thinking.Kind != providers.ThinkingKindBool {
		return false, fmt.Errorf("Current model does not support thinking toggle.")
	}
	cur := r.CurrentThinking()
	enabled := true
	if cur != nil && cur.Enabled != nil {
		enabled = !*cur.Enabled
	}
	tc := providers.ThinkingConfig{Enabled: &enabled}
	if err := r.SetThinking(tc); err != nil {
		return false, err
	}
	return enabled, nil
}

// applyReasoning filters the stored per-model reasoning config through the
// current model's capabilities and pushes it to the provider adapter.
func (r *Runtime) applyReasoning() {
	if r == nil {
		return
	}
	r.mu.RLock()
	provider := r.provider
	var provName, modelName string
	if r.cfg != nil {
		provName, modelName = r.cfg.Model.Provider, r.cfg.Model.Name
	}
	r.mu.RUnlock()
	r.applyReasoningTo(provider, provName, modelName)
}

// applyReasoningTo is applyReasoning against an explicit snapshot so a run
// uses one consistent view even if a switch lands mid-run.
func (r *Runtime) applyReasoningTo(provider providers.ModelProvider, provName, modelName string) {
	if r == nil || provider == nil {
		return
	}
	caps := providers.ModelReasoningCapabilities(provName, modelName)
	key := reasoningKey(provName, modelName)
	r.reasoningMu.RLock()
	stored, ok := r.reasoningSelections[key]
	r.reasoningMu.RUnlock()
	effective := providers.ReasoningConfig{}
	if ok {
		if caps.Effort != nil && stored.Effort != "" {
			if caps.ValidateEffort(stored.Effort) == nil {
				effective.Effort = stored.Effort
			}
		}
		if caps.Thinking != nil && stored.Thinking != nil {
			if caps.ValidateThinking(*stored.Thinking) == nil {
				deep := providers.ThinkingConfig{Level: stored.Thinking.Level}
				if stored.Thinking.Enabled != nil {
					v := *stored.Thinking.Enabled
					deep.Enabled = &v
				}
				if stored.Thinking.Budget != nil {
					v := *stored.Thinking.Budget
					deep.Budget = &v
				}
				effective.Thinking = &deep
			}
		}
	}
	if p, ok := provider.(providers.ReasoningAware); ok {
		p.SetReasoning(effective)
	}
}

// newPolicy builds the sandbox policy for the current project workspace.
func newPolicy(cfg *config.Config) (sandbox.Policy, error) {
	mode, err := sandbox.ParseMode(cfg.Sandbox.Mode)
	if err != nil {
		return sandbox.Policy{}, fmt.Errorf("invalid sandbox.mode: %w", err)
	}
	network, err := sandbox.ParseNetwork(cfg.Sandbox.WSL.Network)
	if err != nil {
		return sandbox.Policy{}, fmt.Errorf("invalid sandbox.wsl.network: %w", err)
	}
	return sandbox.Policy{
		Mode:      mode,
		Workspace: projectWorkspace(),
		Distro:    cfg.Sandbox.WSL.Distribution,
		Network:   network,
	}, nil
}

// newExecutor constructs the sandbox executor for shell commands from the
// configured sandbox section and the current project root. Construction
// fails loudly for unsupported combinations (e.g. wsl mode on Unix);
// there is never a silent fallback to a weaker backend.
func newExecutor(cfg *config.Config) (sandbox.Executor, error) {
	policy, err := newPolicy(cfg)
	if err != nil {
		return nil, err
	}
	executor, err := sandbox.NewExecutor(policy)
	if err != nil {
		return nil, fmt.Errorf("create %s executor (sandbox.mode = %q): %w", policy.Mode, cfg.Sandbox.Mode, err)
	}
	return executor, nil
}

// projectWorkspace resolves the directory shell commands treat as their
// working context: the Git repository root when the process runs inside
// one, otherwise the current directory. Failures fall back to "" so the
// executor resolves per-request instead of failing startup.
func projectWorkspace() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	root, err := memory.ProjectRoot(cwd)
	if err != nil {
		return cwd
	}
	return root
}

func (r *Runtime) buildMessages(history []providers.Message) []providers.Message {
	if r == nil {
		return append([]providers.Message{{Role: providers.SystemRole}}, history...)
	}
	r.mu.RLock()
	agent := r.agent
	r.mu.RUnlock()
	return buildMessagesWithAgent(history, agent)
}

// buildMessagesWithAgent builds the bounded history window for an explicit
// agent snapshot so background runs never read live switchable state.
func buildMessagesWithAgent(history []providers.Message, agent *agent.Agent) []providers.Message {
	prompt := ""
	if agent != nil {
		prompt = agent.BuildSystemPrompt()
	}
	system := providers.Message{
		Role:    providers.SystemRole,
		Content: prompt,
	}
	if len(history) <= maxContextMessages {
		messages := make([]providers.Message, 0, len(history)+1)
		messages = append(messages, system)
		messages = append(messages, history...)
		return messages
	}
	// History exceeds window: preserve the first user message (task goal)
	// plus the most recent tail. This is a conservative mitigation, not a
	// full summarization, but it bounds growth.
	goalIdx := -1
	for i, m := range history {
		if m.Role == providers.UserRole && strings.TrimSpace(m.Content) != "" {
			goalIdx = i
			break
		}
	}
	keepGoal := goalIdx >= 0 && goalIdx < len(history)-maxContextMessages
	tailSize := maxContextMessages
	if keepGoal {
		tailSize = maxContextMessages - 1
	}
	start := len(history) - tailSize
	if start < 0 {
		start = 0
	}
	out := make([]providers.Message, 0, 1+maxContextMessages+1)
	out = append(out, system)
	if keepGoal {
		out = append(out, history[goalIdx])
	}
	out = append(out, history[start:]...)
	return out
}

// runSnapshot is one consistent view of the switchable run state. It is
// captured under RLock at StreamChat entry so SetAgent / SetModel /
// SetProvider take effect on the next StreamChat, never mid-turn.
type runSnapshot struct {
	agent        *agent.Agent
	manager      *tools.Manager
	provider     providers.ModelProvider
	scheduler    *scheduler
	limits       Limits
	authRequired bool
	authEnvVar   string
	providerName string
	modelName    string
}

// snapshotRunState copies the switchable run state under RLock.
func (r *Runtime) snapshotRunState() runSnapshot {
	var snap runSnapshot
	if r == nil {
		return snap
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	snap.agent = r.agent
	snap.manager = r.manager
	snap.provider = r.provider
	snap.scheduler = r.scheduler
	snap.limits = r.limits
	snap.authRequired = r.authRequired
	snap.authEnvVar = r.authEnvVar
	if r.cfg != nil {
		snap.providerName = r.cfg.Model.Provider
		snap.modelName = r.cfg.Model.Name
	}
	return snap
}

// StreamChat runs the agent loop and emits structured events.
func (r *Runtime) StreamChat(ctx context.Context, messages []providers.Message) (<-chan Event, error) {
	if ctx == nil {
		return nil, fmt.Errorf("stream context cannot be nil")
	}
	if r == nil {
		return nil, fmt.Errorf("runtime not available")
	}

	snap := r.snapshotRunState()
	initial := buildMessagesWithAgent(messages, snap.agent)
	events := make(chan Event)
	go func() {
		defer close(events)
		r.run(ctx, initial, func(event Event) bool {
			select {
			case events <- event:
				return true
			case <-ctx.Done():
				return false
			}
		}, snap)
	}()

	return events, nil
}

// Stream is kept as a compatibility alias for StreamChat.
func (r *Runtime) Stream(ctx context.Context, messages []providers.Message) (<-chan Event, error) {
	return r.StreamChat(ctx, messages)
}

// Run executes the agent loop and returns its final response.
func (r *Runtime) Run(messages []providers.Message) (providers.Response, error) {
	return r.RunContext(context.Background(), messages)
}

// RunContext executes Run with caller-controlled cancellation.
func (r *Runtime) RunContext(ctx context.Context, messages []providers.Message) (providers.Response, error) {
	events, err := r.StreamChat(ctx, messages)
	if err != nil {
		return providers.Response{}, err
	}

	for event := range events {
		switch event.Type {
		case EventDone:
			if event.Response == nil {
				return providers.Response{}, fmt.Errorf("runtime completed without a response")
			}
			return *event.Response, nil
		case EventError:
			return providers.Response{}, event.Err
		case EventBlocked:
			return providers.Response{}, fmt.Errorf("task blocked: %w", event.Err)
		}
	}

	if err := ctx.Err(); err != nil {
		return providers.Response{}, err
	}
	return providers.Response{}, fmt.Errorf("runtime stopped without a completion event")
}

// maxToolResultChars keeps verbose tool output from dominating later turns.
const maxToolResultChars = 6000

// run executes the persistent agent loop and enforces runtime limits.
func (r *Runtime) run(ctx context.Context, messages []providers.Message, emit func(Event) bool, snap runSnapshot) {
	state := task.New(goalFrom(messages))
	ctx = task.WithState(ctx, state)

	limits := snap.limits

	for {
		iteration := state.BeginIteration()
		if limits.MaxIterations > 0 && iteration > limits.MaxIterations {
			r.emitBlocked(emit, state, fmt.Sprintf("stopped after %d iterations (maximum reached)", limits.MaxIterations))
			return
		}

		refreshSystemPrompt(messages, snap.agent, state)

		response, err := r.runModelTurn(ctx, messages, emit, snap)
		if err != nil {
			emit(Event{Type: EventError, Err: err, TaskState: snapshotPtr(state)})
			return
		}

		if len(response.ToolCalls) == 0 {
			// Length exhaustion is incomplete, not success. If the model
			// stopped because it hit its max tokens, the answer is truncated
			// and the task should be marked blocked rather than done.
			if response.StopReason == providers.FinishLength {
				r.emitBlocked(emit, state, "model output truncated due to length limit (finish_reason=length) - response incomplete")
				return
			}
			status := state.FinalStatus()
			state.SetStatus(status)
			emit(Event{Type: EventDone, Response: &response, Status: status, TaskState: snapshotPtr(state)})
			return
		}

		if limits.MaxToolCalls > 0 && state.ToolCallCount()+len(response.ToolCalls) > limits.MaxToolCalls {
			r.emitBlocked(emit, state, fmt.Sprintf("stopped after %d tool calls (maximum reached)", limits.MaxToolCalls))
			return
		}

		messages = append(messages, providers.Message{
			Role:      providers.AssistantRole,
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		})

		if snap.scheduler == nil || snap.manager == nil {
			emit(Event{Type: EventError, Err: fmt.Errorf("tool system not initialized"), TaskState: snapshotPtr(state)})
			return
		}
		results := snap.scheduler.RunWithManager(ctx, response.ToolCalls, emit, snap.manager)

		if ctx.Err() != nil {
			emit(Event{Type: EventError, Err: ctx.Err(), TaskState: snapshotPtr(state)})
			return
		}

		for i, tc := range response.ToolCalls {
			result := results[i]
			state.RecordTool(tc.Name, result.Success)
			content := truncateToolResult(result.Content)
			content = session.ScrubContent(content)
			content = session.FenceToolResult(tc.Name, content)
			messages = append(messages, providers.Message{
				Role:       providers.ToolRole,
				Name:       tc.Name,
				Content:    content,
				ToolCallID: tc.ID,
			})
		}

		if limits.MaxConsecutiveFailures > 0 && state.ConsecutiveFailures() >= limits.MaxConsecutiveFailures {
			r.emitBlocked(emit, state, fmt.Sprintf(
				"stopped after %d consecutive tool failures - the agent appears stuck", state.ConsecutiveFailures()))
			return
		}
	}
}

// emitBlocked reports a runtime-enforced stop with the final task snapshot.
func (r *Runtime) emitBlocked(emit func(Event) bool, state *task.State, reason string) {
	state.SetStatus(task.StatusBlocked)
	emit(Event{
		Type:      EventBlocked,
		Status:    task.StatusBlocked,
		TaskState: snapshotPtr(state),
		Err:       fmt.Errorf("%s", reason),
	})
}

func snapshotPtr(state *task.State) *task.Snapshot {
	snap := state.Snapshot()
	return &snap
}

// refreshSystemPrompt adds the current task digest to the system message.
func refreshSystemPrompt(messages []providers.Message, a *agent.Agent, state *task.State) {
	if len(messages) == 0 || messages[0].Role != providers.SystemRole {
		return
	}
	if a == nil {
		return
	}

	base := a.BuildSystemPrompt()
	summary := state.Summary()
	if summary == "" {
		messages[0].Content = base
		return
	}

	messages[0].Content = base + "\n\n## Current Task State\n\n" + summary +
		"\n\nUpdate this via update_task_state as your understanding of the task evolves."
}

func truncateToolResult(content string) string {
	if len(content) <= maxToolResultChars {
		return content
	}

	// Cut on a rune boundary so multi-byte characters at the limit are
	// never split into invalid UTF-8, which would poison every later
	// provider request that replays this tool result.
	cut := 0
	for i, r := range content {
		if i+utf8.RuneLen(r) > maxToolResultChars {
			break
		}
		cut = i + utf8.RuneLen(r)
	}
	if cut == 0 {
		cut = 1
	}

	return fmt.Sprintf("%s\n\n[...output truncated at %d bytes, %d bytes total. Re-run with narrower output (e.g. filters, -run, grep) if you need the rest.]",
		content[:cut], cut, len(content))
}

// goalFrom returns the first user message for task-state display.
func goalFrom(messages []providers.Message) string {
	for _, m := range messages {
		if m.Role == providers.UserRole && m.Content != "" {
			return m.Content
		}
	}
	return ""
}

// runModelTurn streams and assembles one provider response.
func (r *Runtime) runModelTurn(ctx context.Context, messages []providers.Message, emit func(Event) bool, snap runSnapshot) (providers.Response, error) {
	if !emit(Event{Type: EventThinking}) {
		return providers.Response{}, context.Canceled
	}

	if err := checkAuthWithSnapshot(snap.authRequired, snap.authEnvVar, snap.providerName); err != nil {
		return providers.Response{}, err
	}

	r.applyReasoningTo(snap.provider, snap.providerName, snap.modelName)
	if snap.provider == nil {
		return providers.Response{}, fmt.Errorf("model provider not initialized")
	}
	var defs []tools.Definition
	if snap.manager != nil {
		defs = snap.manager.Definitions()
	}
	stream, err := snap.provider.StreamChat(ctx, messages, defs)
	if err != nil {
		return providers.Response{}, fmt.Errorf("model call failed: %w", err)
	}

	var response providers.Response
	for event := range stream {
		if event.Err != nil {
			return providers.Response{}, fmt.Errorf("model stream failed: %w", event.Err)
		}

		if event.Thinking != "" {
			if !emit(Event{Type: EventThinking, Thinking: event.Thinking}) {
				return providers.Response{}, context.Canceled
			}
		}

		if event.Text != "" {
			response.Content += event.Text
			if !emit(Event{Type: EventText, Text: event.Text}) {
				return providers.Response{}, context.Canceled
			}
		}

		response.ToolCalls = append(response.ToolCalls, event.ToolCalls...)
		if event.Usage != nil {
			response.Usage = *event.Usage
		}
		if event.StopReason != providers.FinishNone {
			response.StopReason = event.StopReason
		}
	}

	if err := ctx.Err(); err != nil {
		return providers.Response{}, err
	}
	return response, nil
}

func Run(messages []providers.Message) (providers.Response, error) {
	rt, err := New()
	if err != nil {
		return providers.Response{}, fmt.Errorf("create runtime: %w", err)
	}

	return rt.Run(messages)
}

// newProvider constructs the configured model provider through the
// provider registry: configuration resolves to a Spec, the registry picks
// the adapter that speaks that wire protocol. The runtime itself never
// branches on which provider is active.
func newProvider(cfg *config.Config) (providers.ModelProvider, error) {
	return ProviderFor(cfg, cfg.Model.Provider)
}

// ProviderFor resolves one configured provider and builds it via the
// registry. It fails with actionable messages for unknown types and
// malformed endpoints; a missing API key is tolerated here so Forcefield
// still starts (the model turn fails with guidance instead).
func ProviderFor(cfg *config.Config, id string) (providers.ModelProvider, error) {
	resolved, err := cfg.ResolveProvider(id, cfg.Model.Name)
	if err != nil {
		return nil, err
	}
	provider, err := providers.DefaultFactories().Create(resolved.Spec(cfg.Model.Name))
	if err != nil {
		// Same stale-model case as SetProvider: a configured model from
		// another provider must not prevent startup. Fall back to an
		// unconfigured router; the model picker offers valid models and
		// any turn before that fails locally with guidance.
		if !errors.Is(err, providers.ErrUnknownModel) {
			return nil, fmt.Errorf("create provider %q: %w", id, err)
		}
		emptySpec := resolved.Spec("")
		emptySpec.Model = ""
		provider, err = providers.DefaultFactories().Create(emptySpec)
		if err != nil {
			return nil, fmt.Errorf("create provider %q: %w", id, err)
		}
	}
	return provider, nil
}
