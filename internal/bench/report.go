package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"forcefield/internal/redact"
)

// SchemaVersion is the current version of the benchmark results JSON schema.
const SchemaVersion = "1.0"

// JSONReport is the top-level structured report written to disk.
type JSONReport struct {
	SchemaVersion     string        `json:"schema_version"`
	RunID             string        `json:"run_id"`
	Timestamp         string        `json:"timestamp"`
	ForcefieldVersion string        `json:"forcefield_version"`
	GitCommit         string        `json:"git_commit,omitempty"`
	Provider          string        `json:"provider,omitempty"`
	Model             string        `json:"model,omitempty"`
	Summary           Summary       `json:"summary"`
	Tasks             []TaskMetrics `json:"tasks"`
}

// scrubMetrics ensures no credentials leak into structured reports.
func scrubMetrics(m TaskMetrics) TaskMetrics {
	m.FailureReason = redact.Scrub(m.FailureReason)
	m.Validation.Stderr = redact.Scrub(m.Validation.Stderr)
	m.Validation.Stdout = redact.Scrub(m.Validation.Stdout)
	return m
}

// SaveJSONReport writes the benchmark results to a formatted JSON file.
func SaveJSONReport(res *RunResult, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	tasks := make([]TaskMetrics, len(res.Tasks))
	for i, t := range res.Tasks {
		tasks[i] = scrubMetrics(t)
	}

	report := JSONReport{
		SchemaVersion:     SchemaVersion,
		RunID:             res.RunID,
		Timestamp:         res.Timestamp,
		ForcefieldVersion: res.ForcefieldVersion,
		GitCommit:         res.GitCommit,
		Provider:          res.Provider,
		Model:             res.Model,
		Summary:           res.Summary,
		Tasks:             tasks,
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json report: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

// GenerateMarkdownReport formats the benchmark run result into a clean GitHub-flavored Markdown document.
func GenerateMarkdownReport(res *RunResult) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("# Forcefield Coding-Agent Benchmark Report: `%s`\n\n", res.RunID))

	// Metadata
	b.WriteString("## Run Metadata\n\n")
	b.WriteString("| Property | Value |\n")
	b.WriteString("| :--- | :--- |\n")
	b.WriteString(fmt.Sprintf("| **Timestamp** | %s |\n", res.Timestamp))
	b.WriteString(fmt.Sprintf("| **Forcefield Version** | %s |\n", res.ForcefieldVersion))
	if res.GitCommit != "" {
		b.WriteString(fmt.Sprintf("| **Git Commit** | `%s` |\n", res.GitCommit))
	}
	if res.Provider != "" {
		b.WriteString(fmt.Sprintf("| **Provider** | `%s` |\n", res.Provider))
	}
	if res.Model != "" {
		b.WriteString(fmt.Sprintf("| **Model** | `%s` |\n", res.Model))
	}
	b.WriteString("\n---\n\n")

	// Summary
	b.WriteString("## Overall Performance Summary\n\n")
	b.WriteString("| Metric | Value |\n")
	b.WriteString("| :--- | :--- |\n")
	b.WriteString(fmt.Sprintf("| **Total Tasks** | %d |\n", res.Summary.TotalTasks))
	b.WriteString(fmt.Sprintf("| **Tasks Passed** | %d |\n", res.Summary.PassedTasks))
	b.WriteString(fmt.Sprintf("| **Tasks Failed** | %d |\n", res.Summary.FailedTasks))
	b.WriteString(fmt.Sprintf("| **Pass Rate** | **%.1f%%** |\n", res.Summary.PassRate))
	b.WriteString(fmt.Sprintf("| **Total Duration** | %.2fs |\n", float64(res.Summary.TotalDurationMS)/1000.0))
	b.WriteString(fmt.Sprintf("| **Average Duration** | %.2fs |\n", float64(res.Summary.AvgDurationMS)/1000.0))
	b.WriteString(fmt.Sprintf("| **Total Tool Calls** | %d |\n", res.Summary.TotalToolCalls))
	b.WriteString(fmt.Sprintf("| **Tool Failures** | %d |\n", res.Summary.TotalToolFailures))
	b.WriteString(fmt.Sprintf("| **Delegations** | %d |\n", res.Summary.TotalDelegations))

	if res.Summary.Tokens.Available {
		b.WriteString(fmt.Sprintf("| **Tokens (Prompt / Completion / Total)** | %d / %d / %d |\n",
			res.Summary.Tokens.PromptTokens, res.Summary.Tokens.CompletionTokens, res.Summary.Tokens.TotalTokens))
	} else {
		b.WriteString("| **Token Accounting** | *Unavailable* |\n")
	}
	b.WriteString("\n---\n\n")

	// Category Breakdown
	b.WriteString("## Category Breakdown\n\n")
	b.WriteString("| Category | Total | Passed | Failed | Pass Rate | Avg Duration | Tool Calls |\n")
	b.WriteString("| :--- | :---: | :---: | :---: | :---: | :---: | :---: |\n")

	for cat, cs := range res.Summary.Categories {
		b.WriteString(fmt.Sprintf("| `%s` | %d | %d | %d | **%.1f%%** | %.2fs | %d |\n",
			cat, cs.TotalTasks, cs.PassedTasks, cs.FailedTasks, cs.PassRate, float64(cs.AvgDurationMS)/1000.0, cs.ToolCalls))
	}
	b.WriteString("\n---\n\n")

	// Task Results Table
	b.WriteString("## Individual Task Results\n\n")
	b.WriteString("| Task ID | Name | Category | Difficulty | Status | Validation | Duration | Tool Calls |\n")
	b.WriteString("| :--- | :--- | :--- | :---: | :---: | :---: | :---: | :---: |\n")

	for _, t := range res.Tasks {
		statusStr := "❌ Failed"
		if t.Passed {
			statusStr = "✅ Passed"
		} else if t.Status == "timeout" {
			statusStr = "⏰ Timeout"
		}

		vStr := "Fail"
		if t.Validation.Passed {
			vStr = "Pass"
		}

		b.WriteString(fmt.Sprintf("| `%s` | %s | `%s` | %s | %s | %s | %.2fs | %d |\n",
			t.TaskID, t.Name, t.Category, t.Difficulty, statusStr, vStr, float64(t.DurationMS)/1000.0, t.ToolCalls))
	}
	b.WriteString("\n---\n\n")

	// Detailed Diagnostics for Failed Tasks
	hasFailures := false
	for _, t := range res.Tasks {
		if !t.Passed {
			hasFailures = true
			break
		}
	}

	if hasFailures {
		b.WriteString("## Failure Diagnostics\n\n")
		for _, t := range res.Tasks {
			if t.Passed {
				continue
			}
			b.WriteString(fmt.Sprintf("### Task `%s`: %s\n\n", t.TaskID, t.Name))
			b.WriteString(fmt.Sprintf("- **Category:** `%s` (%s)\n", t.Category, t.Difficulty))
			b.WriteString(fmt.Sprintf("- **Failure Type:** `%s`\n", t.FailureType))
			if t.FailureReason != "" {
				b.WriteString(fmt.Sprintf("- **Failure Reason:** %s\n", redact.Scrub(t.FailureReason)))
			}
			if len(t.FilesChanged) > 0 {
				b.WriteString(fmt.Sprintf("- **Files Modified (%d):** `%s`\n", len(t.FilesChanged), strings.Join(t.FilesChanged, "`, `")))
			} else {
				b.WriteString("- **Files Modified:** *None*\n")
			}
			if t.UnexpectedFilesModified {
				b.WriteString("- **Warning:** Unexpected files were modified outside the declared boundary!\n")
			}
			if t.RetainedWorkspace {
				b.WriteString(fmt.Sprintf("- **Retained Workspace for Debugging:** `%s`\n", t.WorkspaceDir))
			}

			if t.Validation.Stderr != "" || t.Validation.Stdout != "" {
				b.WriteString("\n```text\n")
				if t.Validation.Stderr != "" {
					b.WriteString(redact.Scrub(t.Validation.Stderr) + "\n")
				}
				if t.Validation.Stdout != "" && len(t.Validation.Stdout) < 2000 {
					b.WriteString(redact.Scrub(t.Validation.Stdout) + "\n")
				}
				b.WriteString("```\n")
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// SaveMarkdownReport writes the Markdown report to disk.
func SaveMarkdownReport(res *RunResult, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	content := GenerateMarkdownReport(res)
	return os.WriteFile(path, []byte(content), 0644)
}
