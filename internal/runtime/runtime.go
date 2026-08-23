// Package runtime coordinates the agent loop and its dependencies.
package runtime

import (
	"context"
	"fmt"
	"os"
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
}

func New() (*Runtime, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

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

	executor, err := newExecutor(cfg)
	if err != nil {
		return nil, err
	}

	manager, err := builtin.NewManager(builtin.WithExecutor(executor))
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

	return &Runtime{
		cfg:       cfg,
		provider:  provider,
		agent:     a,
		manager:   manager,
		skills:    skillStore,
		scheduler: newScheduler(manager, permManager, asker, DefaultSchedulerConfig),
		limits:    limitsFromConfig(cfg),
	}, nil
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
// endpoint, and takes effect starting with the next request.
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
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	r.cfg = &cfg
	r.provider = provider
	return nil
}

// SetProvider switches the active provider, keeping the current model
// name, and takes effect starting with the next request. If the
// provider is registered, its default endpoint is adopted too, so
// switching providers never leaves the endpoint pointed at the
// previous one.
func (r *Runtime) SetProvider(name string) error {
	if name == "" {
		return fmt.Errorf("provider name cannot be empty")
	}
	cfg := *r.cfg
	cfg.Model.Provider = name
	if info, ok := providers.ByID(name); ok {
		cfg.Model.Endpoint = info.Endpoint
	}
	provider, err := newProvider(&cfg)
	if err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	r.cfg = &cfg
	r.provider = provider
	return nil
}

// newExecutor constructs the sandbox executor for shell commands from the
// configured sandbox section and the current project root. Construction
// fails loudly for unsupported combinations (e.g. wsl mode on Unix);
// there is never a silent fallback to a weaker backend.
func newExecutor(cfg *config.Config) (sandbox.Executor, error) {
	mode, err := sandbox.ParseMode(cfg.Sandbox.Mode)
	if err != nil {
		return nil, fmt.Errorf("invalid sandbox.mode: %w", err)
	}
	network, err := sandbox.ParseNetwork(cfg.Sandbox.WSL.Network)
	if err != nil {
		return nil, fmt.Errorf("invalid sandbox.wsl.network: %w", err)
	}

	policy := sandbox.Policy{
		Mode:      mode,
		Workspace: projectWorkspace(),
		Distro:    cfg.Sandbox.WSL.Distribution,
		Network:   network,
	}
	executor, err := sandbox.NewExecutor(policy)
	if err != nil {
		return nil, fmt.Errorf("create %s executor (sandbox.mode = %q): %w", mode, cfg.Sandbox.Mode, err)
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

// newProvider constructs the configured model provider.
func newProvider(cfg *config.Config) (providers.ModelProvider, error) {
	switch cfg.Model.Provider {
	case "ollama":
		return providers.NewOllamaProvider(cfg.Model.Endpoint, cfg.Model.Name), nil
	case "nvidia":
		return providers.NewNvidiaProvider("https://integrate.api.nvidia.com/v1", cfg.Model.Name, cfg.Model.APIKey, nil), nil
	case "lmstudio":
		// LM Studio exposes the same OpenAI-compatible chat completions
		// API as NVIDIA NIM, just unauthenticated and local; only the
		// error wording differs.
		return providers.NewLMStudioProvider(cfg.Model.Endpoint, cfg.Model.Name), nil
	default:
		return nil, fmt.Errorf(
			"unsupported model provider %q (only \"ollama\", \"lmstudio\", and \"nvidia\" are supported in this prototype)",
			cfg.Model.Provider,
		)
	}
}
