package bench

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"forcefield/internal/config"
)

func TestRunner_InvalidTaskManifest(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, "tasks")
	resultsDir := filepath.Join(tmpDir, "results")
	if err := os.MkdirAll(tasksDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 1. Test discovering an invalid manifest triggers an error in DiscoverTasks/Run
	invalidYAML := `
id: "INV-001"
name: "Invalid Task"
category: "debugging"
instructions: "   "
validation:
  commands: [["go", "test"]]
`
	_ = os.WriteFile(filepath.Join(tasksDir, "invalid.yaml"), []byte(invalidYAML), 0644)

	runner := NewRunner(RunnerOptions{
		TasksDir:   tasksDir,
		ResultsDir: resultsDir,
	})

	_, err := runner.Run(context.Background())
	if err == nil {
		t.Fatal("expected runner error discovering invalid manifest, got nil")
	}

	// 2. Test runSingleTask directly classifies invalid task with FailureInvalidTask
	invalidTask := &Task{
		ID:           "INV-002",
		Category:     CategoryDebugging,
		Instructions: "", // missing instructions
	}
	metrics := runner.runSingleTask(context.Background(), invalidTask, &config.Config{}, "test-run")
	if metrics.Passed || metrics.FailureType != FailureInvalidTask {
		t.Fatalf("expected FailureInvalidTask, got %+v", metrics)
	}
}

func TestRunner_TimeoutHandling(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, "tasks")
	resultsDir := filepath.Join(tmpDir, "results")
	_ = os.MkdirAll(tasksDir, 0755)

	taskYAML := `
id: "TIMEOUT-001"
name: "Timeout Task"
category: "debugging"
instructions: "Do work"
timeout: "50ms"
validation:
  commands: [["go", "test"]]
`
	_ = os.WriteFile(filepath.Join(tasksDir, "task.yaml"), []byte(taskYAML), 0644)

	// Mock server that flushes headers and waits on request context
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer func() {
		server.CloseClientConnections()
		server.Close()
	}()

	baseCfg := &config.Config{
		Model: config.Model{
			Provider: "test-provider",
			Name:     "test-model",
		},
		Providers: map[string]config.ProviderConfig{
			"test-provider": {
				Type:    "openai-compatible",
				BaseURL: server.URL,
			},
		},
	}

	runner := NewRunner(RunnerOptions{
		TasksDir:        tasksDir,
		ResultsDir:      resultsDir,
		TimeoutOverride: 50 * time.Millisecond,
		BaseConfig:      baseCfg,
	})

	res, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("runner error: %v", err)
	}

	if len(res.Tasks) != 1 {
		t.Fatalf("tasks executed = %d, want 1", len(res.Tasks))
	}

	if res.Tasks[0].Status != "timeout" || res.Tasks[0].FailureType != FailureTimeout {
		t.Fatalf("task status = %q, failure = %q, want timeout", res.Tasks[0].Status, res.Tasks[0].FailureType)
	}
}

func TestRunner_Cancellation(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, "tasks")
	resultsDir := filepath.Join(tmpDir, "results")
	_ = os.MkdirAll(tasksDir, 0755)

	taskYAML := `
id: "CANCEL-001"
name: "Cancel Task"
category: "debugging"
instructions: "Do work"
validation:
  commands: [["go", "test"]]
`
	_ = os.WriteFile(filepath.Join(tasksDir, "task.yaml"), []byte(taskYAML), 0644)

	runner := NewRunner(RunnerOptions{
		TasksDir:   tasksDir,
		ResultsDir: resultsDir,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := runner.Run(ctx)
	if err == nil {
		t.Fatal("expected cancellation error from runner.Run, got nil")
	}
}

func TestRunner_EndToEndDeterministicAgent(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, "tasks")
	resultsDir := filepath.Join(tmpDir, "results")
	_ = os.MkdirAll(tasksDir, 0755)

	// Task that already has passing tests
	taskYAML := `
id: "PASS-001"
name: "Passing Task"
category: "simple_code_modifications"
instructions: "Already passing"
fixtures:
  "go.mod": |
    module passtest
    go 1.21
  "pass_test.go": |
    package passtest
    import "testing"
    func TestOK(t *testing.T) {}
validation:
  commands:
    - ["go", "test", "-v", "./..."]
scoring:
  pass_on_zero_exit: true
  require_files_modified: false
`
	_ = os.WriteFile(filepath.Join(tasksDir, "task.yaml"), []byte(taskYAML), 0644)

	// Mock server that simply returns a text response without tool calls
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Task completed"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer func() {
		server.CloseClientConnections()
		server.Close()
	}()

	baseCfg := &config.Config{
		Model: config.Model{
			Provider: "test-provider",
			Name:     "test-model",
		},
		Providers: map[string]config.ProviderConfig{
			"test-provider": {
				Type:    "openai-compatible",
				BaseURL: server.URL,
			},
		},
	}

	runner := NewRunner(RunnerOptions{
		TasksDir:   tasksDir,
		ResultsDir: resultsDir,
		BaseConfig: baseCfg,
	})

	res, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("runner.Run error: %v", err)
	}

	if len(res.Tasks) != 1 {
		t.Fatalf("tasks executed = %d, want 1", len(res.Tasks))
	}
	if !res.Tasks[0].Passed {
		t.Fatalf("expected task to pass, got: %+v", res.Tasks[0])
	}
	if res.Summary.PassedTasks != 1 || res.Summary.PassRate != 100.0 {
		t.Fatalf("summary mismatch: %+v", res.Summary)
	}

	// Verify reports were generated
	if res.JSONReportPath == "" || res.MDReportPath == "" {
		t.Fatalf("expected non-empty report paths, got JSON=%q, MD=%q", res.JSONReportPath, res.MDReportPath)
	}
	if _, err := os.Stat(res.JSONReportPath); err != nil {
		t.Fatalf("json report file missing on disk: %v", err)
	}
	if _, err := os.Stat(res.MDReportPath); err != nil {
		t.Fatalf("md report file missing on disk: %v", err)
	}
}
