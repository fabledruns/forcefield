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

	ToolCallID string
	Name       string
}

type ToolCall struct {
	ID   string
	Name string
	Args map[string]any
}

type Response struct {
	Content   string
	ToolCalls []ToolCall
}