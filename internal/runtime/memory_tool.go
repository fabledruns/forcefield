package runtime

import (
	"context"
	"fmt"

	"forcefield/internal/memory"
	"forcefield/internal/tools"
)

// addProjectMemoryTool persists approved project-scoped facts.
type addProjectMemoryTool struct {
	store *memory.Store
}

func newAddProjectMemoryTool(store *memory.Store) *addProjectMemoryTool {
	return &addProjectMemoryTool{store: store}
}

func (addProjectMemoryTool) Name() string { return "add_project_memory" }

func (addProjectMemoryTool) Description() string {
	return "Persist a short, durable fact about the current project so future sessions " +
		"start with it already known (e.g. build commands, conventions, architectural " +
		"decisions). Only save things worth remembering long-term, not task-specific " +
		"details. Requires user approval before it's actually written."
}

func (addProjectMemoryTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": "The fact to remember, as a single self-contained sentence.",
			},
		},
		"required": []string{"text"},
	}
}

func (t *addProjectMemoryTool) Execute(_ context.Context, args map[string]any) (tools.Result, error) {
	text, err := tools.StringArg(args, "text")
	if err != nil {
		return tools.Result{}, err
	}

	entry, added, err := t.store.Add(text)
	if err != nil {
		return tools.Result{IsError: true, Content: err.Error()}, nil
	}
	if !added {
		return tools.Result{Content: fmt.Sprintf("already remembered (id: %s): %s", entry.ID, entry.Text)}, nil
	}

	return tools.Result{Content: fmt.Sprintf("remembered (id: %s): %s", entry.ID, entry.Text)}, nil
}
