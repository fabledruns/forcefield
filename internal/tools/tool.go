// Package tools defines the core abstractions for Forcefield's tool system.
// It provides the Tool interface, which represents an action that can be invoked by the model, 
// and the Result struct, which contains the output of a tool execution. Additionally, it defines the Definition struct, which 
// describes a tool without exposing its implementation details.
package tools

import "context"

// Tool represents an action the model can invoke.
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]any
	Execute(ctx context.Context, args map[string]any) (Result, error)
}

// Result contains the output of a tool execution.
type Result struct {
	Content string
	IsError bool
}

// Definition describes a tool without exposing its implementation.
type Definition struct {
	Name        string
	Description string
	InputSchema map[string]any
}