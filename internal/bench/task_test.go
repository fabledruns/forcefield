package bench

import (
	"testing"
	"time"
)

func TestTask_ValidateSuccess(t *testing.T) {
	task := &Task{
		ID:           "MOD-001",
		Name:         "Slugify",
		Category:     CategorySimpleCodeModifications,
		Difficulty:   DifficultyEasy,
		Instructions: "Fix slug.go",
		Timeout:      "45s",
		Validation: ValidationSpec{
			Commands:    [][]string{{"go", "test", "./..."}},
			MaxDuration: "20s",
		},
	}

	if err := task.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	if task.ParsedTimeout() != 45*time.Second {
		t.Errorf("ParsedTimeout() = %v, want 45s", task.ParsedTimeout())
	}
	if task.Validation.ParsedValidationTimeout() != 20*time.Second {
		t.Errorf("ParsedValidationTimeout() = %v, want 20s", task.Validation.ParsedValidationTimeout())
	}
}

func TestTask_ValidateFailures(t *testing.T) {
	cases := []struct {
		name string
		task *Task
	}{
		{
			name: "missing id",
			task: &Task{Name: "Foo", Category: CategoryDebugging, Instructions: "do it", Validation: ValidationSpec{Commands: [][]string{{"go", "test"}}}},
		},
		{
			name: "missing name",
			task: &Task{ID: "T-1", Category: CategoryDebugging, Instructions: "do it", Validation: ValidationSpec{Commands: [][]string{{"go", "test"}}}},
		},
		{
			name: "invalid category",
			task: &Task{ID: "T-1", Name: "Foo", Category: "invalid_category", Instructions: "do it", Validation: ValidationSpec{Commands: [][]string{{"go", "test"}}}},
		},
		{
			name: "invalid difficulty",
			task: &Task{ID: "T-1", Name: "Foo", Category: CategoryDebugging, Difficulty: "extreme", Instructions: "do it", Validation: ValidationSpec{Commands: [][]string{{"go", "test"}}}},
		},
		{
			name: "missing instructions",
			task: &Task{ID: "T-1", Name: "Foo", Category: CategoryDebugging, Validation: ValidationSpec{Commands: [][]string{{"go", "test"}}}},
		},
		{
			name: "missing validation commands",
			task: &Task{ID: "T-1", Name: "Foo", Category: CategoryDebugging, Instructions: "do it"},
		},
		{
			name: "empty command element",
			task: &Task{ID: "T-1", Name: "Foo", Category: CategoryDebugging, Instructions: "do it", Validation: ValidationSpec{Commands: [][]string{{""}}}},
		},
		{
			name: "invalid timeout string",
			task: &Task{ID: "T-1", Name: "Foo", Category: CategoryDebugging, Instructions: "do it", Timeout: "invalid", Validation: ValidationSpec{Commands: [][]string{{"go", "test"}}}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.task.Validate(); err == nil {
				t.Fatalf("expected validation error for case %q, got nil", tc.name)
			}
		})
	}
}

func TestLoadTaskFromBytes(t *testing.T) {
	yamlData := []byte(`
id: "TEST-01"
name: "Test Task"
category: "debugging"
difficulty: "medium"
instructions: "Fix the bug"
validation:
  commands:
    - ["go", "test", "./..."]
`)
	task, err := LoadTaskFromBytes(yamlData)
	if err != nil {
		t.Fatalf("LoadTaskFromBytes failed: %v", err)
	}
	if task.ID != "TEST-01" || task.Category != CategoryDebugging {
		t.Fatalf("unexpected task parsed: %+v", task)
	}
}
