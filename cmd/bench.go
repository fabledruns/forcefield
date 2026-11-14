package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"forcefield/internal/bench"

	"github.com/spf13/cobra"
)

var (
	benchTaskFlag       string
	benchCategoryFlag   string
	benchDifficultyFlag string
	benchKeepWorkspaces bool
	benchKeepFailed     bool
	benchTimeoutFlag    string
	benchTasksDir       string
	benchResultsDir     string
	benchJSONOutput     bool
)

// benchRunner is a package var so tests can inject a mock runner without executing real tasks.
var benchRunner = func(opts bench.RunnerOptions) (*bench.RunResult, error) {
	runner := bench.NewRunner(opts)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runner.Run(ctx)
}

var benchCmd = &cobra.Command{
	Use:   "bench",
	Short: "Automated coding-agent benchmark runner",
	Long: `Run Forcefield's automated evaluation benchmark suite against coding tasks in isolated workspaces.

Subcommands:
  run      Execute benchmark tasks (default if invoked without subcommands)
  list     List available benchmark tasks and categories
  report   Display a benchmark report from previous runs`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBenchCommand(cmd, args)
	},
}

var benchRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Execute benchmark tasks",
	Long: `Execute benchmark tasks in isolated workspaces and produce evaluation reports.

Examples:
  ff bench
  ff bench run
  ff bench run --task MOD-001
  ff bench run --category debugging
  ff bench run --keep-workspaces
  ff bench run --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBenchCommand(cmd, args)
	},
}

var benchListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available benchmark tasks",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBenchListCommand(cmd, args)
	},
}

var benchReportCmd = &cobra.Command{
	Use:   "report [run-id]",
	Short: "Display a benchmark report from previous runs",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBenchReportCommand(cmd, args)
	},
}

func init() {
	// Flags for bench / bench run
	for _, c := range []*cobra.Command{benchCmd, benchRunCmd} {
		c.Flags().StringVar(&benchTaskFlag, "task", "", "filter to a specific task ID (e.g. MOD-001)")
		c.Flags().StringVar(&benchCategoryFlag, "category", "", "filter tasks by category (simple_code_modifications, multi_file_changes, debugging, parallel_delegation)")
		c.Flags().StringVar(&benchDifficultyFlag, "difficulty", "", "filter tasks by difficulty (easy, medium, hard)")
		c.Flags().BoolVar(&benchKeepWorkspaces, "keep-workspaces", false, "retain all isolated workspaces after execution for manual inspection")
		c.Flags().BoolVar(&benchKeepFailed, "keep-failed", false, "retain workspaces of failed tasks for debugging")
		c.Flags().StringVar(&benchTimeoutFlag, "timeout", "", "override task execution timeout (e.g. 90s, 3m)")
		c.Flags().StringVar(&benchTasksDir, "tasks-dir", "benchmarks/tasks", "path to tasks directory")
		c.Flags().StringVar(&benchResultsDir, "results-dir", "benchmarks/results", "path to results directory")
		c.Flags().BoolVar(&benchJSONOutput, "json", false, "output JSON benchmark results to stdout")
	}

	benchListCmd.Flags().StringVar(&benchTasksDir, "tasks-dir", "benchmarks/tasks", "path to tasks directory")
	benchReportCmd.Flags().StringVar(&benchResultsDir, "results-dir", "benchmarks/results", "path to results directory")

	benchCmd.AddCommand(benchRunCmd)
	benchCmd.AddCommand(benchListCmd)
	benchCmd.AddCommand(benchReportCmd)

	rootCmd.AddCommand(benchCmd)
}

func runBenchCommand(cmd *cobra.Command, args []string) error {
	var timeoutDur time.Duration
	if benchTimeoutFlag != "" {
		d, err := time.ParseDuration(benchTimeoutFlag)
		if err != nil {
			return fmt.Errorf("invalid --timeout: %w", err)
		}
		timeoutDur = d
	}

	opts := bench.RunnerOptions{
		TasksDir:         benchTasksDir,
		ResultsDir:       benchResultsDir,
		TaskFilter:       benchTaskFlag,
		CategoryFilter:   benchCategoryFlag,
		DifficultyFilter: benchDifficultyFlag,
		KeepWorkspaces:   benchKeepWorkspaces,
		KeepFailed:       benchKeepFailed,
		TimeoutOverride:  timeoutDur,
		AgentName:        agentFlag,
	}

	if !benchJSONOutput {
		opts.OnProgress = func(taskID string, status string, current, total int) {
			fmt.Fprintf(cmd.ErrOrStderr(), "[%d/%d] Task %s: %s\n", current, total, taskID, status)
		}
	}

	res, err := benchRunner(opts)
	if err != nil {
		return err
	}

	if benchJSONOutput {
		data, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}

	// Print human-readable summary to stdout
	fmt.Printf("\n=======================================================\n")
	fmt.Printf("Forcefield Benchmark Run: %s\n", res.RunID)
	fmt.Printf("=======================================================\n")
	fmt.Printf("Total Tasks:    %d\n", res.Summary.TotalTasks)
	fmt.Printf("Passed:         %d\n", res.Summary.PassedTasks)
	fmt.Printf("Failed:         %d\n", res.Summary.FailedTasks)
	fmt.Printf("Pass Rate:      %.1f%%\n", res.Summary.PassRate)
	fmt.Printf("Total Duration: %.2fs (avg %.2fs/task)\n",
		float64(res.Summary.TotalDurationMS)/1000.0, float64(res.Summary.AvgDurationMS)/1000.0)
	fmt.Printf("Total Tools:    %d calls (%d failures)\n", res.Summary.TotalToolCalls, res.Summary.TotalToolFailures)
	if env := res.Environment; env != nil {
		fmt.Printf("Environment:    %s/%s | backend=%s | slotguard=%s", env.OS, env.Arch, env.Backend, env.SlotGuardMode)
		if env.ContextSize > 0 {
			fmt.Printf(" | ctx=%d", env.ContextSize)
		}
		if env.ParallelSlots > 0 {
			fmt.Printf(" | np=%d", env.ParallelSlots)
		}
		if env.Quantization != "" {
			fmt.Printf(" | quant=%s", env.Quantization)
		}
		fmt.Println()
	}
	if res.Summary.LatencyP50MS > 0 || res.Summary.LatencyP95MS > 0 {
		fmt.Printf("Latency p50/95: %.2fs / %.2fs\n",
			float64(res.Summary.LatencyP50MS)/1000.0, float64(res.Summary.LatencyP95MS)/1000.0)
	}
	if res.Summary.Tokens.Available {
		fmt.Printf("Tokens:         %d prompt / %d completion (%d total, %d cached)\n",
			res.Summary.Tokens.PromptTokens, res.Summary.Tokens.CompletionTokens, res.Summary.Tokens.TotalTokens, res.Summary.Tokens.CachedTokens)
	}
	if res.Summary.AvgPromptPerSecond > 0 || res.Summary.AvgPredictedPerSecond > 0 {
		fmt.Printf("Throughput:     %.1f prompt tok/s | %.1f gen tok/s\n",
			res.Summary.AvgPromptPerSecond, res.Summary.AvgPredictedPerSecond)
	}
	if res.Summary.SlotGuardAdmitted > 0 || res.Summary.SlotGuardWaits > 0 {
		fmt.Printf("SlotGuard:      %d admitted / %d waited (%d ms total wait)\n",
			res.Summary.SlotGuardAdmitted, res.Summary.SlotGuardWaits, res.Summary.SlotGuardWaitMS)
	}

	fmt.Printf("\n--- Category Breakdown ---\n")
	for cat, cs := range res.Summary.Categories {
		fmt.Printf("  • %-28s %d/%d passed (%.1f%%)\n", cat, cs.PassedTasks, cs.TotalTasks, cs.PassRate)
	}

	fmt.Printf("\n--- Task Details ---\n")
	for _, t := range res.Tasks {
		mark := "PASS"
		if !t.Passed {
			mark = fmt.Sprintf("FAIL (%s)", t.FailureType)
		}
		fmt.Printf("  [%-10s] %-10s %-32s (%.2fs, %d tools)\n",
			mark, t.TaskID, t.Name, float64(t.DurationMS)/1000.0, t.ToolCalls)
		if !t.Passed && t.RetainedWorkspace {
			fmt.Printf("    ↳ Workspace preserved: %s\n", t.WorkspaceDir)
		}
	}

	if res.JSONReportPath != "" {
		fmt.Printf("\nReports written to:\n")
		fmt.Printf("  JSON: %s\n", res.JSONReportPath)
		fmt.Printf("  MD:   %s\n", res.MDReportPath)
	}
	fmt.Println()

	return nil
}

func runBenchListCommand(cmd *cobra.Command, args []string) error {
	tasks, err := bench.DiscoverTasks(benchTasksDir)
	if err != nil {
		return err
	}

	fmt.Printf("Available Benchmark Tasks (%d total):\n\n", len(tasks))
	fmt.Printf("%-10s %-30s %-28s %-8s\n", "ID", "Name", "Category", "Difficulty")
	fmt.Println(strings.Repeat("-", 80))
	for _, t := range tasks {
		fmt.Printf("%-10s %-30s %-28s %-8s\n", t.ID, t.Name, t.Category, t.Difficulty)
	}
	fmt.Println()
	return nil
}

func runBenchReportCommand(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		// List available reports in resultsDir
		entries, err := os.ReadDir(benchResultsDir)
		if err != nil {
			return fmt.Errorf("read results directory %q: %w", benchResultsDir, err)
		}
		var reports []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".md") {
				reports = append(reports, strings.TrimSuffix(e.Name(), ".md"))
			}
		}
		if len(reports) == 0 {
			fmt.Printf("No benchmark reports found in %s\n", benchResultsDir)
			return nil
		}
		fmt.Printf("Existing Benchmark Runs (%d available):\n\n", len(reports))
		for _, r := range reports {
			fmt.Printf("  ff bench report %s\n", r)
		}
		return nil
	}

	runID := args[0]
	mdPath := filepath.Join(benchResultsDir, fmt.Sprintf("%s.md", runID))
	data, err := os.ReadFile(mdPath)
	if err != nil {
		return fmt.Errorf("read report for %q: %w", runID, err)
	}
	fmt.Println(string(data))
	return nil
}
