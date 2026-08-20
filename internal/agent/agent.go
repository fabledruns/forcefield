// Package agent defines the Agent type: an identity (name + base system
// prompt), an operating contract, optional project memory, and whatever
// skills have been loaded for it. It knows how to combine those into the
// final system prompt sent to a model.
package agent

import "strings"

// Agent holds everything needed to build a system prompt for a single run.
type Agent struct {
	Name          string
	SystemPrompt  string
	SkillCatalog  string
	ProjectMemory string
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

// WithProjectMemory sets the formatted project memory (see
// memory.FormatForPrompt) that gets folded into the system prompt, and
// returns the agent for chaining. An empty string means "nothing remembered
// yet" and adds no section to the prompt.
func (a *Agent) WithProjectMemory(formatted string) *Agent {
	a.ProjectMemory = strings.TrimSpace(formatted)
	return a
}

// BuildSystemPrompt combines the agent's base system prompt, the
// operating contract, optional project memory, and optional skill catalog
// into the single string sent to the model as the "system" message.
func (a *Agent) BuildSystemPrompt() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(a.SystemPrompt))
	b.WriteString(agentContract)

	if a.ProjectMemory != "" {
		b.WriteString(`

## Project Memory

The following facts were remembered from previous sessions on this project:

`)
		b.WriteString(a.ProjectMemory)
		b.WriteString(`

Treat these as established context. Use the "add_project_memory" tool to add new durable facts worth remembering, but only after user approval.`)
	}

	if a.SkillCatalog == "" {
		return b.String()
	}

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
