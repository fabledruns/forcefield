package runtime

import (
	"errors"
	"fmt"
	"strings"

	"forcefield/internal/config"
	"forcefield/internal/providers"
)

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

// SetModel switches the active model for the next request (in-memory only
// until SaveConfig). See docs/Runtime.md.
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

// SetProvider switches the active provider for the next request (in-memory
// only until SaveConfig), adopting its default endpoint. See
// docs/Runtime.md.
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
		// Unknown models fall back to an unconfigured router so the switch
		// succeeds and the picker can open (see docs/Runtime.md).
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

func Run(messages []providers.Message) (providers.Response, error) {
	rt, err := New()
	if err != nil {
		return providers.Response{}, fmt.Errorf("create runtime: %w", err)
	}
	// Tear down background work (shell jobs, MCP servers) before
	// returning so one-shot runs never orphan helper processes.
	defer func() { _ = rt.Close() }()

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
