package providers

// StreamEvent is one piece of a model response. A provider emits these events
// for a single model turn; the runtime is responsible for turning tool calls
// into tool executions and starting subsequent model turns.
type StreamEvent struct {
	Text      string
	Thinking  string
	ToolCalls []ToolCall
	Done      bool
	Err       error
}
