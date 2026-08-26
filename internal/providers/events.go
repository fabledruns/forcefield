package providers

// Usage carries token accounting for one model turn, when the provider
// reports it. Zero values mean "not reported".
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// FinishReason is the normalized reason a model turn ended. Providers map
// their native spellings onto these; empty means the provider never said.
type FinishReason string

const (
	FinishNone      FinishReason = ""
	FinishStop      FinishReason = "stop"
	FinishToolCalls FinishReason = "tool_calls"
	FinishLength    FinishReason = "length"
)

// StreamEvent is one piece of a model response. A provider emits these events
// for a single model turn; the runtime is responsible for turning tool calls
// into tool executions and starting subsequent model turns.
type StreamEvent struct {
	Text      string
	Thinking  string
	ToolCalls []ToolCall
	Done      bool
	Err       error

	// StopReason is set on the final event when the provider reported why
	// the turn ended (stop, tool_calls, length).
	StopReason FinishReason
	// Usage is set when the provider reported token counts for the turn.
	Usage *Usage
}
