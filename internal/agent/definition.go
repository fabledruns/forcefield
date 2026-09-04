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
	// Provider is an optional hint for the model provider to use with
	// this agent (e.g. "openai"). Empty means "keep current provider".
	Provider string
	// Model is an optional hint for the model to use (e.g. "gpt-4o").
	// Only meaningful when Provider is set or matches the active provider.
	Model string
}

// AgentOverride is the configuration-level override for a single agent.
// All fields are optional: only non-empty values replace the built-in.
type AgentOverride struct {
	Description  string   `yaml:"description,omitempty"`
	SystemPrompt string   `yaml:"system_prompt,omitempty"`
	Tools        []string `yaml:"tools,omitempty"`
	Provider     string   `yaml:"provider,omitempty"`
	Model        string   `yaml:"model,omitempty"`
}

// Clone returns a deep copy of d so callers cannot mutate the registry
// via the returned slice alias.
func (d Definition) Clone() Definition {
	out := d
	if len(d.Tools) > 0 {
		out.Tools = append([]string(nil), d.Tools...)
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
		out.Tools = append([]string(nil), o.Tools...)
		sort.Strings(out.Tools)
	}
	if strings.TrimSpace(o.Provider) != "" {
		out.Provider = strings.TrimSpace(o.Provider)
	}
	if strings.TrimSpace(o.Model) != "" {
		out.Model = strings.TrimSpace(o.Model)
	}
	return out
}
