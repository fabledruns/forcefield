// Package runtime coordinates the agent loop and its dependencies.
package runtime

import (
	"context"
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

// Runtime is the main execution point for Forcefield.
type Runtime struct {
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

	a := agent.New(
		cfg.Agent.Name,
		cfg.Agent.SystemPrompt,
		skills.FormatCatalog(skillStore.Catalog()),
	).WithProjectMemory(memory.FormatForPrompt(memoryEntries))

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

	manager, err := builtin.NewManager(builtin.WithExecutor(executor), builtin.WithPolicy(policy))
	if err != nil {
		return nil, fmt.Errorf("create tool manager: %w", err)
	}
	if err := manager.Register(newLoadSkillTool(skillStore)); err != nil {
		return nil, fmt.Errorf("register load_skill tool: %w", err)
	}
	if err := manager.Register(newUpdateTaskStateTool()); err != nil {
		return nil, fmt.Errorf("register update_task_state tool: %w", err)
	}
	if err := manager.Register(newAddProjectMemoryTool(projectMemory)); err != nil {
		return nil, fmt.Errorf("register add_project_memory tool: %w", err)
	}

	permManager, err := permissions.NewManager(permissions.NewConfigStore())
	if err != nil {
		return nil, fmt.Errorf("load permissions: %w", err)
	}

	asker := permissions.NewStdinAsker()

	r := &Runtime{
		cfg:                 cfg,
		provider:            provider,
		agent:               a,
		manager:             manager,
		skills:              skillStore,
		scheduler:           newScheduler(manager, permManager, asker, DefaultSchedulerConfig),
		limits:              limitsFromConfig(cfg),
		discovery:           providers.NewDiscovery(providers.DefaultFactories()),
		reasoningSelections: make(map[string]providers.ReasoningConfig),
	}
	r.refreshAuthState()
	r.applyReasoning()
	return r, nil
}

// refreshAuthState re-reads the active provider's authentication
// requirements after construction, provider switch, or model switch.
func (r *Runtime) refreshAuthState() {
	resolved, err := r.cfg.ResolveProvider(r.cfg.Model.Provider, r.cfg.Model.Name)
	if err != nil {
		r.authRequired, r.authEnvVar = false, ""
		return
	}
	r.authRequired = resolved.AuthRequired
	r.authEnvVar = resolved.AuthEnvVar
}

// checkAuth reports an actionable error when the active provider needs an
// API key that was not found.
func (r *Runtime) checkAuth() error {
	if !r.authRequired || r.authEnvVar == "" {
		return nil
	}
	if key, _, err := config.ResolveEnvValue(r.authEnvVar); err == nil && key != "" {
		return nil
	}
	return fmt.Errorf(
		"%s requires an API key - set %s in your environment or .env file and restart Forcefield",
		providers.DisplayName(r.cfg.Model.Provider), r.authEnvVar,
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
	r.scheduler.asker = asker
}

// Permissions returns the runtime's permission manager, e.g. for a
// "/permissions" command that lists or edits the current rules.
func (r *Runtime) Permissions() *permissions.Manager {
	return r.scheduler.permissions
}

// CurrentModel returns the active model name.
func (r *Runtime) CurrentModel() string { return r.cfg.Model.Name }

// CurrentProvider returns the active provider name.
func (r *Runtime) CurrentProvider() string { return r.cfg.Model.Provider }

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
	resolved, _ := r.cfg.ResolveAll(r.cfg.Model.Name)
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
	defs := r.manager.Definitions()
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name+": "+d.Description)
	}
	return out
}

// SetModel switches the active model, keeping the current provider and
// endpoint, and takes effect starting with the next request. The change is
// in-memory only; it does not write config.yaml. This matches AGENTS.md's
// contract that runtime switching is temporary unless explicitly persisted,
// avoids silently stripping user comments via yaml.Marshal, and keeps the
// TUI picker fast and non-destructive. Call SaveConfig to persist.
func (r *Runtime) SetModel(name string) error {
	if name == "" {
		return fmt.Errorf("model name cannot be empty")
	}
	cfg := *r.cfg
	cfg.Model.Name = name
	provider, err := newProvider(&cfg)
	if err != nil {
		return err
	}
	r.cfg = &cfg
	r.provider = provider
	r.refreshAuthState()
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
	if name == "" {
		return fmt.Errorf("provider name cannot be empty")
	}
	cfg := *r.cfg
	resolved, err := cfg.ResolveProvider(name, cfg.Model.Name)
	if err != nil {
		return err
	}
	if resolved.AuthRequired && resolved.APIKey == "" {
		return fmt.Errorf(
			"%s requires an API key - set %s in your environment or .env file and restart Forcefield",
			resolved.Label, resolved.AuthEnvVar,
		)
	}
	cfg.Model.Provider = name
	cfg.Model.Endpoint = resolved.BaseURL
	provider, err := providers.DefaultFactories().Create(resolved.Spec(cfg.Model.Name))
	if err != nil {
		return fmt.Errorf("create provider %q: %w", name, err)
	}
	r.cfg = &cfg
	r.provider = provider
	r.refreshAuthState()
	r.applyReasoning()
	return nil
}

// SaveConfig persists the current in-memory Config to config.yaml atomically.
// Runtime switching via SetModel/SetProvider is temporary by default; call
// this explicitly when the user opts into persistence (e.g. /save or
// ff config). It never writes secrets.
func (r *Runtime) SaveConfig() error {
	if r == nil || r.cfg == nil {
		return fmt.Errorf("no config to save")
	}
	if err := r.cfg.Save(); err != nil {
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
	if r == nil || r.cfg == nil {
		return providers.ReasoningCapabilities{}
	}
	return providers.ModelReasoningCapabilities(r.cfg.Model.Provider, r.cfg.Model.Name)
}

// CurrentEffort returns the selected effort for the active model, or empty
// when none is set or the model does not support effort.
func (r *Runtime) CurrentEffort() string {
	if r == nil || r.cfg == nil {
		return ""
	}
	caps := r.CurrentReasoningCapabilities()
	if caps.Effort == nil {
		return ""
	}
	key := reasoningKey(r.cfg.Model.Provider, r.cfg.Model.Name)
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
	caps := r.CurrentReasoningCapabilities()
	if err := caps.ValidateEffort(level); err != nil {
		return err
	}
	canonical := caps.CanonicalEffort(level)
	key := reasoningKey(r.cfg.Model.Provider, r.cfg.Model.Name)
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
	if r == nil || r.cfg == nil {
		return nil
	}
	caps := r.CurrentReasoningCapabilities()
	if caps.Thinking == nil {
		return nil
	}
	key := reasoningKey(r.cfg.Model.Provider, r.cfg.Model.Name)
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
	key := reasoningKey(r.cfg.Model.Provider, r.cfg.Model.Name)
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
	if r == nil || r.cfg == nil {
		return
	}
	key := reasoningKey(r.cfg.Model.Provider, r.cfg.Model.Name)
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
	if r == nil || r.provider == nil || r.cfg == nil {
		return
	}
	caps := r.CurrentReasoningCapabilities()
	key := reasoningKey(r.cfg.Model.Provider, r.cfg.Model.Name)
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
	if p, ok := r.provider.(providers.ReasoningAware); ok {
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
	messages := []providers.Message{
		{
			Role:    providers.SystemRole,
			Content: r.agent.BuildSystemPrompt(),
		},
	}

	messages = append(messages, history...)

	return messages
}

// StreamChat runs the agent loop and emits structured events.
func (r *Runtime) StreamChat(ctx context.Context, messages []providers.Message) (<-chan Event, error) {
	if ctx == nil {
		return nil, fmt.Errorf("stream context cannot be nil")
	}

	events := make(chan Event)
	go func() {
		defer close(events)
		r.run(ctx, r.buildMessages(messages), func(event Event) bool {
			select {
			case events <- event:
				return true
			case <-ctx.Done():
				return false
			}
		})
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
func (r *Runtime) run(ctx context.Context, messages []providers.Message, emit func(Event) bool) {
	state := task.New(goalFrom(messages))
	ctx = task.WithState(ctx, state)

	limits := r.limits

	for {
		iteration := state.BeginIteration()
		if limits.MaxIterations > 0 && iteration > limits.MaxIterations {
			r.emitBlocked(emit, state, fmt.Sprintf("stopped after %d iterations (maximum reached)", limits.MaxIterations))
			return
		}

		refreshSystemPrompt(messages, r.agent, state)

		response, err := r.runModelTurn(ctx, messages, emit)
		if err != nil {
			emit(Event{Type: EventError, Err: err, TaskState: snapshotPtr(state)})
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

		results := r.scheduler.Run(ctx, response.ToolCalls, emit)

		if ctx.Err() != nil {
			emit(Event{Type: EventError, Err: ctx.Err(), TaskState: snapshotPtr(state)})
			return
		}

		for i, tc := range response.ToolCalls {
			result := results[i]
			state.RecordTool(tc.Name, result.Success)
			messages = append(messages, providers.Message{
				Role:       providers.ToolRole,
				Name:       tc.Name,
				Content:    truncateToolResult(result.Content),
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
func (r *Runtime) runModelTurn(ctx context.Context, messages []providers.Message, emit func(Event) bool) (providers.Response, error) {
	if !emit(Event{Type: EventThinking}) {
		return providers.Response{}, context.Canceled
	}

	if err := r.checkAuth(); err != nil {
		return providers.Response{}, err
	}

	r.applyReasoning()
	stream, err := r.provider.StreamChat(ctx, messages, r.manager.Definitions())
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
		return nil, fmt.Errorf("create provider %q: %w", id, err)
	}
	return provider, nil
}
