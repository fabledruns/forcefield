package runtime

import (
	"context"
	"fmt"

	"forcefield/internal/task"
	"forcefield/internal/tools"
)

// updateTaskStateTool is the model-facing tool for maintaining the current
// task's plan and working memory. It reads the *task.State for the
// in-flight run from ctx (set by Runtime.run via task.WithState), so it
// needs no per-run registration: one instance is registered once, like
// load_skill.
type updateTaskStateTool struct{}

func newUpdateTaskStateTool() *updateTaskStateTool { return &updateTaskStateTool{} }

func (updateTaskStateTool) Name() string { return "update_task_state" }

func (updateTaskStateTool) Description() string {
	return "Record or update your plan and working memory for the current task: the plan " +
		"(a list of steps with status pending/in_progress/done/blocked), the current phase, " +
		"the step you're on, a discovery worth remembering, a blocker, and your verification " +
		"status (none/in_progress/passed/failed). Call this to keep track of a multi-step task " +
		"instead of re-deriving it from scratch every turn. Only fields you provide are changed; " +
		"omit anything unchanged. Set verification to \"passed\" only after you have actually run " +
		"a check (e.g. tests) and confirmed it succeeded - never report completion without this."
}

func (updateTaskStateTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"phase": map[string]any{
				"type":        "string",
				"description": "Short label for what you're currently doing, e.g. \"investigating failure\".",
			},
			"plan": map[string]any{
				"type":        "array",
				"description": "Replaces the entire plan. Send the full list, including already-done steps with their status, every time you update it.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text":   map[string]any{"type": "string"},
						"status": map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "done", "blocked"}},
					},
					"required": []string{"text", "status"},
				},
			},
			"current_step": map[string]any{
				"type":        "string",
				"description": "What you are working on right now.",
			},
			"discovery": map[string]any{
				"type":        "string",
				"description": "One important thing you learned, worth remembering for later turns.",
			},
			"blocker": map[string]any{
				"type":        "string",
				"description": "Something preventing progress. Appended to the open blockers list.",
			},
			"clear_blockers": map[string]any{
				"type":        "boolean",
				"description": "Set true to clear all open blockers (e.g. once resolved).",
			},
			"verification": map[string]any{
				"type":        "string",
				"enum":        []string{"none", "in_progress", "passed", "failed"},
				"description": "Your current verification status for the task's changes.",
			},
			"verification_note": map[string]any{
				"type":        "string",
				"description": "What you checked and what happened, e.g. \"go test ./... passed, 42 tests\".",
			},
			"status": map[string]any{
				"type":        "string",
				"enum":        []string{"in_progress", "verified", "partial", "blocked", "failed"},
				"description": "Overall task status, if you want to set it explicitly.",
			},
		},
	}
}

func (updateTaskStateTool) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	st, ok := task.FromContext(ctx)
	if !ok {
		return tools.Result{IsError: true, Content: "no task state available for this run"}, nil
	}

	patch := task.Patch{
		Phase:            stringField(args, "phase"),
		CurrentStep:      stringField(args, "current_step"),
		Discovery:        stringField(args, "discovery"),
		Blocker:          stringField(args, "blocker"),
		ClearBlockers:    boolField(args, "clear_blockers"),
		Verification:     task.Verification(stringField(args, "verification")),
		VerificationNote: stringField(args, "verification_note"),
		Status:           task.Status(stringField(args, "status")),
	}

	if raw, ok := args["plan"]; ok && raw != nil {
		steps, err := parsePlan(raw)
		if err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("invalid plan: %v", err)}, nil
		}
		patch.Plan = steps
	}

	st.Apply(patch)

	summary := st.Summary()
	if summary == "" {
		summary = "task state updated"
	}
	return tools.Result{Content: summary}, nil
}

func stringField(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func boolField(args map[string]any, key string) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return false
	}
	b, _ := v.(bool)
	return b
}

func parsePlan(raw any) ([]task.Step, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("expected an array")
	}

	steps := make([]task.Step, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("step %d: expected an object", i)
		}
		text := stringField(m, "text")
		if text == "" {
			return nil, fmt.Errorf("step %d: \"text\" is required", i)
		}
		status := task.StepStatus(stringField(m, "status"))
		switch status {
		case task.StepPending, task.StepInProgress, task.StepDone, task.StepBlocked:
		case "":
			status = task.StepPending
		default:
			return nil, fmt.Errorf("step %d: unknown status %q", i, status)
		}
		steps = append(steps, task.Step{Text: text, Status: status})
	}
	return steps, nil
}
