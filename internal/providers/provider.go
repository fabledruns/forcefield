// Package providers defines the ModelProvider interface and its
// implementations. In this prototype there is exactly one implementation
// (Ollama), but the interface exists so a second provider can be added
// later without touching agent or runtime code.
package providers

import (
	"context"
	"forcefield/internal/tools"
)

// ModelProvider streams one model turn. The runtime owns the agent loop so
// every provider has one execution primitive regardless of how callers
// consume the final answer.
type ModelProvider interface {
	StreamChat(ctx context.Context, messages []Message, tools []tools.Definition) (<-chan StreamEvent, error)
}
