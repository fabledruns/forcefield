package bench

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/runtime"
	"forcefield/internal/task"
)

// TestAdversarial_PathTraversalInFixtures verifies that tasks specifying paths with
// ".." or absolute paths cannot escape the isolated workspace.
func TestAdversarial_PathTraversalInFixtures(t *testing.T) {
	// 1. In Fixtures
	maliciousTask := &Task{
		ID:           "ADV-001",
		Name:         "Path Traversal Fixture",
		Category:     CategoryDebugging,
		Instructions: "do work",
		Fixtures: map[string]string{
			"../../evil.txt": "evil content",
		},
		Validation: ValidationSpec{
			Commands: [][]string{{"go", "test"}},
		},
	}

	if err := maliciousTask.Validate(); err == nil {
		t.Fatal("expected Task.Validate to reject fixture path escaping workspace with ../, got nil")
	}

	// 2. In CanonicalFiles
	maliciousTask2 := &Task{
		ID:           "ADV-002",
		Name:         "Path Traversal Canonical",
		Category:     CategoryDebugging,
		Instructions: "do work",
		CanonicalFiles: map[string]string{
			"/etc/evil": "evil content",
		},
		Validation: ValidationSpec{
			Commands: [][]string{{"go", "test"}},
		},
	}

	if err := maliciousTask2.Validate(); err == nil {
		t.Fatal("expected Task.Validate to reject absolute canonical path, got nil")
	}
}

// TestAdversarial_CleanupPathSafety verifies that WorkspaceManager refuses to delete
// directories outside its configured BaseDir.
func TestAdversarial_CleanupPathSafety(t *testing.T) {
	tmpBase := t.TempDir()
	mgr := NewWorkspaceManager(tmpBase, false, false)

	outsideDir := t.TempDir()

	ws := &Workspace{
		Dir: outsideDir,
	}

	err := mgr.Cleanup(ws, true)
	if err == nil {
		t.Fatal("expected Cleanup to refuse deleting directory outside BaseDir, got nil")
	}

	// Verify outsideDir was NOT deleted
	if _, err := os.Stat(outsideDir); err != nil {
		t.Fatalf("outsideDir was deleted: %v", err)
	}
}

// TestAdversarial_NewFileDiffInclusion verifies that newly created untracked files
// are captured in WorkspaceManager.Diff.
func TestAdversarial_NewFileDiffInclusion(t *testing.T) {
	tmpBase := t.TempDir()
	mgr := NewWorkspaceManager(tmpBase, false, false)

	task := &Task{
		ID:       "ADV-DIFF",
		Category: CategorySimpleCodeModifications,
		Fixtures: map[string]string{
			"init.txt": "initial",
		},
	}

	ws, err := mgr.Prepare(context.Background(), task, "diff-run")
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer mgr.Cleanup(ws, true)

	// Create a brand new file
	newFilePath := filepath.Join(ws.Dir, "brand_new.go")
	if err := os.WriteFile(newFilePath, []byte("package foo\nfunc Bar() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	files, diffText, err := mgr.Diff(context.Background(), ws)
	if err != nil {
		t.Fatalf("Diff error: %v", err)
	}

	found := false
	for _, f := range files {
		if f == "brand_new.go" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("brand_new.go not found in Diff files list: %v", files)
	}

	if !strings.Contains(diffText, "brand_new.go") || !strings.Contains(diffText, "package foo") {
		t.Fatalf("diffText does not include new file contents: %q", diffText)
	}
}

// TestAdversarial_TurnsAndRetriesTracking verifies that TaskMetrics correctly records
// agent turns and retries from runtime events.
func TestAdversarial_TurnsAndRetriesTracking(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, "tasks")
	resultsDir := filepath.Join(tmpDir, "results")
	_ = os.MkdirAll(tasksDir, 0755)

	taskYAML := `
id: "TURNS-001"
name: "Turns Test Task"
category: "simple_code_modifications"
instructions: "Test turns"
fixtures:
  "go.mod": "module turnstest\ngo 1.21\n"
  "turns_test.go": "package turnstest\nimport \"testing\"\nfunc TestOK(t *testing.T) {}\n"
validation:
  commands: [["go", "test", "-v", "./..."]]
scoring:
  pass_on_zero_exit: true
`
	_ = os.WriteFile(filepath.Join(tasksDir, "task.yaml"), []byte(taskYAML), 0644)

	runner := NewRunner(RunnerOptions{
		TasksDir:   tasksDir,
		ResultsDir: resultsDir,
	})

	tTask, err := LoadTask(filepath.Join(tasksDir, "task.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	ws, err := runner.wsManager.Prepare(context.Background(), tTask, "turns-run")
	if err != nil {
		t.Fatal(err)
	}
	defer runner.wsManager.Cleanup(ws, true)

	// Simulate event stream with 3 iterations and 2 tool retries
	metrics := TaskMetrics{
		TaskID: tTask.ID,
	}

	events := []runtime.Event{
		{
			Type:      runtime.EventToolStart,
			ToolCall:  &providers.ToolCall{ID: "c1", Name: "shell"},
			TaskState: &task.Snapshot{Iteration: 1},
		},
		{
			Type: runtime.EventToolFinish,
			ToolResult: &runtime.ToolResult{
				ToolCallID: "c1",
				Name:       "shell",
				Attempt:    2, // retry 1
				Success:    true,
			},
			TaskState: &task.Snapshot{Iteration: 1},
		},
		{
			Type:      runtime.EventToolStart,
			ToolCall:  &providers.ToolCall{ID: "c2", Name: "shell"},
			TaskState: &task.Snapshot{Iteration: 2},
		},
		{
			Type: runtime.EventToolFinish,
			ToolResult: &runtime.ToolResult{
				ToolCallID: "c2",
				Name:       "shell",
				Attempt:    2, // retry 2
				Success:    true,
			},
			TaskState: &task.Snapshot{Iteration: 2},
		},
		{
			Type:      runtime.EventDone,
			TaskState: &task.Snapshot{Iteration: 3},
		},
	}

	for _, ev := range events {
		if ev.TaskState != nil && ev.TaskState.Iteration > metrics.AgentTurns {
			metrics.AgentTurns = ev.TaskState.Iteration
		}
		if ev.ToolResult != nil && ev.ToolResult.Attempt > 1 {
			metrics.AgentRetries += (ev.ToolResult.Attempt - 1)
		}
	}

	if metrics.AgentTurns != 3 {
		t.Fatalf("AgentTurns = %d, want 3", metrics.AgentTurns)
	}
	if metrics.AgentRetries != 2 {
		t.Fatalf("AgentRetries = %d, want 2", metrics.AgentRetries)
	}
}

// TestAdversarial_DelegationEnforcement verifies that when a task requires delegation,
// an agent that completes the task directly without calling delegate fails.
func TestAdversarial_DelegationEnforcement(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, "tasks")
	resultsDir := filepath.Join(tmpDir, "results")
	_ = os.MkdirAll(tasksDir, 0755)

	taskYAML := `
id: "DELEG-TEST"
name: "Delegation Required Task"
category: "parallel_delegation"
instructions: "Use delegate to solve"
fixtures:
  "go.mod": "module delegtest\ngo 1.21\n"
  "deleg_test.go": "package delegtest\nimport \"testing\"\nfunc TestOK(t *testing.T) {}\n"
validation:
  commands: [["go", "test", "-v", "./..."]]
scoring:
  pass_on_zero_exit: true
  require_delegation: true
`
	_ = os.WriteFile(filepath.Join(tasksDir, "task.yaml"), []byte(taskYAML), 0644)

	// Mock server that returns Done without delegating
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"direct completion without delegate"}}]}`+"\n\n")
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

	// Must fail because require_delegation was true but delegations was 0!
	if res.Tasks[0].Passed {
		t.Fatal("expected task to FAIL when require_delegation is true and 0 delegations occurred, got PASS")
	}
	if res.Tasks[0].FailureType != FailureAgent {
		t.Fatalf("FailureType = %q, want %q", res.Tasks[0].FailureType, FailureAgent)
	}
	if !strings.Contains(res.Tasks[0].FailureReason, "delegat") {
		t.Fatalf("FailureReason %q does not mention delegation", res.Tasks[0].FailureReason)
	}
}

// TestAdversarial_UnexpectedFilesEnforcement verifies that modifying files outside
// ExpectedFiles causes the task to fail when DisallowUnexpectedFiles is set.
func TestAdversarial_UnexpectedFilesEnforcement(t *testing.T) {
	tmpDir := t.TempDir()
	tasksDir := filepath.Join(tmpDir, "tasks")
	resultsDir := filepath.Join(tmpDir, "results")
	_ = os.MkdirAll(tasksDir, 0755)

	taskYAML := `
id: "UNEXP-001"
name: "Unexpected Files Task"
category: "simple_code_modifications"
instructions: "Only modify allowed.go"
expected_files:
  - "allowed.go"
fixtures:
  "go.mod": "module unexptest\ngo 1.21\n"
  "allowed.go": "package unexptest\n"
  "unexp_test.go": "package unexptest\nimport \"testing\"\nfunc TestOK(t *testing.T) {}\n"
validation:
  commands: [["go", "test", "-v", "./..."]]
scoring:
  pass_on_zero_exit: true
  disallow_unexpected_files: true
`
	_ = os.WriteFile(filepath.Join(tasksDir, "task.yaml"), []byte(taskYAML), 0644)

	// Mock server that modifies both allowed.go and untracked cheat file
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Simulate tool calls modifying allowed.go and creating evil.go
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"evil.go\",\"content\":\"package unexptest\"}"}}]}}]}`+"\n\n")
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

	if res.Tasks[0].Passed {
		t.Fatal("expected task to FAIL when unexpected file evil.go was created, got PASS")
	}
	if !res.Tasks[0].UnexpectedFilesModified {
		t.Fatal("expected UnexpectedFilesModified to be true")
	}
}
