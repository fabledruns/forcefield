// Package providers defines the runtime-facing model provider interfaces.
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
