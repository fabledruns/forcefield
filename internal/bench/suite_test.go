package bench

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverAndFilterTasks(t *testing.T) {
	tmpDir := t.TempDir()

	// Write 2 tasks
	task1 := `
id: "TASK-001"
name: "Task One"
category: "simple_code_modifications"
difficulty: "easy"
instructions: "do task 1"
validation:
  commands: [["go", "test"]]
`
	task2 := `
id: "TASK-002"
name: "Task Two"
category: "debugging"
difficulty: "hard"
instructions: "do task 2"
validation:
  commands: [["go", "test"]]
`
	if err := os.WriteFile(filepath.Join(tmpDir, "task1.yaml"), []byte(task1), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "task2.yaml"), []byte(task2), 0644); err != nil {
		t.Fatal(err)
	}

	tasks, err := DiscoverTasks(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverTasks failed: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("discovered tasks = %d, want 2", len(tasks))
	}

	// Filter by task ID
	f1 := FilterTasks(tasks, "TASK-001", "", "")
	if len(f1) != 1 || f1[0].ID != "TASK-001" {
		t.Fatalf("Filter by ID failed: %+v", f1)
	}

	// Filter by category
	f2 := FilterTasks(tasks, "", "debugging", "")
	if len(f2) != 1 || f2[0].ID != "TASK-002" {
		t.Fatalf("Filter by Category failed: %+v", f2)
	}

	// Filter by difficulty
	f3 := FilterTasks(tasks, "", "", "easy")
	if len(f3) != 1 || f3[0].ID != "TASK-001" {
		t.Fatalf("Filter by Difficulty failed: %+v", f3)
	}

	// Filter with no match
	f4 := FilterTasks(tasks, "NONEXISTENT", "", "")
	if len(f4) != 0 {
		t.Fatalf("Filter expected 0, got %d", len(f4))
	}

	// Test duplicate detection
	if err := os.WriteFile(filepath.Join(tmpDir, "task1_dup.yaml"), []byte(task1), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverTasks(tmpDir); err == nil {
		t.Fatal("expected error on duplicate task ID, got nil")
	}
}

func TestDiscoverTasks_RepositoryDataset(t *testing.T) {
	// Test the actual 15 tasks created in benchmarks/tasks
	tasks, err := DiscoverTasks("../../benchmarks/tasks")
	if err != nil {
		t.Fatalf("DiscoverTasks from repo tasks failed: %v", err)
	}
	if len(tasks) != 15 {
		t.Fatalf("Discovered tasks = %d, want exactly 15", len(tasks))
	}

	cats := Categories(tasks)
	if len(cats) != 4 {
		t.Fatalf("Categories count = %d, want 4 (%v)", len(cats), cats)
	}
}

func TestLoadEmbeddedTasks(t *testing.T) {
	tasks, err := LoadEmbeddedTasks()
	if err != nil {
		t.Fatalf("LoadEmbeddedTasks failed: %v", err)
	}
	if len(tasks) != 15 {
		t.Fatalf("LoadEmbeddedTasks count = %d, want 15", len(tasks))
	}
	cats := Categories(tasks)
	if len(cats) != 4 {
		t.Fatalf("embedded categories count = %d, want 4 (%v)", len(cats), cats)
	}
}
