package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"forcefield/internal/config"
	"forcefield/internal/orchevents"
	"forcefield/internal/providers"
	"forcefield/internal/redact"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// RunnerOptions configures the benchmark runner.
type RunnerOptions struct {
	TasksDir         string
	ResultsDir       string
	TaskFilter       string
	CategoryFilter   string
	DifficultyFilter string
	KeepWorkspaces   bool
	KeepFailed       bool
	TimeoutOverride  time.Duration
	AgentName        string
	BaseConfig       *config.Config
	RuntimeFactory   func(cfg *config.Config) (*runtime.Runtime, error)
	OnProgress       func(taskID string, status string, current, total int)
}

// RunResult holds the output of a completed benchmark suite run.
type RunResult struct {
	RunID             string            `json:"run_id"`
	Timestamp         string            `json:"timestamp"`
	ForcefieldVersion string            `json:"forcefield_version"`
	GitCommit         string            `json:"git_commit,omitempty"`
	Provider          string            `json:"provider,omitempty"`
	Model             string            `json:"model,omitempty"`
	Environment       *BenchEnvironment `json:"environment,omitempty"`
	Summary           Summary           `json:"summary"`
	Tasks             []TaskMetrics     `json:"tasks"`
	JSONReportPath    string            `json:"json_report_path,omitempty"`
	MDReportPath      string            `json:"md_report_path,omitempty"`
}

// Runner coordinates benchmark execution across isolated workspaces.
type Runner struct {
	opts      RunnerOptions
	wsManager *WorkspaceManager
}

// NewRunner creates a new benchmark runner with the provided options.
func NewRunner(opts RunnerOptions) *Runner {
	if opts.TasksDir == "" {
		opts.TasksDir = "benchmarks/tasks"
	}
	if opts.ResultsDir == "" {
		opts.ResultsDir = "benchmarks/results"
	}
	if opts.AgentName == "" {
		opts.AgentName = "coding"
	}
	if opts.RuntimeFactory == nil {
		opts.RuntimeFactory = runtime.NewFromConfig
	}

	// Ensure project environment directory is preserved on BaseConfig for credential resolution
	// across isolated task workspaces without using global state.
	if opts.BaseConfig != nil && opts.BaseConfig.ProjectDir == "" {
		if opts.BaseConfig.Workspace.Root != "" {
			opts.BaseConfig.ProjectDir = opts.BaseConfig.Workspace.Root
		} else if wd, err := os.Getwd(); err == nil {
			opts.BaseConfig.ProjectDir = wd
		}
	}

	return &Runner{
		opts: opts,
		wsManager: NewWorkspaceManager(
			filepath.Join(os.TempDir(), "ff-bench-workspaces"),
			opts.KeepWorkspaces,
			opts.KeepFailed,
		),
	}
}

// Preflight verifies that the configured model provider can be resolved and
// has necessary credentials before executing any benchmark tasks. It uses
// cfg.ResolveProvider so credential resolution is identical to runtime execution.
func (r *Runner) Preflight(cfg *config.Config) (*config.ResolvedProvider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("preflight: base configuration is nil")
	}
	resolved, err := cfg.ResolveProvider(cfg.Model.Provider, cfg.Model.Name)
	if err != nil {
		return nil, fmt.Errorf("preflight provider resolution failed: %w", err)
	}
	if resolved.AuthRequired && resolved.APIKey == "" {
		label := resolved.Label
		if label == "" {
			label = providers.DisplayName(resolved.ID)
		}
		return nil, fmt.Errorf("%s requires an API key - set %s in your environment or .env file and restart Forcefield", label, resolved.AuthEnvVar)
	}
	return &resolved, nil
}

// Run executes the discovered benchmark tasks and produces evaluation reports.
func (r *Runner) Run(ctx context.Context) (*RunResult, error) {
	tasks, err := DiscoverTasks(r.opts.TasksDir)
	if err != nil {
		return nil, fmt.Errorf("discover benchmark tasks: %w", err)
	}

	filtered := FilterTasks(tasks, r.opts.TaskFilter, r.opts.CategoryFilter, r.opts.DifficultyFilter)
	if len(filtered) == 0 {
		return nil, fmt.Errorf("no benchmark tasks matched criteria (total available: %d)", len(tasks))
	}

	baseCfg := r.opts.BaseConfig
	if baseCfg == nil {
		loaded, err := config.Load()
		if err != nil {
			return nil, fmt.Errorf("load base config: %w", err)
		}
		baseCfg = loaded
	}

	// Ensure project environment directory is preserved on baseCfg.
	if baseCfg.ProjectDir == "" {
		if baseCfg.Workspace.Root != "" {
			baseCfg.ProjectDir = baseCfg.Workspace.Root
		} else if wd, err := os.Getwd(); err == nil {
			baseCfg.ProjectDir = wd
		}
	}

	// Preflight provider configuration and credentials using the unified provider resolver.
	resolved, err := r.Preflight(baseCfg)
	if err != nil {
		return nil, fmt.Errorf("preflight check failed: %w", err)
	}

	runID := fmt.Sprintf("run-%s", time.Now().UTC().Format("20060102-150405"))
	gitCommit := resolveGitCommit()

	result := &RunResult{
		RunID:             runID,
		Timestamp:         time.Now().UTC().Format(time.RFC3339),
		ForcefieldVersion: "v1.6",
		GitCommit:         gitCommit,
		Provider:          baseCfg.Model.Provider,
		Model:             baseCfg.Model.Name,
		Environment:       probeBenchEnvironment(resolved, baseCfg),
	}

	total := len(filtered)
	for i, task := range filtered {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if r.opts.OnProgress != nil {
			r.opts.OnProgress(task.ID, "running", i+1, total)
		}

		metrics := r.runSingleTask(ctx, task, baseCfg, runID)
		result.Tasks = append(result.Tasks, metrics)

		if r.opts.OnProgress != nil {
			status := "passed"
			if !metrics.Passed {
				status = fmt.Sprintf("failed (%s)", metrics.FailureType)
			}
			r.opts.OnProgress(task.ID, status, i+1, total)
		}
	}

	result.Summary = CalculateSummary(result.Tasks)

	// Generate and save reports
	if err := os.MkdirAll(r.opts.ResultsDir, 0755); err != nil {
		return result, fmt.Errorf("create results directory: %w", err)
	}

	jsonPath := filepath.Join(r.opts.ResultsDir, fmt.Sprintf("%s.json", runID))
	mdPath := filepath.Join(r.opts.ResultsDir, fmt.Sprintf("%s.md", runID))

	if err := SaveJSONReport(result, jsonPath); err == nil {
		result.JSONReportPath = jsonPath
	}
	if err := SaveMarkdownReport(result, mdPath); err == nil {
		result.MDReportPath = mdPath
	}

	return result, nil
}

// runSingleTask executes one benchmark task within an isolated workspace.
func (r *Runner) runSingleTask(ctx context.Context, task *Task, baseCfg *config.Config, runID string) TaskMetrics {
	start := time.Now()
	metrics := TaskMetrics{
		TaskID:            task.ID,
		Name:              task.Name,
		Category:          task.Category,
		Difficulty:        task.Difficulty,
		ToolCallBreakdown: make(map[string]int),
	}

	// 1. Validate manifest definition.
	if err := task.Validate(); err != nil {
		metrics.Status = "error"
		metrics.Passed = false
		metrics.FailureType = FailureInvalidTask
		metrics.FailureReason = redact.Scrub(fmt.Sprintf("invalid task manifest: %v", err))
		metrics.DurationMS = time.Since(start).Milliseconds()
		return metrics
	}

	// 2. Prepare isolated workspace.
	ws, err := r.wsManager.Prepare(ctx, task, runID)
	if err != nil {
		metrics.Status = "error"
		metrics.Passed = false
		metrics.FailureType = FailureInfra
		metrics.FailureReason = redact.Scrub(fmt.Sprintf("workspace setup failed: %v", err))
		metrics.DurationMS = time.Since(start).Milliseconds()
		return metrics
	}
	metrics.WorkspaceDir = ws.Dir

	// 3. Build task-scoped configuration caging workspace strictly to temp dir.
	taskCfg := cloneConfig(baseCfg)
	taskCfg.Workspace.Root = ws.Dir
	taskCfg.Workspace.Mode = config.WorkspaceStrict
	// In benchmark mode, auto-allow tool permissions so the agent does not hang on stdin.
	taskCfg.Permissions.Default = "allow"
	taskCfg.Permissions.Tools = nil
	// Enable orchestration so the delegate tool is registered for multi-agent / parallel tasks.
	taskCfg.Orchestration.Enabled = true
	// Disable host MCP servers in isolated benchmark workspaces.
	taskCfg.MCP.Servers = nil

	// 4. Determine execution timeout.
	taskTimeout := task.ParsedTimeout()
	if r.opts.TimeoutOverride > 0 {
		taskTimeout = r.opts.TimeoutOverride
	}
	runCtx, cancel := context.WithTimeout(ctx, taskTimeout)
	defer cancel()

	// 5. Construct isolated runtime.
	rt, err := r.opts.RuntimeFactory(taskCfg)
	if err != nil {
		_ = r.wsManager.Cleanup(ws, false)
		metrics.Status = "error"
		metrics.Passed = false
		metrics.FailureType = FailureInfra
		metrics.FailureReason = redact.Scrub(fmt.Sprintf("runtime construction failed: %v", err))
		metrics.DurationMS = time.Since(start).Milliseconds()
		return metrics
	}
	defer func() { _ = rt.Close() }()

	if err := rt.SetAgent(r.opts.AgentName); err != nil {
		_ = r.wsManager.Cleanup(ws, false)
		metrics.Status = "error"
		metrics.Passed = false
		metrics.FailureType = FailureInfra
		metrics.FailureReason = redact.Scrub(fmt.Sprintf("set agent %q failed: %v", r.opts.AgentName, err))
		metrics.DurationMS = time.Since(start).Milliseconds()
		return metrics
	}

	// 6. Hook orchestration sink for telemetry.
	collector := &benchEventCollector{}
	rt.SetOrchestrationEventSink(collector)

	// 7. Execute agent loop.
	sess := session.New()
	sess.Agent = r.opts.AgentName
	sess.AddProviderMessage(providers.Message{
		Role:    providers.UserRole,
		Content: task.Instructions,
	})

	streamEvents, streamErr := rt.StreamChat(runCtx, sess.ProviderMessages())
	if streamErr != nil {
		metrics.Status = "error"
		metrics.Passed = false
		metrics.FailureType = FailureInfra
		metrics.FailureReason = redact.Scrub(fmt.Sprintf("stream chat invocation failed: %v", streamErr))
	} else {
		for ev := range streamEvents {
			if ev.TaskState != nil && ev.TaskState.Iteration > metrics.AgentTurns {
				metrics.AgentTurns = ev.TaskState.Iteration
			}
			if ev.ToolResult != nil && ev.ToolResult.Attempt > 1 {
				metrics.AgentRetries += (ev.ToolResult.Attempt - 1)
			}
			switch ev.Type {
			case runtime.EventToolStart:
				metrics.ToolCalls++
				if ev.ToolCall != nil {
					metrics.ToolCallBreakdown[ev.ToolCall.Name]++
				}
			case runtime.EventToolFailed:
				if ev.ToolResult != nil && ev.ToolResult.CommandFailure {
					metrics.CommandFailures++
				} else {
					metrics.ToolFailures++
				}
			case runtime.EventToolDenied:
				metrics.ToolFailures++
			case runtime.EventDone:
				metrics.Status = "completed"
				if ev.Response != nil {
					u := ev.Response.Usage
					if u.TotalTokens > 0 {
						metrics.Tokens = TokenUsage{
							Available:        true,
							PromptTokens:     u.PromptTokens,
							CompletionTokens: u.CompletionTokens,
							TotalTokens:      u.TotalTokens,
							CachedTokens:     u.CachedTokens,
						}
					}
					if u.Timings != (providers.TurnTimings{}) {
						metrics.Inference = &InferenceTelemetry{
							PromptMS:           u.Timings.PromptMS,
							PredictedMS:        u.Timings.PredictedMS,
							PromptPerSecond:    u.Timings.PromptPerSecond,
							PredictedPerSecond: u.Timings.PredictedPerSecond,
							CacheN:             u.Timings.CacheN,
						}
					}
				}
			case runtime.EventBlocked:
				metrics.Status = "blocked"
				metrics.FailureReason = "agent execution limits reached (blocked by runtime)"
			case runtime.EventCancelled:
				metrics.Status = "cancelled"
			case runtime.EventError:
				metrics.Status = "error"
				metrics.FailureType = FailureInfra
				if ev.Err != nil {
					metrics.FailureReason = redact.Scrub(ev.Err.Error())
				}
			}
		}
		if metrics.AgentTurns == 0 && metrics.Status == "completed" {
			metrics.AgentTurns = 1
		}
	}

	// 8. Handle timeouts / cancellations.
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		metrics.Status = "timeout"
		metrics.FailureType = FailureTimeout
		metrics.FailureReason = fmt.Sprintf("execution timed out after %s", taskTimeout)
	} else if errors.Is(ctx.Err(), context.Canceled) {
		metrics.Status = "cancelled"
		metrics.FailureReason = "execution cancelled by caller"
	}

	// Read collector + SlotGuard stats
	metrics.Delegations = collector.Delegations()
	if childCached := collector.ChildCachedTokens(); childCached > 0 && metrics.Tokens.Available {
		metrics.Tokens.CachedTokens += childCached
	}
	if childPredTPS := collector.AvgChildPredictedTPS(); childPredTPS > 0 && (metrics.Inference == nil || metrics.Inference.PredictedPerSecond == 0) {
		if metrics.Inference == nil {
			metrics.Inference = &InferenceTelemetry{}
		}
		metrics.Inference.PredictedPerSecond = roundTwoDecimals(childPredTPS)
	}
	if stats, ok := rt.ProviderSlotStats(); ok {
		enabled := rt.SlotGuardEnabled()
		if enabled || stats.Admitted > 0 || stats.Waits > 0 {
			metrics.SlotGuard = &SlotGuardMetrics{
				Enabled:           enabled,
				Admitted:          stats.Admitted,
				Waits:             stats.Waits,
				WaitMS:            stats.TotalWaitMS,
				DegradedFallbacks: stats.DegradedFallbacks,
			}
		}
	}

	// 9. Inspect filesystem modifications via Git before restoring canonical files.
	modifiedFiles, diffText, _ := r.wsManager.Diff(context.Background(), ws)
	metrics.FilesChanged = modifiedFiles
	metrics.FilesChangedCount = len(modifiedFiles)
	metrics.Diff = diffText
	metrics.UnexpectedFilesModified = CheckUnexpectedFiles(modifiedFiles, task.ExpectedFiles)

	// 10. Restore canonical tests if specified to prevent cheating.
	if task.Validation.RestoreCanonical {
		if err := r.wsManager.RestoreCanonical(ws); err != nil {
			metrics.Status = "error"
			metrics.Passed = false
			metrics.FailureType = FailureInfra
			metrics.FailureReason = redact.Scrub(fmt.Sprintf("failed to restore canonical test files: %v", err))
			metrics.DurationMS = time.Since(start).Milliseconds()
			_ = r.wsManager.Cleanup(ws, false)
			return metrics
		}
	}

	// 11. Run independent validation.
	vResult := RunValidation(context.Background(), ws)
	metrics.Validation = vResult
	metrics.DiffPassesValidation = vResult.Passed

	// 12. Evaluate final task verdict.
	if metrics.Status == "timeout" {
		metrics.Passed = false
	} else if metrics.Status == "error" {
		metrics.Passed = false
		if metrics.FailureType == "" {
			metrics.FailureType = FailureInfra
		}
	} else if task.Scoring.RequireDelegation && metrics.Delegations == 0 {
		metrics.Passed = false
		metrics.FailureType = FailureAgent
		metrics.FailureReason = "task required agent delegation, but no delegation events occurred"
	} else if task.Scoring.DisallowUnexpectedFiles && metrics.UnexpectedFilesModified {
		metrics.Passed = false
		metrics.FailureType = FailureAgent
		metrics.FailureReason = "agent modified unexpected files outside the authorized target set"
	} else if !vResult.Passed {
		metrics.Passed = false
		if metrics.FailureType == "" {
			if len(modifiedFiles) == 0 {
				metrics.FailureType = FailureAgent
				metrics.FailureReason = "agent produced no file modifications"
			} else {
				metrics.FailureType = FailureTest
				reason := strings.TrimSpace(vResult.Stderr)
				if reason == "" {
					reason = extractTestFailureReason(vResult.Stdout)
				}
				if reason == "" {
					reason = vResult.Error
				}
				metrics.FailureReason = redact.Scrub(reason)
			}
		}
	} else {
		// Validation passed
		if task.Scoring.RequireFilesModified && len(modifiedFiles) == 0 {
			metrics.Passed = false
			metrics.FailureType = FailureAgent
			metrics.FailureReason = "task required file modifications, but none occurred"
		} else {
			metrics.Passed = true
			metrics.Status = "passed"
			metrics.FailureType = FailureNone
		}
	}

	metrics.DurationMS = time.Since(start).Milliseconds()

	// 13. Clean up or retain workspace.
	_ = r.wsManager.Cleanup(ws, metrics.Passed)
	metrics.RetainedWorkspace = ws.Retained

	return metrics
}

// cloneConfig creates an in-memory shallow copy of base configuration.
func cloneConfig(c *config.Config) *config.Config {
	if c == nil {
		return &config.Config{}
	}
	cp := *c
	return &cp
}

// benchEventCollector aggregates orchestration events and numeric child telemetry.
type benchEventCollector struct {
	mu                sync.Mutex
	delegations       int
	childCachedTokens int
	childPredTPS      []float64
}

func (c *benchEventCollector) Emit(e orchevents.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.Type == orchevents.DelegationRequested || e.Type == orchevents.AgentSpawned {
		c.delegations++
	}
	if e.Type == orchevents.ChildCompleted && e.Meta != nil {
		if v, ok := metaInt(e.Meta["cached_tokens"]); ok && v > 0 {
			c.childCachedTokens += v
		}
		if v, ok := metaFloat(e.Meta["predicted_tps"]); ok && v > 0 {
			c.childPredTPS = append(c.childPredTPS, v)
		}
	}
}

func (c *benchEventCollector) Delegations() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.delegations
}

func (c *benchEventCollector) ChildCachedTokens() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.childCachedTokens
}

func (c *benchEventCollector) AvgChildPredictedTPS() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.childPredTPS) == 0 {
		return 0
	}
	var sum float64
	for _, v := range c.childPredTPS {
		sum += v
	}
	return sum / float64(len(c.childPredTPS))
}

func metaInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case uint64:
		return int(t), true
	case float64:
		return int(t), true
	default:
		return 0, false
	}
}

func metaFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	default:
		return 0, false
	}
}

func probeBenchEnvironment(resolved *config.ResolvedProvider, cfg *config.Config) *BenchEnvironment {
	env := &BenchEnvironment{
		OS:           goruntime.GOOS,
		Arch:         goruntime.GOARCH,
		Quantization: detectQuantization(cfg),
	}
	if resolved == nil {
		return env
	}
	env.ProviderType = resolved.Type
	env.Backend = resolved.Type
	if cfg != nil && cfg.Agent.ContextWindow > 0 {
		env.ContextSize = cfg.Agent.ContextWindow
	}
	if resolved.Type != "lemonade" {
		env.SlotGuardMode = "n/a"
		return env
	}
	if resolved.DisableSlotGuard {
		env.SlotGuardMode = "disabled"
	} else {
		env.SlotGuardMode = "enabled"
	}

	apiBase, _ := providers.NormalizeLemonadeEndpoints(resolved.BaseURL)
	client := &http.Client{Timeout: 750 * time.Millisecond}
	getJSON := func(fullURL string, out any) bool {
		req, err := http.NewRequest(http.MethodGet, fullURL, nil)
		if err != nil {
			return false
		}
		if resolved.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+resolved.APIKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return false
		}
		return json.Unmarshal(body, out) == nil
	}

	var health struct {
		AllModelsLoaded []struct {
			ModelName        string         `json:"model_name"`
			Checkpoint       string         `json:"checkpoint"`
			Recipe           string         `json:"recipe"`
			Device           string         `json:"device"`
			MaxContextWindow int            `json:"max_context_window"`
			LaunchCommand    []string       `json:"launch_command"`
			RecipeOptions    map[string]any `json:"recipe_options"`
		} `json:"all_models_loaded"`
	}
	if getJSON(apiBase+"/health", &health) {
		env.LemonadeLive = true
		activeModel := ""
		if cfg != nil {
			activeModel = cfg.Model.Name
		}
		for _, m := range health.AllModelsLoaded {
			if activeModel != "" && m.ModelName != activeModel && len(health.AllModelsLoaded) > 1 {
				continue
			}
			env.Recipe = m.Recipe
			env.Device = m.Device
			if m.Recipe != "" {
				if m.Device != "" {
					env.Backend = m.Recipe + ":" + m.Device
				} else {
					env.Backend = m.Recipe
				}
			}
			if env.Quantization == "" {
				env.Quantization = detectQuantizationFromString(m.Checkpoint)
			}
			env.ParallelSlots = parseBenchNP(m.LaunchCommand, m.RecipeOptions)
			if env.ContextSize == 0 {
				env.ContextSize = parseBenchCtxSize(m.RecipeOptions)
			}
			if env.ContextSize == 0 && m.MaxContextWindow > 0 {
				env.ContextSize = m.MaxContextWindow
			}
			break
		}
	}
	if env.ContextSize == 0 && cfg != nil && cfg.Model.Name != "" {
		var opts struct {
			ResolvedCtxSize int `json:"resolved_ctx_size"`
		}
		if getJSON(apiBase+"/models/"+url.PathEscape(cfg.Model.Name)+"/options", &opts) && opts.ResolvedCtxSize > 0 {
			env.ContextSize = opts.ResolvedCtxSize
		}
	}
	var sysInfo struct {
		CPU     string         `json:"cpu"`
		Devices map[string]any `json:"devices"`
	}
	if getJSON(apiBase+"/system-info", &sysInfo) {
		env.LemonadeLive = true
		if sysInfo.CPU != "" {
			env.CPU = sysInfo.CPU
		}
	}
	var sysStats struct {
		MemoryGB float64 `json:"memory_gb"`
		GPUs     []struct {
			Name          string   `json:"name"`
			MemoryTotalGB *float64 `json:"memory_total_gb"`
		} `json:"gpus"`
	}
	if getJSON(apiBase+"/system-stats", &sysStats) {
		if sysStats.MemoryGB > 0 {
			env.RAMGB = sysStats.MemoryGB
		}
		for _, g := range sysStats.GPUs {
			if g.Name == "" {
				continue
			}
			if g.MemoryTotalGB != nil && *g.MemoryTotalGB > 0 {
				env.GPUs = append(env.GPUs, fmt.Sprintf("%s (%.1f GB)", g.Name, *g.MemoryTotalGB))
			} else {
				env.GPUs = append(env.GPUs, g.Name)
			}
		}
	}
	return env
}

func parseBenchNP(launchCmd []string, recipeOpts map[string]any) int {
	for i := 0; i < len(launchCmd); i++ {
		if (launchCmd[i] == "-np" || launchCmd[i] == "--parallel") && i+1 < len(launchCmd) {
			if n, err := strconv.Atoi(launchCmd[i+1]); err == nil && n > 0 {
				return n
			}
		}
	}
	if raw, ok := recipeOpts["llamacpp_args"].(string); ok && raw != "" {
		fields := strings.Fields(raw)
		for i := 0; i < len(fields); i++ {
			if (fields[i] == "-np" || fields[i] == "--parallel") && i+1 < len(fields) {
				if n, err := strconv.Atoi(fields[i+1]); err == nil && n > 0 {
					return n
				}
			}
		}
	}
	return 1
}

func parseBenchCtxSize(recipeOpts map[string]any) int {
	for _, key := range []string{"ctx_size", "context_size", "n_ctx"} {
		v, ok := recipeOpts[key]
		if !ok || v == nil {
			continue
		}
		switch n := v.(type) {
		case float64:
			if int(n) > 0 {
				return int(n)
			}
		case int:
			if n > 0 {
				return n
			}
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(n)); err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return 0
}

func detectQuantization(cfg *config.Config) string {
	if q := strings.TrimSpace(os.Getenv("FF_BENCH_QUANTIZATION")); q != "" {
		return q
	}
	if cfg == nil {
		return ""
	}
	return detectQuantizationFromString(cfg.Model.Name)
}

func detectQuantizationFromString(raw string) string {
	upper := strings.ToUpper(raw)
	for _, tag := range []string{"Q4_K_M", "Q4_K_S", "Q4_0", "Q5_K_M", "Q5_K_S", "Q6_K", "Q8_0", "IQ4_XS", "FP16", "BF16"} {
		if strings.Contains(upper, tag) {
			return tag
		}
	}
	return ""
}

func resolveGitCommit() string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func extractTestFailureReason(stdout string) string {
	lines := strings.Split(stdout, "\n")
	var failLines []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "--- FAIL:") || strings.HasPrefix(trimmed, "FAIL\t") || strings.Contains(trimmed, "_test.go:") {
			failLines = append(failLines, trimmed)
		}
	}
	if len(failLines) > 0 {
		if len(failLines) > 5 {
			failLines = failLines[:5]
		}
		return strings.Join(failLines, "\n")
	}
	trimmed := strings.TrimSpace(stdout)
	if len(trimmed) > 300 {
		return trimmed[len(trimmed)-300:]
	}
	return trimmed
}
