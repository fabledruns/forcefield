package bench

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	RunID             string        `json:"run_id"`
	Timestamp         string        `json:"timestamp"`
	ForcefieldVersion string        `json:"forcefield_version"`
	GitCommit         string        `json:"git_commit,omitempty"`
	Provider          string        `json:"provider,omitempty"`
	Model             string        `json:"model,omitempty"`
	Summary           Summary       `json:"summary"`
	Tasks             []TaskMetrics `json:"tasks"`
	JSONReportPath    string        `json:"json_report_path,omitempty"`
	MDReportPath      string        `json:"md_report_path,omitempty"`
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
	return &Runner{
		opts: opts,
		wsManager: NewWorkspaceManager(
			filepath.Join(os.TempDir(), "ff-bench-workspaces"),
			opts.KeepWorkspaces,
			opts.KeepFailed,
		),
	}
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

	runID := fmt.Sprintf("run-%s", time.Now().UTC().Format("20060102-150405"))
	gitCommit := resolveGitCommit()

	result := &RunResult{
		RunID:             runID,
		Timestamp:         time.Now().UTC().Format(time.RFC3339),
		ForcefieldVersion: "v1.6",
		GitCommit:         gitCommit,
		Provider:          baseCfg.Model.Provider,
		Model:             baseCfg.Model.Name,
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
		metrics.FailureReason = fmt.Sprintf("invalid task manifest: %v", err)
		metrics.DurationMS = time.Since(start).Milliseconds()
		return metrics
	}

	// 2. Prepare isolated workspace.
	ws, err := r.wsManager.Prepare(ctx, task, runID)
	if err != nil {
		metrics.Status = "error"
		metrics.Passed = false
		metrics.FailureType = FailureInfra
		metrics.FailureReason = fmt.Sprintf("workspace setup failed: %v", err)
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
		metrics.FailureReason = fmt.Sprintf("runtime construction failed: %v", err)
		metrics.DurationMS = time.Since(start).Milliseconds()
		return metrics
	}
	defer func() { _ = rt.Close() }()

	if err := rt.SetAgent(r.opts.AgentName); err != nil {
		_ = r.wsManager.Cleanup(ws, false)
		metrics.Status = "error"
		metrics.Passed = false
		metrics.FailureType = FailureInfra
		metrics.FailureReason = fmt.Sprintf("set agent %q failed: %v", r.opts.AgentName, err)
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
		metrics.FailureReason = fmt.Sprintf("stream chat invocation failed: %v", streamErr)
	} else {
		for ev := range streamEvents {
			switch ev.Type {
			case runtime.EventToolStart:
				metrics.ToolCalls++
				if ev.ToolCall != nil {
					metrics.ToolCallBreakdown[ev.ToolCall.Name]++
				}
			case runtime.EventToolFailed:
				metrics.ToolFailures++
			case runtime.EventToolDenied:
				metrics.ToolFailures++
			case runtime.EventDone:
				metrics.Status = "completed"
				if ev.Response != nil && ev.Response.Usage.TotalTokens > 0 {
					metrics.Tokens = TokenUsage{
						Available:        true,
						PromptTokens:     ev.Response.Usage.PromptTokens,
						CompletionTokens: ev.Response.Usage.CompletionTokens,
						TotalTokens:      ev.Response.Usage.TotalTokens,
					}
				}
			case runtime.EventBlocked:
				metrics.Status = "blocked"
				metrics.FailureReason = "agent execution limits reached (blocked by runtime)"
			case runtime.EventCancelled:
				metrics.Status = "cancelled"
			case runtime.EventError:
				metrics.Status = "error"
				if ev.Err != nil {
					metrics.FailureReason = ev.Err.Error()
				}
			}
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

	// Read collector stats
	metrics.Delegations = collector.Delegations()

	// 9. Restore canonical tests if specified to prevent cheating.
	if task.Validation.RestoreCanonical {
		if err := r.wsManager.RestoreCanonical(ws); err != nil {
			metrics.Status = "error"
			metrics.Passed = false
			metrics.FailureType = FailureInfra
			metrics.FailureReason = fmt.Sprintf("failed to restore canonical test files: %v", err)
			metrics.DurationMS = time.Since(start).Milliseconds()
			_ = r.wsManager.Cleanup(ws, false)
			return metrics
		}
	}

	// 10. Inspect filesystem modifications via Git.
	modifiedFiles, diffText, _ := r.wsManager.Diff(context.Background(), ws)
	metrics.FilesChanged = modifiedFiles
	metrics.FilesChangedCount = len(modifiedFiles)
	metrics.Diff = diffText
	metrics.UnexpectedFilesModified = CheckUnexpectedFiles(modifiedFiles, task.ExpectedFiles)

	// 11. Run independent validation.
	vResult := RunValidation(context.Background(), ws)
	metrics.Validation = vResult
	metrics.DiffPassesValidation = vResult.Passed

	// 12. Evaluate final task verdict.
	if metrics.Status == "timeout" {
		metrics.Passed = false
	} else if !vResult.Passed {
		metrics.Passed = false
		if metrics.FailureType == "" {
			if len(modifiedFiles) == 0 {
				metrics.FailureType = FailureAgent
				metrics.FailureReason = "agent produced no file modifications"
			} else {
				metrics.FailureType = FailureTest
				metrics.FailureReason = redact.Scrub(vResult.Error)
				if metrics.FailureReason == "" && vResult.Stderr != "" {
					metrics.FailureReason = redact.Scrub(vResult.Stderr)
				}
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

// benchEventCollector aggregates orchestration events.
type benchEventCollector struct {
	mu          sync.Mutex
	delegations int
}

func (c *benchEventCollector) Emit(e orchevents.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e.Type == orchevents.DelegationRequested || e.Type == orchevents.AgentSpawned {
		c.delegations++
	}
}

func (c *benchEventCollector) Delegations() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.delegations
}

func resolveGitCommit() string {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
