package providers

type Role string

const (
	SystemRole    Role = "system"
	UserRole      Role = "user"
	AssistantRole Role = "assistant"
	ToolRole      Role = "tool"
)

type Message struct {
	Role    Role
	Content string

	ToolCalls []ToolCall

	ToolCallID string
	Name       string
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

type Response struct {
	Content   string
	ToolCalls []ToolCall

	// StopReason is why the turn ended (stop, tool_calls, length), when
	// the provider reported it.
	StopReason FinishReason
	// Usage holds token counts for the turn, when reported.
	Usage Usage
}
