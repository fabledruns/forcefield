package bench

import (
	"path/filepath"
	"strings"
)

// Failure types categorized for evaluation and diagnosis.
const (
	FailureNone        = ""
	FailureAgent       = "agent"        // Agent finished or gave up, but tests failed or modifications were invalid
	FailureTest        = "test"         // Tests failed after valid changes
	FailureInfra       = "infra"        // Workspace setup, git error, or local runtime initialization fault
	FailureTimeout     = "timeout"      // Execution exceeded maximum allowable task duration
	FailureInvalidTask = "invalid_task" // Manifest definition malformed or missing required specifications
)

// TokenUsage carries token statistics reported by the provider.
// If the provider does not report token accounting, Available is false.
type TokenUsage struct {
	Available        bool `json:"available"`
	PromptTokens     int  `json:"prompt_tokens,omitempty"`
	CompletionTokens int  `json:"completion_tokens,omitempty"`
	TotalTokens      int  `json:"total_tokens,omitempty"`
}

// TaskMetrics records fine-grained execution metrics for one benchmark task.
type TaskMetrics struct {
	TaskID                  string           `json:"task_id"`
	Name                    string           `json:"name"`
	Category                string           `json:"category"`
	Difficulty              string           `json:"difficulty"`
	Status                  string           `json:"status"` // "passed", "failed", "timeout", "blocked", "cancelled", "error"
	Passed                  bool             `json:"passed"`
	FailureType             string           `json:"failure_type,omitempty"`
	FailureReason           string           `json:"failure_reason,omitempty"`
	DurationMS              int64            `json:"duration_ms"`
	AgentTurns              int              `json:"agent_turns"`
	AgentRetries            int              `json:"agent_retries"`
	ToolCalls               int              `json:"tool_calls"`
	ToolCallBreakdown       map[string]int   `json:"tool_call_breakdown,omitempty"`
	ToolFailures            int              `json:"tool_failures"`
	CommandFailures         int              `json:"command_failures"`
	Tokens                  TokenUsage       `json:"tokens"`
	Delegations             int              `json:"delegations"`
	FilesChanged            []string         `json:"files_changed,omitempty"`
	FilesChangedCount       int              `json:"files_changed_count"`
	UnexpectedFilesModified bool             `json:"unexpected_files_modified"`
	DiffPassesValidation    bool             `json:"diff_passes_validation"`
	WorkspaceDir            string           `json:"workspace_dir,omitempty"`
	RetainedWorkspace       bool             `json:"retained_workspace,omitempty"`
	Validation              ValidationResult `json:"validation"`
	Diff                    string           `json:"diff,omitempty"`
}

// CheckUnexpectedFiles compares the modified files against the task's ExpectedFiles list.
func CheckUnexpectedFiles(modifiedFiles, expectedFiles []string) bool {
	if len(expectedFiles) == 0 {
		return false
	}
	expectedSet := make(map[string]bool)
	for _, f := range expectedFiles {
		norm := filepath.ToSlash(strings.TrimSpace(f))
		expectedSet[norm] = true
	}

	for _, m := range modifiedFiles {
		norm := filepath.ToSlash(strings.TrimSpace(m))
		if !expectedSet[norm] {
			return true
		}
	}
	return false
}
