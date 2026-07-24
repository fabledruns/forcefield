// Package agent defines the Agent type: an identity (name + base system
// prompt) plus whatever skills have been loaded for it. It knows how to
// combine those into the final system prompt sent to a model. It does not
// know about config files, HTTP, or the model provider — those are wired
// together by the runtime package.
package agent

import "strings"

// Agent holds everything needed to build a system prompt for a single run.
type Agent struct {
	Name         string
	SystemPrompt string
	Skills       string
}

// New constructs an Agent from a base system prompt and the concatenated
// skills text (which may be empty if no skills are defined).
func New(name, systemPrompt, skillsText string) *Agent {
	return &Agent{
		Name:         name,
		SystemPrompt: strings.TrimSpace(systemPrompt),
		Skills:       strings.TrimSpace(skillsText),
	}
}

// BuildSystemPrompt combines the agent's base system prompt with its
// loaded skills into the single string sent to the model as the "system"
// message. If there are no skills, this is just the base prompt.
func (a *Agent) BuildSystemPrompt() string {
	if a.Skills == "" {
		return a.SystemPrompt
	}

	var b strings.Builder
	b.WriteString(a.SystemPrompt)
	b.WriteString("\n\n")
	b.WriteString("# Skills\n\n")
	b.WriteString("The following skills describe additional expertise you have. Apply them when relevant.\n\n")
	b.WriteString(a.Skills)
	return b.String()
}
