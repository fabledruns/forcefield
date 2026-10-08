// Package runtime coordinates the agent loop and its dependencies.
package runtime

import (
	"fmt"
	"os"
	"sync"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/mcp"
	"forcefield/internal/memory"
	"forcefield/internal/perfmark"
	"forcefield/internal/permissions"
	"forcefield/internal/providers"
	"forcefield/internal/sandbox"
	"forcefield/internal/skills"
	"forcefield/internal/tools"
	"forcefield/internal/tools/builtin"
	"forcefield/internal/trace"
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
	// mcpHost owns MCP server subprocesses when any MCP server is
	// enabled, nil otherwise. Runtime owns Host lifecycle (Close); the
	// Host owns process lifecycle. The adapters it registered stay in
	// fullManager for the runtime lifetime (frozen universe, no per-turn
	// teardown, no reconnect).
	mcpHost *mcp.Host
	// mcpStatusNote records a non-fatal MCP status persistence failure
	// for MCPWarnings. Status writes are best-effort by design: they must
	// never fail startup or shutdown.
	mcpStatusNote string
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
	// The configuration is loaded and usable here. Headless startup
	// paths (ff run) build through New; the TUI path loads config in
	// tui.Start and emits its own config-loaded there, so each
	// process emits this marker at most once per path.
	perfmark.Event("config-loaded")
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
	// Runtime construction starts here on every path (headless via
	// New, interactive via NewFromConfig). The TUI builds it on a
	// background goroutine while the first frame renders.
	perfmark.Event("runtime-init-start")
	forcefieldHome, err := config.Dir()
	if err != nil {
		return nil, fmt.Errorf("resolve forcefield home: %w", err)
	}

	skillStore, err := skills.New(forcefieldHome)
	if err != nil {
		return nil, fmt.Errorf("load skill store: %w", err)
	}
	perfmark.Event("stage-skills")

	// Resolve the project root once for both the memory store and the
	// workspace policy below: each used to shell out to `git rev-parse`
	// independently (~60-80ms per spawn on Windows) for the same cwd.
	// Error strings mirror memory.CurrentProjectStore wrapped by the
	// historical "resolve project memory store" context.
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve project memory store: resolve working directory: %w", err)
	}
	sharedRoot, rootErr := memory.ProjectRoot(cwd)
	var projectMemory *memory.Store
	if rootErr != nil {
		return nil, fmt.Errorf("resolve project memory store: %w", rootErr)
	}
	projectMemory, err = memory.ProjectStore(forcefieldHome, sharedRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project memory store: %w", err)
	}
	memoryEntries, err := projectMemory.Load()
	if err != nil {
		return nil, fmt.Errorf("load project memory: %w", err)
	}
	memoryText := memory.FormatForPrompt(memoryEntries)
	perfmark.Event("stage-memory")

	provider, err := newProvider(cfg)
	if err != nil {
		return nil, err
	}
	perfmark.Event("stage-provider")

	policy, err := newPolicyWithRoot(cfg, cwd, sharedRoot, rootErr)
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
	perfmark.Event("stage-tools")

	// Start MCP servers, if any are enabled, and fold healthy adapters
	// into the full manager. Individual server failures warn-and-continue
	// (see MCPWarnings): natives and healthy survivors always register.
	var mcpHost *mcp.Host
	mcpStatusNote := ""
	if mcpHasEnabledServers(cfg) {
		h, err := startMCPHost(cfg, policy.Workspace, fullManager)
		if err != nil {
			return nil, fmt.Errorf("start MCP host: %w", err)
		}
		mcpHost = h
		// Best-effort last-known-state for `ff doctor`. A write failure
		// warns via MCPWarnings; it never fails startup.
		if err := persistMCPStatus(h, policy.Workspace, cfg); err != nil {
			mcpStatusNote = fmt.Sprintf("mcp status not persisted: %v", err)
		}
		perfmark.Event("stage-mcp")
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

	// Requested-but-missing mcp__* names are omitted (see MCPWarnings);
	// unknown native names still fail strictly inside Filtered.
	agentTools, _ := resolveAgentDefinitionTools(fullManager, def)
	filtered, err := fullManager.Filtered(agentTools)
	if err != nil {
		return nil, fmt.Errorf("build tool set for agent %q: %w", def.Name, err)
	}

	// The Config is already loaded: parse its permissions section
	// directly instead of loading config.yaml a second time through
	// the store. Persistence (Update/Save) still uses the store.
	permRules, err := permissions.RulesFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("load permissions: %w", err)
	}
	permManager := permissions.NewManagerWithRules(permRules, permissions.NewConfigStore())

	asker := permissions.NewStdinAsker()

	// cwd was resolved (with error) at the top for the single git
	// lookup; reuse it here as the permissive-mode anchor.
	r := &Runtime{
		cfg:                 cfg,
		provider:            provider,
		manager:             filtered,
		fullManager:         fullManager,
		mcpHost:             mcpHost,
		mcpStatusNote:       mcpStatusNote,
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
	perfmark.Event("stage-agents")
	return r, nil
}
