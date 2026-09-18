package runtime

import (
	"forcefield/internal/tools"
)

// RunMode selects the tool and prompt policy for one run. The zero value
// is the normal chat policy; planning runs narrow it.
type RunMode int

const (
	// ModeChat is the normal agent policy: the active agent's full tool
	// set with its regular system prompt.
	ModeChat RunMode = iota
	// ModePlan is the /plan policy: a strictly read-only tool subset
	// with a planning overlay on the system prompt. The workspace cannot
	// be modified from a planning run.
	ModePlan
)

// planModeOverlay constrains planning turns to inspection and analysis.
// It is appended to the agent's system prompt for plan-mode runs (and
// re-applied on every turn refresh, which rebuilds the prompt).
const planModeOverlay = "\n\n## Plan Mode\n\n" +
	"You are producing an implementation plan. Inspect and analyze only: " +
	"do not modify the workspace, run commands, or start background jobs. " +
	"End with a numbered plan covering steps, files to change, tests to run, and risks."

// planModeTools is the strictly read-only tool subset available during
// planning turns. Anything that writes, executes, or mutates task state
// stays out: no write_file, shell, shell_job, update_task_state, or
// add_project_memory.
var planModeTools = map[string]bool{
	"read_file":    true,
	"list_files":   true,
	"search_files": true,
	"find_files":   true,
	"search_code":  true,
	"git":          true,
	"secret_scan":  true,
	"load_skill":   true,
}

// planOverlay returns the prompt overlay for mode, or "" for normal runs.
func (m RunMode) planOverlay() string {
	if m == ModePlan {
		return planModeOverlay
	}
	return ""
}

// planManager narrows m to the read-only planning subset, preserving
// registration order. Tool instances are reused, so executor and policy
// wiring are identical; the scheduler's fail-closed lookup keeps
// hallucinated mutating calls out exactly as it does for agents.
func planManager(m *tools.Manager) (*tools.Manager, error) {
	if m == nil {
		return nil, nil
	}
	keep := []string{}
	for _, t := range m.List() {
		if planModeTools[t.Name()] {
			keep = append(keep, t.Name())
		}
	}
	return m.Filtered(keep)
}
