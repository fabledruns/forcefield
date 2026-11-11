// Package bench implements the automated coding-agent benchmark runner for Forcefield.
package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Standard task categories.
const (
	CategorySimpleCodeModifications = "simple_code_modifications"
	CategoryMultiFileChanges        = "multi_file_changes"
	CategoryDebugging               = "debugging"
	CategoryParallelDelegation      = "parallel_delegation"
)

// ValidCategories is the set of allowed benchmark categories.
var ValidCategories = map[string]bool{
	CategorySimpleCodeModifications: true,
	CategoryMultiFileChanges:        true,
	CategoryDebugging:               true,
	CategoryParallelDelegation:      true,
}

// Standard difficulty levels.
const (
	DifficultyEasy   = "easy"
	DifficultyMedium = "medium"
	DifficultyHard   = "hard"
)

// ValidDifficulties is the set of recognized task difficulties.
var ValidDifficulties = map[string]bool{
	DifficultyEasy:   true,
	DifficultyMedium: true,
	DifficultyHard:   true,
}

// ValidationSpec defines how a task's outcome is verified independently.
type ValidationSpec struct {
	// Commands are the argv commands to run sequentially (e.g. [["go", "test", "-v", "./..."]]).
	Commands [][]string `yaml:"commands" json:"commands"`
	// RestoreCanonical, if true, re-applies CanonicalFiles right before validation
	// to prevent the agent from passing by deleting or altering the test assertions.
	RestoreCanonical bool `yaml:"restore_canonical_tests,omitempty" json:"restore_canonical_tests,omitempty"`
	// MaxDuration caps the validation phase duration (e.g. "30s").
	MaxDuration string `yaml:"max_duration,omitempty" json:"max_duration,omitempty"`
}

// ScoringSpec defines the pass criteria for the task.
type ScoringSpec struct {
	// PassOnZeroExit requires all validation commands to exit with 0.
	PassOnZeroExit bool `yaml:"pass_on_zero_exit" json:"pass_on_zero_exit"`
	// RequireFilesModified fails the task if the agent made no file edits.
	RequireFilesModified bool `yaml:"require_files_modified" json:"require_files_modified"`
}

// Task is a declarative specification of a benchmark task.
type Task struct {
	ID             string            `yaml:"id" json:"id"`
	Name           string            `yaml:"name" json:"name"`
	Category       string            `yaml:"category" json:"category"`
	Difficulty     string            `yaml:"difficulty" json:"difficulty"`
	Description    string            `yaml:"description" json:"description"`
	Instructions   string            `yaml:"instructions" json:"instructions"`
	Timeout        string            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	ExpectedFiles  []string          `yaml:"expected_files,omitempty" json:"expected_files,omitempty"`
	ForbiddenFiles []string          `yaml:"forbidden_files,omitempty" json:"forbidden_files,omitempty"`
	Validation     ValidationSpec    `yaml:"validation" json:"validation"`
	Scoring        ScoringSpec       `yaml:"scoring,omitempty" json:"scoring,omitempty"`
	Fixtures       map[string]string `yaml:"fixtures,omitempty" json:"fixtures,omitempty"`
	CanonicalFiles map[string]string `yaml:"canonical_files,omitempty" json:"canonical_files,omitempty"`

	// ManifestPath is the absolute path to the task manifest file on disk.
	ManifestPath string `yaml:"-" json:"-"`
	// Dir is the directory containing the task manifest.
	Dir string `yaml:"-" json:"-"`
}

// ParsedTimeout returns the parsed timeout duration or a default of 2 minutes.
func (t *Task) ParsedTimeout() time.Duration {
	if t.Timeout == "" {
		return 2 * time.Minute
	}
	d, err := time.ParseDuration(t.Timeout)
	if err != nil || d <= 0 {
		return 2 * time.Minute
	}
	return d
}

// ParsedValidationTimeout returns the timeout for validation or a default of 30 seconds.
func (v *ValidationSpec) ParsedValidationTimeout() time.Duration {
	if v.MaxDuration == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(v.MaxDuration)
	if err != nil || d <= 0 {
		return 30 * time.Second
	}
	return d
}

// Validate checks task definition integrity.
func (t *Task) Validate() error {
	t.ID = strings.TrimSpace(t.ID)
	if t.ID == "" {
		return fmt.Errorf("task id is required")
	}
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" {
		return fmt.Errorf("task %q: name is required", t.ID)
	}
	t.Category = strings.TrimSpace(t.Category)
	if !ValidCategories[t.Category] {
		return fmt.Errorf("task %q: invalid category %q (valid: simple_code_modifications, multi_file_changes, debugging, parallel_delegation)", t.ID, t.Category)
	}
	t.Difficulty = strings.TrimSpace(t.Difficulty)
	if t.Difficulty != "" && !ValidDifficulties[t.Difficulty] {
		return fmt.Errorf("task %q: invalid difficulty %q (valid: easy, medium, hard)", t.ID, t.Difficulty)
	}
	if strings.TrimSpace(t.Instructions) == "" {
		return fmt.Errorf("task %q: instructions are required", t.ID)
	}
	if len(t.Validation.Commands) == 0 {
		return fmt.Errorf("task %q: at least one validation command is required", t.ID)
	}
	for i, cmd := range t.Validation.Commands {
		if len(cmd) == 0 || strings.TrimSpace(cmd[0]) == "" {
			return fmt.Errorf("task %q: validation command [%d] is empty", t.ID, i)
		}
	}
	if t.Timeout != "" {
		if _, err := time.ParseDuration(t.Timeout); err != nil {
			return fmt.Errorf("task %q: invalid timeout %q: %w", t.ID, t.Timeout, err)
		}
	}
	if t.Validation.MaxDuration != "" {
		if _, err := time.ParseDuration(t.Validation.MaxDuration); err != nil {
			return fmt.Errorf("task %q: invalid validation max_duration %q: %w", t.ID, t.Validation.MaxDuration, err)
		}
	}
	return nil
}

// LoadTask loads and validates a task manifest from a YAML file.
func LoadTask(path string) (*Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read task file %q: %w", path, err)
	}
	task, err := LoadTaskFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("parse task file %q: %w", path, err)
	}
	absPath, err := filepath.Abs(path)
	if err == nil {
		task.ManifestPath = absPath
		task.Dir = filepath.Dir(absPath)
	} else {
		task.ManifestPath = path
		task.Dir = filepath.Dir(path)
	}
	return task, nil
}

// LoadTaskFromBytes parses and validates a task manifest from raw YAML bytes.
func LoadTaskFromBytes(data []byte) (*Task, error) {
	var task Task
	if err := yaml.Unmarshal(data, &task); err != nil {
		return nil, fmt.Errorf("unmarshal task yaml: %w", err)
	}
	if err := task.Validate(); err != nil {
		return nil, err
	}
	return &task, nil
}
