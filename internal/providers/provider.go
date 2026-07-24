// Package providers defines the ModelProvider interface and its
// implementations. In this prototype there is exactly one implementation
// (Ollama), but the interface exists so a second provider can be added
// later without touching agent or runtime code.
package providers

import "context"

// ModelProvider is the minimal contract any model backend must satisfy:
// given a system prompt and a user prompt, return the model's reply as
// plain text.
type ModelProvider interface {
		  Chat(ctx context.Context, system string, prompt string) (string, error)
	StreamChat(ctx context.Context, system string, prompt string) (<-chan StreamEvent, error)
}
