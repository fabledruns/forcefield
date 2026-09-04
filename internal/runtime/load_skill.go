package runtime

import (
	"context"
	"errors"
	"fmt"

	"forcefield/internal/skills"
	"forcefield/internal/tools"
)

// loadSkillTool loads a skill body from the runtime's in-memory store,
// scoped to the active agent's skill set. A skill body is knowledge, not
// capability: loading is refused for IDs outside the agent's assignment,
// and loading never grants tool access (tools resolve exclusively through
// the filtered tool manager).
type loadSkillTool struct {
	store *skills.Store
	// allowed returns the exact skill IDs the active agent may load.
	// Set by the runtime after construction; nil means "no scoping".
	allowed func() map[string]bool
	// agentName reports the active agent for error messages.
	agentName func() string
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

	// Agent scoping first, with exact-ID match only (no normalization
	// fallthrough): a skill must never be granted by naming similarity,
	// and a missing skill must never resolve to a different skill.
	if t.allowed != nil {
		if !t.allowed()[id] {
			agent := "the active agent"
			if t.agentName != nil {
				agent = fmt.Sprintf("agent %q", t.agentName())
			}
			if _, exists := t.store.Get(id); exists {
				return tools.Result{
					IsError: true,
					Content: fmt.Sprintf("skill %q exists but is not available to %s", id, agent),
				}, nil
			}
			return tools.Result{
				IsError: true,
				Content: fmt.Sprintf("skill %q not found in the catalog", id),
			}, nil
		}
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
