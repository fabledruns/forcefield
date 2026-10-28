package tools

import "context"

// callContextKey carries the executing tool call's id through a tool
// execution context. Evidence layers (the trace file, the orchestration
// event log) attribute their emissions to one call without parsing
// arguments and without threading ids through every tool signature.
type callContextKey struct{}

// WithToolCall returns a context stamped with one executing call id.
// Call ids are opaque correlation handles, never parsed content.
func WithToolCall(ctx context.Context, callID string) context.Context {
	if callID == "" {
		return ctx
	}
	return context.WithValue(ctx, callContextKey{}, callID)
}

// ToolCallIDFromContext returns the stamped call id, or "".
func ToolCallIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(callContextKey{}).(string)
	return id
}
