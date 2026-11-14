// Package agent defines the Agent type: an identity (name + base system
// prompt), an operating contract, optional project memory, and whatever
// skills have been loaded for it. It knows how to combine those into the
// final system prompt sent to a model.
package agent

import (
	"fmt"
	"strings"
)

// EnvironmentInfo captures immutable runtime environment metadata rendered
// into the agent system prompt so the model keeps an accurate model of host
// paths and execution boundaries across turns and context rebuilds.
type EnvironmentInfo struct {
	OS            string
	Arch          string
	WorkspaceRoot string
	WorkspaceMode string
	SandboxMode   string
}

// FormatEnvironment renders immutable runtime environment metadata for
// inclusion in the system prompt. When all identifying fields are empty it
// returns "".
func FormatEnvironment(info EnvironmentInfo) string {
	osName := strings.TrimSpace(info.OS)
	arch := strings.TrimSpace(info.Arch)
	ws := strings.TrimSpace(info.WorkspaceRoot)
	wsMode := strings.TrimSpace(info.WorkspaceMode)
	mode := strings.TrimSpace(info.SandboxMode)
	if osName == "" && arch == "" && ws == "" && wsMode == "" && mode == "" {
		return ""
	}
	var b strings.Builder
	switch {
	case osName != "" && arch != "":
		fmt.Fprintf(&b, "- Host OS/arch: %s/%s\n", osName, arch)
	case osName != "":
		fmt.Fprintf(&b, "- Host OS: %s\n", osName)
	case arch != "":
		fmt.Fprintf(&b, "- Host arch: %s\n", arch)
	}
	if ws != "" {
		fmt.Fprintf(&b, "- Workspace root: %s\n", ws)
	}
	if wsMode != "" {
		fmt.Fprintf(&b, "- Workspace mode: %s\n", wsMode)
	}
	if mode != "" {
		fmt.Fprintf(&b, "- Sandbox mode: %s\n", mode)
	}
	if strings.EqualFold(osName, "windows") {
		b.WriteString("- Windows / WSL boundary: file tools (list_files, read_file, write_file, edit_file, search_files, find_files, search_code, git, pwd) run in the Windows Forcefield process using Windows host paths, while shell and shell_job execute GNU Bash inside WSL (where shell cwd is still validated as a Windows host path before entering WSL).\n")
	}
	b.WriteString("- Filesystem tools path usage: for filesystem tools, omit `path` or use `\".\"` to target the workspace root.\n")
	b.WriteString("- Shell path usage (permissive native mode): omitting `cwd` uses the process working directory, and `\".\"` resolves relative to the process working directory (not necessarily the workspace root); to target the workspace root in permissive native mode, pass the actual `Workspace root` host path shown above.\n")
	b.WriteString("- Shell path usage (confined mode — `workspace.mode: strict`, `sandbox.mode: wsl`, or `sandbox.mode: isolated`): omitting `cwd` or using `\".\"` targets the workspace root.\n")
	b.WriteString("- Never invent placeholder paths such as `\"/home/agent/repo\"` or `\"root\"`.\n")
	return strings.TrimSpace(b.String())
}

// Agent holds everything needed to build a system prompt for a single run.
type Agent struct {
	Name          string
	SystemPrompt  string
	Constraints   []string
	RuntimeEnv    string
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

// WithConstraints sets the behavioral boundaries rendered as a prompt
// section. These are guidance, never enforcement.
func (a *Agent) WithConstraints(constraints []string) *Agent {
	a.Constraints = append([]string(nil), constraints...)
	return a
}

// WithRuntimeEnv sets immutable runtime environment metadata rendered into
// the system prompt, and returns the agent for chaining.
func (a *Agent) WithRuntimeEnv(formatted string) *Agent {
	a.RuntimeEnv = strings.TrimSpace(formatted)
	return a
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

	if len(a.Constraints) > 0 {
		b.WriteString("\n\n## Boundaries\n\nThe following rules guide (but do not replace) the harness enforcement:\n\n")
		for _, c := range a.Constraints {
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(c))
			b.WriteString("\n")
		}
	}

	if a.RuntimeEnv != "" {
		b.WriteString("\n\n## Runtime Environment\n\n")
		b.WriteString(a.RuntimeEnv)
	}

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
