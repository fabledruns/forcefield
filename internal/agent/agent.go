// Package agent defines the Agent type: an identity (name + base system
// prompt) plus whatever skills have been loaded for it. It knows how to
// combine those into the final system prompt sent to a model.
package agent

import "strings"

// Agent holds everything needed to build a system prompt for a single run.
type Agent struct {
    Name         string
    SystemPrompt string
    SkillCatalog string
}

// New constructs an Agent from a base system prompt and the concatenated
// skills text (which may be empty if no skills are defined).
func New(name, systemPrompt, skillCatalog string) *Agent {
	return &Agent{
		Name:         name,
		SystemPrompt: strings.TrimSpace(systemPrompt),
		SkillCatalog: strings.TrimSpace(skillCatalog),
	}
}

// BuildSystemPrompt combines the agent's base system prompt with its
// loaded skills into the single string sent to the model as the "system"
// message. If there are no skills, this is just the base prompt.
func (a *Agent) BuildSystemPrompt() string {
	if a.SkillCatalog == "" {
		return a.SystemPrompt
	}

	var b strings.Builder

	b.WriteString(strings.TrimSpace(a.SystemPrompt))
	b.WriteString(`
## Skill Catalog

You have access to a catalog of reusable skills.

The list below is the complete catalog of skills currently available.

You already know:
- each skill's ID
- its name
- its description

You do NOT know the contents of any skill unless you explicitly load it.

If a task would benefit from a skill, call the "load_skill" tool using the skill's ID.

You may freely list or recommend any skill from this catalog without calling the tool.

Only use "load_skill" when you need the full instructions contained in a skill.

Available skills:

`)

	b.WriteString(a.SkillCatalog)

	return b.String()
}