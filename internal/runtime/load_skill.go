package runtime

import (
	"context"
	"errors"
	"fmt"

	"forcefield/internal/skills"
	"forcefield/internal/tools"
)

// loadSkillTool is the model-facing tool for loading a skill's full body
// by id. It delegates entirely to the Runtime's in-memory skills.Store —
// no filesystem scan of the skills directory happens here.
type loadSkillTool struct {
	store *skills.Store
}

func newLoadSkillTool(store *skills.Store) *loadSkillTool {
	return &loadSkillTool{store: store}
}

func (loadSkillTool) Name() string { return "load_skill" }

func (loadSkillTool) Description() string {
	return "Load the full Markdown body of a skill by its id from the skill catalog. " +
		"Use this when a listed skill is relevant to the current task and you need its full guidance."
}

func (loadSkillTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				"type":        "string",
				"description": "The skill id from the catalog (e.g. \"architecture-review\").",
			},
		},
		"required": []string{"id"},
	}
}

func (t *loadSkillTool) Execute(_ context.Context, args map[string]any) (tools.Result, error) {
	id, err := tools.StringArg(args, "id")
	if err != nil {
		return tools.Result{}, err
	}

	body, err := t.store.Load(id)
	if err != nil {
		if errors.Is(err, skills.ErrSkillNotFound) {
			return tools.Result{
				IsError: true,
				Content: fmt.Sprintf("skill %q not found in the catalog", id),
			}, nil
		}
		return tools.Result{}, err
	}

	return tools.Result{Content: body}, nil
}
