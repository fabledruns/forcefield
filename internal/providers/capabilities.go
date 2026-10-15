package providers

import "context"

// AuthRequirement describes whether a provider needs an API key to work.
type AuthRequirement int

const (
	// AuthNone means the service accepts unauthenticated requests (local
	// servers like Ollama or LM Studio).
	AuthNone AuthRequirement = iota
	// AuthOptional means the service works without a key but accepts one.
	AuthOptional
	// AuthRequired means requests fail without a valid API key.
	AuthRequired
)

// String returns a short lowercase label for UI and error use.
func (a AuthRequirement) String() string {
	switch a {
	case AuthNone:
		return "no auth"
	case AuthOptional:
		return "optional key"
	default:
		return "api key required"
	}
}

// Scope describes where a provider's models run, for picker labels like
// "local · tools · streaming".
type Scope string

const (
	ScopeLocal Scope = "local"
	ScopeCloud Scope = "cloud"
)

// Capabilities states what a provider adapter supports; runtime/TUI read
// these instead of branching on provider names. Vision stays false until
// messages can carry images. See docs/Providers.md.
type Capabilities struct {
	Streaming         bool
	ToolCalling       bool
	Vision            bool
	StructuredOutput  bool // JSON/structured response format
	Reasoning         bool // separate reasoning/thinking stream
	ParallelToolCalls bool
	// ContextWindow is the model context size in tokens; 0 means unknown.
	ContextWindow int
	// MaxOutputTokens is the maximum tokens the model can emit in one
	// turn; 0 means unknown. The runtime uses it to reserve space for
	// the next response when the exact limit is not configured.
	MaxOutputTokens int
}

// Detail renders capabilities as the compact descriptor shown under each
// entry in the TUI pickers, e.g. "tools · streaming · reasoning".
func (c Capabilities) Detail() string {
	parts := make([]string, 0, 6)
	if c.ToolCalling {
		parts = append(parts, "tools")
	}
	if c.Streaming {
		parts = append(parts, "streaming")
	}
	if c.Reasoning {
		parts = append(parts, "reasoning")
	}
	if c.Vision {
		parts = append(parts, "vision")
	}
	if c.StructuredOutput {
		parts = append(parts, "json")
	}
	if c.ParallelToolCalls {
		parts = append(parts, "parallel tools")
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " · "
		}
		out += p
	}
	return out
}

// CapabilitiesProvider is implemented by providers that can report what
// they support. All built-in adapters implement it.
type CapabilitiesProvider interface {
	Capabilities() Capabilities
}

// CapabilitiesOverride carries explicit per-provider capability choices
// from configuration. Every field is optional: nil keeps the adapter's
// built-in default, non-nil forces the value. It exists so custom
// OpenAI-compatible endpoints can honestly advertise only what they
// support instead of inheriting every OpenAI feature by default.
type CapabilitiesOverride struct {
	Streaming         *bool `json:"streaming,omitempty" yaml:"streaming,omitempty"`
	ToolCalling       *bool `json:"tool_calling,omitempty" yaml:"tool_calling,omitempty"`
	Vision            *bool `json:"vision,omitempty" yaml:"vision,omitempty"`
	StructuredOutput  *bool `json:"structured_output,omitempty" yaml:"structured_output,omitempty"`
	Reasoning         *bool `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	ParallelToolCalls *bool `json:"parallel_tool_calls,omitempty" yaml:"parallel_tool_calls,omitempty"`
}

// Apply returns base with every non-nil override field applied.
func (o *CapabilitiesOverride) Apply(base Capabilities) Capabilities {
	if o == nil {
		return base
	}
	if o.Streaming != nil {
		base.Streaming = *o.Streaming
	}
	if o.ToolCalling != nil {
		base.ToolCalling = *o.ToolCalling
	}
	if o.Vision != nil {
		base.Vision = *o.Vision
	}
	if o.StructuredOutput != nil {
		base.StructuredOutput = *o.StructuredOutput
	}
	if o.Reasoning != nil {
		base.Reasoning = *o.Reasoning
	}
	if o.ParallelToolCalls != nil {
		base.ParallelToolCalls = *o.ParallelToolCalls
	}
	return base
}

// ModelLister is implemented by providers whose API can enumerate the
// models it serves. Model discovery is optional: providers without it are
// driven entirely by configured or catalog-known models.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}
