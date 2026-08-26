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

// Capabilities states explicitly what a provider (and the Forcefield
// adapter in front of it) supports. The runtime and TUI read these
// instead of asking "if provider == ollama" anywhere.
//
// Vision is reported false by every current adapter on purpose: no
// Forcefield message can carry image content yet, so claiming vision
// support would overpromise regardless of what the remote API accepts.
type Capabilities struct {
	Streaming         bool
	ToolCalling       bool
	Vision            bool
	StructuredOutput  bool // JSON/structured response format
	Reasoning         bool // separate reasoning/thinking stream
	ParallelToolCalls bool
	// ContextWindow is the model context size in tokens; 0 means unknown.
	ContextWindow int
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

// ModelLister is implemented by providers whose API can enumerate the
// models it serves. Model discovery is optional: providers without it are
// driven entirely by configured or catalog-known models.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}
