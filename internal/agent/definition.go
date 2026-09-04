// Package agent — Definition type.
package agent

import (
	"fmt"
	"sort"
	"strings"
)

// Definition describes one specialised agent: what it is and what it may
// do. It is data, not behaviour — the runtime owns how an agent runs.
type Definition struct {
	// Name is the unique lowercase identifier, e.g. "coding".
	Name string
	// Description is a one-line human summary for listings.
	Description string
	// SystemPrompt is the domain-specific identity prepended before the
	// shared operating contract.
	SystemPrompt string
	// Tools is the explicit allow-list of tool names this agent may use.
	// Empty means "no tools" (not "all tools").
	Tools []string
	// Skills is the explicit allow-list of skill IDs shaping this
	// agent's skill catalog. Honored only when AllSkills is false.
	// An empty (non-nil) list means "no skills". IDs absent from the
	// skill store degrade gracefully (warning + omission, never fatal).
	Skills []string
	// AllSkills exposes the full skill catalog to this agent (the
	// general agent). When true, Skills must be empty. There is
	// deliberately no nil-means-all semantic: the flag is explicit.
	AllSkills bool
	// Constraints are short behavioral prohibitions rendered as a
	// "Boundaries" prompt section (e.g. "never write exploit code").
	// They are guidance, NEVER a security boundary: enforcement stays
	// in the tool allow-list, permissions, and the scheduler.
	Constraints []string
	// Provider is an optional hint for the model provider to use with
	// this agent (e.g. "openai"). Empty means "keep current provider".
	Provider string
	// Model is an optional hint for the model to use (e.g. "gpt-4o").
	// Only meaningful when Provider is set or matches the active provider.
	Model string
}

// AgentOverride is the configuration-level override for a single agent.
// Scalar fields: only non-empty values replace the built-in. List
// fields (Tools, Skills, Constraints): nil means "keep built-in",
// non-nil replaces (verified: yaml.v3 decodes an explicitly empty
// `skills: []` as non-nil, so `skills: []` means "no skills").
// There is intentionally no way to express "all skills" from config in
// v1; the general agent keeps all skills by omitting the field.
type AgentOverride struct {
	Description  string   `yaml:"description,omitempty"`
	SystemPrompt string   `yaml:"system_prompt,omitempty"`
	Tools        []string `yaml:"tools,omitempty"`
	Skills       []string `yaml:"skills,omitempty"`
	Constraints  []string `yaml:"constraints,omitempty"`
	Provider     string   `yaml:"provider,omitempty"`
	Model        string   `yaml:"model,omitempty"`
}

// Clone returns a deep copy of d so callers cannot mutate the registry
// via the returned slice alias. Non-nil empty slices stay non-nil:
// append([]string{}, ...) preserves explicit-set ("none") versus nil
// ("omitted"/"all"), which the config override semantics depend on.
func (d Definition) Clone() Definition {
	out := d
	if d.Tools != nil {
		out.Tools = append([]string{}, d.Tools...)
	}
	if d.Skills != nil {
		out.Skills = append([]string{}, d.Skills...)
	}
	if d.Constraints != nil {
		out.Constraints = append([]string{}, d.Constraints...)
	}
	return out
}

// Validate reports whether d is well-formed. Tool names are not validated
// here against the global tool set; that happens when the filtered manager
// is built so unknown tools surface with the config field name.
func (d Definition) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return fmt.Errorf("agent name cannot be empty")
	}
	if strings.ContainsAny(d.Name, " \t\n/\\") {
		return fmt.Errorf("agent name %q contains invalid characters", d.Name)
	}
	if strings.TrimSpace(d.Description) == "" {
		return fmt.Errorf("agent %q: description cannot be empty", d.Name)
	}
	if strings.TrimSpace(d.SystemPrompt) == "" {
		return fmt.Errorf("agent %q: system_prompt cannot be empty", d.Name)
	}
	if d.Tools == nil {
		return fmt.Errorf("agent %q: tools cannot be nil (use empty slice for no tools)", d.Name)
	}
	seen := make(map[string]struct{}, len(d.Tools))
	for _, t := range d.Tools {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("agent %q: tool name cannot be empty", d.Name)
		}
		if _, ok := seen[t]; ok {
			return fmt.Errorf("agent %q: duplicate tool %q", d.Name, t)
		}
		seen[t] = struct{}{}
	}
	// Keep tools sorted for deterministic Definitions output.
	sort.Strings(d.Tools)
	if d.AllSkills {
		if len(d.Skills) != 0 {
			return fmt.Errorf("agent %q: all_skills is set but skills list is non-empty (explicit flag, no nil semantics)", d.Name)
		}
	} else {
		if d.Skills == nil {
			return fmt.Errorf("agent %q: skills cannot be nil when all_skills is false (use empty slice for no skills)", d.Name)
		}
		seenSkills := make(map[string]struct{}, len(d.Skills))
		for _, s := range d.Skills {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("agent %q: skill id cannot be empty", d.Name)
			}
			if _, ok := seenSkills[s]; ok {
				return fmt.Errorf("agent %q: duplicate skill %q", d.Name, s)
			}
			seenSkills[s] = struct{}{}
		}
		sort.Strings(d.Skills)
	}
	seenConstraints := make(map[string]struct{}, len(d.Constraints))
	for _, c := range d.Constraints {
		if strings.TrimSpace(c) == "" {
			return fmt.Errorf("agent %q: constraint cannot be empty", d.Name)
		}
		if _, ok := seenConstraints[c]; ok {
			return fmt.Errorf("agent %q: duplicate constraint %q", d.Name, c)
		}
		seenConstraints[c] = struct{}{}
	}
	return nil
}

// ApplyOverride returns a copy of d with non-empty fields from o applied.
func (d Definition) ApplyOverride(o AgentOverride) Definition {
	out := d.Clone()
	if strings.TrimSpace(o.Description) != "" {
		out.Description = strings.TrimSpace(o.Description)
	}
	if strings.TrimSpace(o.SystemPrompt) != "" {
		out.SystemPrompt = strings.TrimSpace(o.SystemPrompt)
	}
	if o.Tools != nil {
		out.Tools = append([]string{}, o.Tools...)
		sort.Strings(out.Tools)
	}
	if o.Skills != nil {
		out.Skills = append([]string{}, o.Skills...)
		sort.Strings(out.Skills)
		// An explicit skills list replaces the assignment, including
		// the general agent's all-skills behavior. Non-nil empty means
		// "no skills" (append([]string{}, ...) keeps it non-nil).
		out.AllSkills = false
	}
	if o.Constraints != nil {
		out.Constraints = append([]string{}, o.Constraints...)
	}
	if strings.TrimSpace(o.Provider) != "" {
		out.Provider = strings.TrimSpace(o.Provider)
	}
	if strings.TrimSpace(o.Model) != "" {
		out.Model = strings.TrimSpace(o.Model)
	}
	return out
}
