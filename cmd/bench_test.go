package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forcefield/internal/bench"
)

func TestBenchCommand_RunDefaults(t *testing.T) {
	origRunner := benchRunner
	defer func() { benchRunner = origRunner }()

	var capturedOpts bench.RunnerOptions
	benchRunner = func(opts bench.RunnerOptions) (*bench.RunResult, error) {
		capturedOpts = opts
		return &bench.RunResult{
			RunID: "mock-run-001",
			Summary: bench.Summary{
				TotalTasks:  1,
				PassedTasks: 1,
				PassRate:    100.0,
			},
			Tasks: []bench.TaskMetrics{
				{
					TaskID:    "MOD-001",
					Name:      "Slugify",
					Passed:    true,
					Status:    "passed",
					ToolCalls: 2,
				},
			},
		}, nil
	}

	benchTaskFlag = ""
	benchCategoryFlag = ""
	benchTimeoutFlag = ""
	benchJSONOutput = false

	if err := benchRunCmd.RunE(benchRunCmd, []string{}); err != nil {
		t.Fatalf("benchRunCmd.RunE error: %v", err)
	}

	if capturedOpts.TasksDir != "benchmarks/tasks" {
		t.Errorf("TasksDir = %q, want benchmarks/tasks", capturedOpts.TasksDir)
	}
}

func TestBenchCommand_JSONOutput(t *testing.T) {
	origRunner := benchRunner
	defer func() { benchRunner = origRunner }()

	benchRunner = func(opts bench.RunnerOptions) (*bench.RunResult, error) {
		return &bench.RunResult{
			RunID: "json-run-001",
			Summary: bench.Summary{
				TotalTasks:  1,
				PassedTasks: 1,
				PassRate:    100.0,
			},
		}, nil
	}

	benchJSONOutput = true
	defer func() { benchJSONOutput = false }()

	if err := benchRunCmd.RunE(benchRunCmd, []string{}); err != nil {
		t.Fatalf("benchRunCmd.RunE error: %v", err)
	}
}

func TestBenchCommand_PropagatesRunnerError(t *testing.T) {
	origRunner := benchRunner
	defer func() { benchRunner = origRunner }()

	benchRunner = func(opts bench.RunnerOptions) (*bench.RunResult, error) {
		return nil, fmt.Errorf("simulated runner failure")
	}

	benchJSONOutput = false
	err := benchRunCmd.RunE(benchRunCmd, []string{})
	if err == nil || !strings.Contains(err.Error(), "simulated runner failure") {
		t.Fatalf("expected simulated runner failure, got: %v", err)
	}
}

func TestBenchCommand_TimeoutFlagParsing(t *testing.T) {
	origRunner := benchRunner
	defer func() { benchRunner = origRunner }()

	var capturedTimeout time.Duration
	benchRunner = func(opts bench.RunnerOptions) (*bench.RunResult, error) {
		capturedTimeout = opts.TimeoutOverride
		return &bench.RunResult{
			RunID: "timeout-flag-run",
		}, nil
	}

	benchTimeoutFlag = "45s"
	defer func() { benchTimeoutFlag = "" }()

	if err := benchRunCmd.RunE(benchRunCmd, []string{}); err != nil {
		t.Fatalf("benchRunCmd.RunE error: %v", err)
	}

	if capturedTimeout != 45*time.Second {
		t.Errorf("capturedTimeout = %v, want 45s", capturedTimeout)
	}

	benchTimeoutFlag = "invalid-dur"
	if err := benchRunCmd.RunE(benchRunCmd, []string{}); err == nil {
		t.Fatal("expected error on invalid timeout duration, got nil")
	}
}

func TestBenchListCommand(t *testing.T) {
	tmpDir := t.TempDir()
	taskYAML := `
id: "LIST-001"
name: "List Test Task"
category: "simple_code_modifications"
difficulty: "easy"
instructions: "Test instructions"
validation:
  commands: [["go", "test"]]
`
	_ = os.WriteFile(filepath.Join(tmpDir, "task.yaml"), []byte(taskYAML), 0644)

	benchTasksDir = tmpDir
	defer func() { benchTasksDir = "benchmarks/tasks" }()

	if err := benchListCmd.RunE(benchListCmd, []string{}); err != nil {
		t.Fatalf("benchListCmd error: %v", err)
	}
}

func TestBenchReportCommand(t *testing.T) {
	tmpDir := t.TempDir()
	reportContent := "# Test Report\n\nRun ID: mock-1"
	_ = os.WriteFile(filepath.Join(tmpDir, "mock-1.md"), []byte(reportContent), 0644)

	benchResultsDir = tmpDir
	defer func() { benchResultsDir = "benchmarks/results" }()

	// List reports
	if err := benchReportCmd.RunE(benchReportCmd, []string{}); err != nil {
		t.Fatalf("benchReportCmd (list) error: %v", err)
	}

	// Show specific report
	if err := benchReportCmd.RunE(benchReportCmd, []string{"mock-1"}); err != nil {
		t.Fatalf("benchReportCmd (show) error: %v", err)
	}

	// Missing report
	if err := benchReportCmd.RunE(benchReportCmd, []string{"nonexistent"}); err == nil {
		t.Fatal("expected error for nonexistent report, got nil")
	}
}
