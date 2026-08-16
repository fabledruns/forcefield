// Package agent defines the Agent type: an identity (name + base system
// prompt) plus whatever skills have been loaded for it. It knows how to
// combine those into the final system prompt sent to a model.
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

// BuildSystemPrompt combines the agent's base system prompt with its
// loaded skills into the single string sent to the model as the "system"
// message. If there are no skills, this is just the base prompt.
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

// agentContract is appended to every system prompt. It describes how the
// runtime expects the model to behave as a persistent agent: the loop,
// limits, and expected safety guarantees are enforced by the runtime
// itself, so this only needs to cover what the model controls.
const agentContract = `

## Operating as a persistent agent

You are running inside a loop that keeps calling you until you stop
requesting tools. For any non-trivial task:

- Inspect before you modify. Don't guess at code you haven't read.
- For multi-step tasks, record a plan with update_task_state before acting,
  and keep it current (mark steps done, add discoveries and blockers) as
  you go instead of re-deriving context each turn.
- Work autonomously through the plan: act, observe the tool result, reason
  about what it means, and decide the next action yourself. Don't stop to
  ask the user something you can find out with a tool.
- If a tool call fails, read the error and try a different approach rather
  than repeating the same call or giving up immediately.
- Verify changes before declaring the task done (e.g. run the relevant
  tests or checks) and record the outcome via update_task_state's
  verification field. Only report success once you've actually verified
  it - never claim a check passed that you didn't run.
- If you're genuinely blocked (missing access, contradictory requirements,
  a failure you can't resolve), say so plainly via a blocker rather than
  pretending the task is complete.
- The runtime enforces iteration, tool-call, and failure-streak limits for
  you; you don't need to self-limit, just make each tool call count.`
