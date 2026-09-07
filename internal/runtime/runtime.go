// Package runtime coordinates the agent loop and its dependencies.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/memory"
	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/redact"
	"forcefield/internal/sandbox"
	"forcefield/internal/session"
	"forcefield/internal/skills"
	"forcefield/internal/task"
	"forcefield/internal/tools"
	"forcefield/internal/tools/builtin"
	"forcefield/internal/trace"

	"github.com/google/uuid"
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
	// runMu serializes agent loops for this Runtime. A cancelled run can
	// still be waiting for a provider or process to acknowledge context
	// cancellation; starting the next prompt only after that cleanup avoids
	// overlapping tool executions against the same session/runtime state.
	runMu sync.Mutex

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
	// workspaceRoot is the resolved project root every filesystem tool
	// and the shell executor are scoped to (in confining modes).
	// workspaceCwd is the process working directory at construction:
	// the permissive-mode anchor and the base explicit relative roots
	// resolve against. Both are fixed for the runtime lifetime.
	workspaceRoot string
	workspaceCwd  string
	// tracer records the local-only execution trace when enabled in
	// config. Nil-safe: a nil or disabled tracer records nothing.
	tracer *trace.Tracer
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

	fullManager, err := builtin.NewManager(
		builtin.WithExecutor(executor),
		builtin.WithPolicy(policy),
		builtin.WithLimits(toolLimitsFromConfig(cfg)),
	)
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

	cwd, _ := os.Getwd()
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
		workspaceRoot:       policy.Workspace,
		workspaceCwd:        cwd,
		tracer:              trace.New(cfg.Tracing.Enabled, cfg.Tracing.Dir),
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

// toolLimitsFromConfig converts the optional tools: overrides into the
// shared limits form. Absent entries resolve to each tool's default at
// use time, so existing configs behave exactly as before.
func toolLimitsFromConfig(cfg *config.Config) map[string]tools.Limits {
	if cfg == nil || len(cfg.Tools) == 0 {
		return nil
	}
	out := make(map[string]tools.Limits, len(cfg.Tools))
	for name, lim := range cfg.Tools {
		out[name] = tools.Limits{
			MaxBytes: lim.MaxBytes,
			MaxLines: lim.MaxLines,
			Timeout:  time.Duration(lim.TimeoutSeconds * float64(time.Second)),
		}
	}
	return out
}

// limitsForAgent overlays one agent profile's run bounds onto the
// global base: positive profile values win, everything else keeps the
// base. A nil config or unknown agent leaves the base untouched, so
// existing behavior is preserved unless a profile opts in.
func limitsForAgent(cfg *config.Config, agentName string, base Limits) Limits {
	if cfg == nil {
		return base
	}
	o, ok := cfg.Agents[agentName]
	if !ok {
		return base
	}
	out := base
	if o.MaxIterations > 0 {
		out.MaxIterations = o.MaxIterations
	}
	if o.MaxToolCalls > 0 {
		out.MaxToolCalls = o.MaxToolCalls
	}
	if o.MaxConsecutiveFailures > 0 {
		out.MaxConsecutiveFailures = o.MaxConsecutiveFailures
	}
	return out
}

// contextBudgetFromConfig builds the per-turn context budget for one
// agent profile: profile settings win, then the global agent.* block,
// then negotiated provider capabilities, then the static model table,
// then conservative defaults. Non-positive values fall through each
// layer, so existing configs keep working unchanged.
func contextBudgetFromConfig(cfg *config.Config, modelName, agentName string, caps providers.Capabilities) ContextBudget {
	limit, reserve, maxMsg := cfg.Agent.ContextWindow, cfg.Agent.ContextReserve, cfg.Agent.MaxContextMessages
	summarize := cfg.Agent.ContextSummary
	if o, ok := cfg.Agents[agentName]; ok {
		if o.ContextWindow > 0 {
			limit = o.ContextWindow
		}
		if o.ContextReserve > 0 {
			reserve = o.ContextReserve
		}
		if o.MaxContextMessages > 0 {
			maxMsg = o.MaxContextMessages
		}
		if o.ContextSummary != nil {
			summarize = *o.ContextSummary
		}
	}
	return BudgetForCaps(modelName, limit, reserve, maxMsg, summarize, caps)
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

// WorkspaceRoot returns the resolved project root filesystem tools and
// the shell executor are scoped to in confining modes.
func (r *Runtime) WorkspaceRoot() string {
	if r == nil {
		return ""
	}
	return r.workspaceRoot
}

// WorkspaceCwd returns the process working directory captured at
// construction: the permissive-mode anchor.
func (r *Runtime) WorkspaceCwd() string {
	if r == nil {
		return ""
	}
	return r.workspaceCwd
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

// newPolicy builds the sandbox policy for the resolved workspace root.
// The root and strict flag come from ResolveWorkspace/workspace.mode, so
// filesystem tools and the shell executor share one boundary.
func newPolicy(cfg *config.Config) (sandbox.Policy, error) {
	mode, err := sandbox.ParseMode(cfg.Sandbox.Mode)
	if err != nil {
		return sandbox.Policy{}, fmt.Errorf("invalid sandbox.mode: %w", err)
	}
	network, err := sandbox.ParseNetwork(cfg.Sandbox.WSL.Network)
	if err != nil {
		return sandbox.Policy{}, fmt.Errorf("invalid sandbox.wsl.network: %w", err)
	}
	root, err := ResolveWorkspace(cfg)
	if err != nil {
		return sandbox.Policy{}, err
	}
	return sandbox.Policy{
		Mode:      mode,
		Workspace: root,
		Strict:    cfg.Workspace.Mode == config.WorkspaceStrict,
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

// ResolveWorkspace resolves the effective workspace root for cfg. An
// explicit workspace.root wins (absolute, or relative to the startup
// directory, and it must exist). Otherwise the root is the Git
// top-level when the process runs inside a repository, else the current
// working directory. This is the single resolution point: the runtime
// calls it once at construction and both the tool policy and the shell
// executor inherit the result, so no second resolver can disagree.
//
// An explicit root that does not exist is an error (fail fast with a
// named path); the fallback chain itself never errors on missing git.
func ResolveWorkspace(cfg *config.Config) (string, error) {
	if cfg != nil && strings.TrimSpace(cfg.Workspace.Root) != "" {
		return resolveExplicitRoot(cfg.Workspace.Root)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	root, err := memory.ProjectRoot(cwd)
	if err != nil {
		return cwd, nil
	}
	return root, nil
}

// resolveExplicitRoot absolutizes a configured root (relative roots
// anchor at the startup directory) and requires it to exist.
func resolveExplicitRoot(root string) (string, error) {
	orig := root
	if !filepath.IsAbs(root) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve workspace.root %q: %w", orig, err)
		}
		root = filepath.Join(cwd, root)
	}
	abs := filepath.Clean(root)
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("workspace.root %q: %w", orig, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace.root %q is not a directory", orig)
	}
	// Canonicalize symlinks/junctions once so containment checks and
	// doctor reporting use one stable spelling.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

func (r *Runtime) buildMessages(history []providers.Message) []providers.Message {
	if r == nil {
		return append([]providers.Message{{Role: providers.SystemRole}}, history...)
	}
	r.mu.RLock()
	agent := r.agent
	cfg := r.cfg
	provider := r.provider
	activeAgent := r.activeAgent
	r.mu.RUnlock()
	budget := DefaultContextBudget()
	if cfg != nil {
		budget = contextBudgetFromConfig(cfg, cfg.Model.Name, activeAgent, providers.ResolveCapabilities(provider, cfg.Model.Name))
	}
	return buildMessagesWithBudget(history, agent, budget)
}

// buildMessagesWithAgent builds the bounded history window for an explicit
// agent snapshot so background runs never read live switchable state.
// It uses the default budget (message-count windowing, pair-aware); use
// buildMessagesWithBudget when a resolved per-model budget is available.
func buildMessagesWithAgent(history []providers.Message, agent *agent.Agent) []providers.Message {
	return buildMessagesWithBudget(history, agent, DefaultContextBudget())
}

// buildMessagesWithBudget is buildMessagesWithAgent with an explicit
// context budget: token-aware when the model's window is known, otherwise
// message-count windowing. It always preserves the system prompt, the
// first user message (task goal), recent turns, and tool call/result
// pairing, reserving space for the next model response.
func buildMessagesWithBudget(history []providers.Message, agent *agent.Agent, budget ContextBudget) []providers.Message {
	prompt := ""
	if agent != nil {
		prompt = agent.BuildSystemPrompt()
	}
	system := providers.Message{
		Role:    providers.SystemRole,
		Content: prompt,
	}
	out, _ := budget.SelectContext(system, history)
	return out
}

// windowForProvider splits a full run history (system at [0]) into the
// budgeted provider view for one turn. The full history is never mutated:
// callers keep appending to it while each model request stays within the
// snapshot's context budget.
func windowForProvider(messages []providers.Message, snap runSnapshot) []providers.Message {
	if len(messages) == 0 {
		return buildMessagesWithBudget(nil, snap.agent, snap.contextBudget)
	}
	if messages[0].Role != providers.SystemRole {
		return buildMessagesWithBudget(messages, snap.agent, snap.contextBudget)
	}
	out, _ := snap.contextBudget.SelectContext(messages[0], messages[1:])
	return out
}

// runSnapshot is one consistent view of the switchable run state. It is
// captured under RLock at StreamChat entry so SetAgent / SetModel /
// SetProvider take effect on the next StreamChat, never mid-turn.
type runSnapshot struct {
	agent         *agent.Agent
	manager       *tools.Manager
	provider      providers.ModelProvider
	scheduler     *scheduler
	limits        Limits
	contextBudget ContextBudget
	// caps holds the negotiated provider capabilities for this run
	// (transport features from the instance, limits from the model
	// table); capsReported distinguishes "known to lack" from
	// "too old to say" so unreported providers keep history.
	caps         providers.Capabilities
	capsReported bool
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
	// Capabilities negotiate independently of configuration: even a
	// config-less runtime reports what its provider can do.
	modelName := ""
	if r.cfg != nil {
		snap.providerName = r.cfg.Model.Provider
		snap.modelName = r.cfg.Model.Name
		modelName = r.cfg.Model.Name
	}
	snap.caps = providers.ResolveCapabilities(r.provider, modelName)
	snap.capsReported = providers.ReportsCapabilities(r.provider)
	if r.cfg != nil {
		snap.limits = limitsForAgent(r.cfg, r.activeAgent, snap.limits)
		snap.contextBudget = contextBudgetFromConfig(r.cfg, modelName, r.activeAgent, snap.caps)
	} else {
		snap.contextBudget = DefaultContextBudget()
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
	initial := buildMessagesWithBudget(messages, snap.agent, snap.contextBudget)
	// Local execution trace (P1.11): one run handle for the whole
	// StreamChat invocation. Nil when disabled or unopenable — every
	// trace call below is nil-safe, so tracing can never fail the run.
	var tr *trace.Run
	if r.tracer.Enabled() {
		tr = r.tracer.StartRun(uuid.NewString(), trace.RunMeta{
			Provider:     snap.providerName,
			Model:        snap.modelName,
			Agent:        r.CurrentAgent(),
			MessageCount: len(messages),
		})
	}
	// One terminal event can remain buffered after an interactive consumer
	// has detached on Ctrl+C. That lets the run goroutine finish cleanup
	// instead of blocking forever trying to report cancellation.
	events := make(chan Event, 1)
	go func() {
		defer close(events)
		if tr != nil {
			defer tr.Close()
		}
		r.runMu.Lock()
		defer r.runMu.Unlock()
		r.run(ctx, initial, func(event Event) bool {
			if event.Type == EventCancelled {
				// Cancellation is terminal and should not block teardown if the
				// TUI retired this stream already. Normal events retain strict
				// backpressure so consumers observe them in order.
				select {
				case events <- event:
					traceEvent(tr, event)
					return true
				default:
					return false
				}
			}
			select {
			case events <- event:
				// Record what the consumer observed: dropped
				// (cancelled) events never happened from its view.
				traceEvent(tr, event)
				return true
			case <-ctx.Done():
				return false
			}
		}, snap, tr)
	}()

	return events, nil
}

// traceEvent translates one observed runtime event into the local
// execution trace. Text, thinking, and progress deltas are deliberately
// skipped: turn boundaries plus tool start/outcome pairs carry the
// diagnostic signal without per-token volume.
func traceEvent(tr *trace.Run, e Event) {
	if tr == nil {
		return
	}
	switch e.Type {
	case EventToolStart:
		if e.ToolCall != nil {
			tr.ToolCall(e.ToolCall.ID, e.ToolCall.Name, e.ToolCall.Arguments)
		}
	case EventToolFinish, EventToolFailed, EventToolCancelled, EventToolDenied:
		if e.ToolResult == nil {
			return
		}
		res := e.ToolResult
		var exit *int
		if res.HasExitCode {
			v := res.ExitCode
			exit = &v
		}
		errText := ""
		if res.Err != nil {
			errText = res.Err.Error()
		}
		tr.ToolResult(res.ToolCallID, res.Name, traceToolStatus(e.Type), res.Success, res.Duration, res.Attempt, exit, res.Content, errText)
	case EventDone:
		finalLen := 0
		if e.Response != nil {
			finalLen = len(e.Response.Content)
		}
		tr.RunDone(string(e.Status), finalLen)
	case EventError:
		tr.RunError(e.Err)
	case EventCancelled:
		tr.RunError(e.Err)
	case EventBlocked:
		tr.RunBlocked(e.Err)
	}
}

func traceToolStatus(t EventType) string {
	switch t {
	case EventToolFinish:
		return "finish"
	case EventToolFailed:
		return "failed"
	case EventToolCancelled:
		return "cancelled"
	case EventToolDenied:
		return "denied"
	default:
		return "unknown"
	}
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
		case EventCancelled:
			if event.Err != nil {
				return providers.Response{}, event.Err
			}
			return providers.Response{}, context.Canceled
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
func (r *Runtime) run(ctx context.Context, messages []providers.Message, emit func(Event) bool, snap runSnapshot, tr *trace.Run) {
	state := task.New(goalFrom(messages))
	ctx = task.WithState(ctx, state)
	cleanup := tools.NewRunControl(ctx)
	ctx = tools.WithRunControl(ctx, cleanup)
	cancelled := func(err error) {
		cleanup.Wait()
		r.emitCancelled(emit, state, err)
	}
	if err := ctx.Err(); err != nil {
		cancelled(err)
		return
	}

	limits := snap.limits
	// executed records every tool call ID that ran in this run, with
	// its terminal outcome. A repeated ID reuses the recorded result
	// instead of executing again, so a provider retry or a model echo
	// can never duplicate a side-effecting tool call.
	executed := make(map[string]executedCall)
	loops := newLoopDetector()

	for {
		if err := ctx.Err(); err != nil {
			cancelled(err)
			return
		}
		iteration := state.BeginIteration()
		if limits.MaxIterations > 0 && iteration > limits.MaxIterations {
			r.emitBlocked(emit, state, fmt.Sprintf("stopped after %d iterations (maximum reached)", limits.MaxIterations))
			return
		}

		refreshSystemPrompt(messages, snap.agent, state)

		// Window the provider view every turn so conversation/tool
		// history can never grow past the model's context budget. The
		// full messages slice is retained for future windows; only
		// this turn's request is bounded.
		windowed := windowForProvider(messages, snap)
		turnStart := time.Now()
		tr.TurnStart(int64(iteration), len(windowed), estimateMessagesTokens(windowed))
		response, err := r.runModelTurn(ctx, windowed, emit, snap)
		if err != nil {
			tr.TurnError(int64(iteration), err, time.Since(turnStart))
			if cancellationErr(ctx, err) {
				cancelled(cancellationCause(ctx, err))
				return
			}
			emit(Event{Type: EventError, Err: err, TaskState: snapshotPtr(state)})
			return
		}
		tr.TurnEnd(int64(iteration), string(response.StopReason),
			response.Usage.PromptTokens, response.Usage.CompletionTokens,
			response.Usage.TotalTokens, len(response.ToolCalls), time.Since(turnStart))

		// Length exhaustion is incomplete, not success — with or without
		// tool calls. A length-truncated turn may carry partial argument
		// JSON; executing it would run an operation the model never
		// completed. Block without executing so partial output is never
		// confused with successful completion.
		if response.StopReason == providers.FinishLength {
			r.emitBlocked(emit, state, "model output truncated due to length limit (finish_reason=length) - response incomplete; retry with narrower tool output, fewer turns in context, or a model with a larger context window")
			return
		}
		if len(response.ToolCalls) == 0 {
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
		loopCalls, loopIndexes := freshCallsForLoopDetection(response.ToolCalls, executed)
		results := r.executeCalls(ctx, response.ToolCalls, emit, snap, executed)

		if ctx.Err() != nil {
			cancelled(ctx.Err())
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

		loopResults := make([]ToolResult, len(loopIndexes))
		for i, index := range loopIndexes {
			loopResults[i] = results[index]
		}
		if loops.Observe(loopCalls, loopResults) {
			r.emitBlocked(emit, state, "Agent stopped: repeated tool execution detected without meaningful progress.")
			return
		}

		if limits.MaxConsecutiveFailures > 0 && state.ConsecutiveFailures() >= limits.MaxConsecutiveFailures {
			r.emitBlocked(emit, state, fmt.Sprintf(
				"stopped after %d consecutive tool failures - the agent appears stuck", state.ConsecutiveFailures()))
			return
		}
	}
}

// freshCallsForLoopDetection excludes a provider replay of an already-seen
// call ID. executeCalls reuses that result without invoking a tool, so it is
// not repeated execution and cannot cause side effects. Fresh IDs (and calls
// without IDs) remain visible to the progress guard.
func freshCallsForLoopDetection(calls []providers.ToolCall, executed map[string]executedCall) ([]providers.ToolCall, []int) {
	fresh := make([]providers.ToolCall, 0, len(calls))
	indexes := make([]int, 0, len(calls))
	for i, call := range calls {
		if call.ID != "" {
			if _, exists := executed[call.ID]; exists {
				continue
			}
		}
		fresh = append(fresh, call)
		indexes = append(indexes, i)
	}
	return fresh, indexes
}

// executedCall is the recorded outcome of one tool call ID within a
// run: the result plus the terminal event kind originally emitted, so
// a repeated ID replays the identical observable outcome.
type executedCall struct {
	result   ToolResult
	terminal EventType
}

// executeCalls runs one batch of tool calls with run-scoped idempotency.
// Calls whose non-empty ID already executed reuse the recorded result
// (marked explicitly, never silently) and emit the same Start/terminal
// event pair, keeping transcript and session pairing intact. Calls
// without IDs always execute — without identity there is nothing safe
// to key on. Fresh calls run concurrently through the scheduler; the
// returned slice aligns with calls.
func (r *Runtime) executeCalls(ctx context.Context, calls []providers.ToolCall, emit func(Event) bool, snap runSnapshot, executed map[string]executedCall) []ToolResult {
	results := make([]ToolResult, len(calls))
	var fresh []providers.ToolCall
	var freshIdx []int
	for i := range calls {
		tc := calls[i]
		if tc.ID != "" {
			if prev, ok := executed[tc.ID]; ok {
				dup := prev.result
				dup.Content += "\n\n[note: this tool call repeats call " + tc.ID +
					" from earlier in the same run; the recorded result is reused and the tool was not executed again]"
				results[i] = dup
				call := tc
				emit(Event{Type: EventToolStart, ToolCall: &call})
				emit(Event{Type: prev.terminal, ToolResult: &dup})
				continue
			}
		}
		fresh = append(fresh, tc)
		freshIdx = append(freshIdx, i)
	}
	if len(fresh) > 0 {
		// Concurrency follows negotiated capabilities: providers that
		// report no parallel tool support run batches sequentially.
		// Unreported providers keep the configured concurrency.
		maxConc := snap.scheduler.cfg.MaxConcurrency
		if snap.capsReported && !snap.caps.ParallelToolCalls {
			maxConc = 1
		}
		terminals := make(map[string]EventType, len(fresh))
		watchEmit := func(e Event) bool {
			if e.ToolResult != nil {
				switch e.Type {
				case EventToolFinish, EventToolFailed, EventToolCancelled, EventToolDenied:
					terminals[e.ToolResult.ToolCallID] = e.Type
				}
			}
			return emit(e)
		}
		freshResults := snap.scheduler.runWithConcurrency(ctx, fresh, watchEmit, snap.manager, maxConc)
		for j, res := range freshResults {
			results[freshIdx[j]] = res
			id := fresh[j].ID
			if id == "" {
				continue
			}
			terminal, ok := terminals[id]
			if !ok {
				// No terminal event (e.g. cancelled before start, which
				// is deliberately eventless): record as cancelled so a
				// repeat can never execute what this run skipped.
				terminal = EventToolCancelled
			}
			executed[id] = executedCall{result: res, terminal: terminal}
		}
	}
	return results
}

// toolCallingAllowed reports whether tool definitions are offered this
// turn. Definitions are withheld only when the provider explicitly
// reports no tool support; unreported providers keep the historical
// behavior of receiving them.
func toolCallingAllowed(snap runSnapshot) bool {
	return !snap.capsReported || snap.caps.ToolCalling
}

// estimateMessagesTokens sums the context-budget token estimates for a
// provider-bound message window. Used for trace metadata only.
func estimateMessagesTokens(messages []providers.Message) int {
	total := 0
	for _, m := range messages {
		total += messageTokens(m)
	}
	return total
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

// emitCancelled preserves cancellation as a first-class terminal result.
// Providers may return context.Canceled directly or the caller's context
// may have already been cancelled; both are a user/run-control outcome, not
// a model-provider failure.
func (r *Runtime) emitCancelled(emit func(Event) bool, state *task.State, err error) {
	if err == nil {
		err = context.Canceled
	}
	emit(Event{
		Type:      EventCancelled,
		TaskState: snapshotPtr(state),
		Err:       err,
	})
}

func cancellationErr(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled)
}

func cancellationCause(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
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

// maxTurnRetries bounds turn-level provider retries on top of the first
// attempt: a clean transient failure (nothing emitted yet) is retried at
// most twice more, so a sustained outage fails after 3 attempts instead
// of stalling the run.
const maxTurnRetries = 2

// transientError marks a model-turn failure the runtime classifies as
// transient: a later turn (automatic or manual) may succeed. It preserves
// the wrapped chain so errors.Is/As callers keep working.
type transientError struct{ err error }

func (e *transientError) Error() string {
	return e.err.Error() + " (transient provider failure; retrying may succeed)"
}

func (e *transientError) Unwrap() error { return e.err }

// IsTransientError reports whether the runtime marked err as a transient
// provider failure.
func IsTransientError(err error) bool {
	var te *transientError
	return errors.As(err, &te)
}

// markTransient wraps err when the provider classifies it transient;
// anything else (auth, quota, invalid requests, cancellations) passes
// through unchanged.
func markTransient(err error) error {
	if err == nil || !providers.IsTransient(err) {
		return err
	}
	return &transientError{err: err}
}

// canRetryTurn reports whether a failed model turn may be retried
// without risking duplicate output: only a clean failure (no text,
// thinking, or tool calls emitted yet) that is transient, with attempts
// remaining and a live context. Anything already emitted must never be
// replayed — the TUI already showed it and the session may reference it.
func canRetryTurn(attempt int, emitted bool, err error, ctx context.Context) bool {
	if attempt >= maxTurnRetries {
		return false
	}
	if emitted {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	return providers.IsTransient(err)
}

// runModelTurn streams and assembles one provider response, retrying
// clean transient failures with bounded backoff. Retries never replay
// output and never reach tool execution (the scheduler runs only after a
// turn succeeds), so a retried request cannot duplicate tool calls or
// persist inconsistent state.
func (r *Runtime) runModelTurn(ctx context.Context, messages []providers.Message, emit func(Event) bool, snap runSnapshot) (providers.Response, error) {
	if err := checkAuthWithSnapshot(snap.authRequired, snap.authEnvVar, snap.providerName); err != nil {
		return providers.Response{}, err
	}

	r.applyReasoningTo(snap.provider, snap.providerName, snap.modelName)
	if snap.provider == nil {
		return providers.Response{}, fmt.Errorf("model provider not initialized")
	}
	var defs []tools.Definition
	if snap.manager != nil && toolCallingAllowed(snap) {
		defs = snap.manager.Definitions()
	}

	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return providers.Response{}, err
		}
		if !emit(Event{Type: EventThinking}) {
			return providers.Response{}, context.Canceled
		}
		resp, emitted, err := r.streamOneTurn(ctx, messages, emit, snap, defs)
		if err == nil {
			return resp, nil
		}
		if !canRetryTurn(attempt, emitted, err, ctx) {
			return providers.Response{}, markTransient(err)
		}
		delay := providers.TurnRetryDelay(attempt, err)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return providers.Response{}, ctx.Err()
		}
	}
}

// streamOneTurn streams and assembles a single provider attempt. It
// reports whether anything user-visible (thinking, text, or tool calls)
// was emitted, so the caller can decide whether a retry is safe.
func (r *Runtime) streamOneTurn(ctx context.Context, messages []providers.Message, emit func(Event) bool, snap runSnapshot, defs []tools.Definition) (providers.Response, bool, error) {
	stream, err := snap.provider.StreamChat(ctx, messages, defs)
	if err != nil {
		// Provider errors can echo request or response fragments;
		// scrub before the error is surfaced, logged, or persisted.
		// The chain is preserved for errors.Is/As callers.
		return providers.Response{}, false, redact.ScrubError(fmt.Errorf("model call failed: %w", err))
	}

	var response providers.Response
	emitted := false
	sawDone := false
	// Select on ctx alongside the provider channel so a hung provider
	// that never closes its stream cannot wedge the run forever: on
	// cancellation the turn ends promptly, and the provider's own
	// goroutine exits via its context checks (all built-in adapters
	// observe ctx on send and on read).
	for {
		select {
		case <-ctx.Done():
			return providers.Response{}, emitted, ctx.Err()
		case event, ok := <-stream:
			if !ok {
				if err := ctx.Err(); err != nil {
					return providers.Response{}, emitted, err
				}
				// A stream that closes without a terminal Done marker is
				// incomplete, not success. Reporting it as a valid turn
				// would confuse partial output with completion.
				if !sawDone {
					return providers.Response{}, emitted, fmt.Errorf("model stream ended without a terminal marker (incomplete response)")
				}
				return response, emitted, nil
			}
			if event.Done {
				sawDone = true
			}
			if event.Err != nil {
				return providers.Response{}, emitted, redact.ScrubError(fmt.Errorf("model stream failed: %w", event.Err))
			}

			if event.Thinking != "" {
				emitted = true
				if !emit(Event{Type: EventThinking, Thinking: event.Thinking}) {
					return providers.Response{}, emitted, context.Canceled
				}
			}

			if event.Text != "" {
				emitted = true
				response.Content += event.Text
				if !emit(Event{Type: EventText, Text: event.Text}) {
					return providers.Response{}, emitted, context.Canceled
				}
			}

			if len(event.ToolCalls) > 0 {
				emitted = true
			}
			response.ToolCalls = append(response.ToolCalls, event.ToolCalls...)
			if event.Usage != nil {
				response.Usage = *event.Usage
			}
			if event.StopReason != providers.FinishNone {
				response.StopReason = event.StopReason
			}
		}
	}
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
