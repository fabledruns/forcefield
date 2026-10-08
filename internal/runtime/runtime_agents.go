package runtime

import (
	"fmt"
	"strings"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/skills"
)

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
	// tool set is valid. Missing mcp__* names are omitted (MCPWarnings);
	// unknown natives still fail strictly.
	agentTools, _ := resolveAgentDefinitionTools(full, def)
	filtered, err := full.Filtered(agentTools)
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
