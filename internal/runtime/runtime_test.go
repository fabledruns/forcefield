package runtime

import (
	"context"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

type scriptedProvider struct {
	turns    [][]providers.StreamEvent
	calls    int
	messages [][]providers.Message
}

func (p *scriptedProvider) StreamChat(_ context.Context, messages []providers.Message, _ []tools.Definition) (<-chan providers.StreamEvent, error) {
	p.messages = append(p.messages, append([]providers.Message(nil), messages...))

	turn := p.turns[p.calls]
	p.calls++

	events := make(chan providers.StreamEvent, len(turn))
	for _, event := range turn {
		events <- event
	}
	close(events)
	return events, nil
}

type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "Returns the supplied value." }
func (echoTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (echoTool) Execute(_ context.Context, args map[string]any) (tools.Result, error) {
	return tools.Result{Content: args["value"].(string)}, nil
}

func newTestRuntime(p providers.ModelProvider) *Runtime {
	manager := tools.NewManager(tools.NewRegistry())
	if err := manager.Register(echoTool{}); err != nil {
		panic(err)
	}

	return &Runtime{
		provider: p,
		agent:    agent.New("test", "system", ""),
		manager:  manager,
	}
}

func testTurns() [][]providers.StreamEvent {
	return [][]providers.StreamEvent{
		{
			{Text: "Checking that now. "},
			{ToolCalls: []providers.ToolCall{{
				ID:        "call-1",
				Name:      "echo",
				Arguments: map[string]any{"value": "tool output"},
			}}},
			{Done: true},
		},
		{
			{Text: "The tool said: tool output"},
			{Done: true},
		},
	}
}

func TestStreamChatExecutesToolsAndContinuesGeneration(t *testing.T) {
	provider := &scriptedProvider{turns: testTurns()}
	runtime := newTestRuntime(provider)

	events, err := runtime.StreamChat(context.Background(), "use a tool")
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	var types []EventType
	var final *providers.Response
	var toolResult *ToolResult
	for event := range events {
		types = append(types, event.Type)
		if event.Type == EventDone {
			final = event.Response
		}
		if event.Type == EventToolFinish {
			toolResult = event.ToolResult
		}
	}

	wantTypes := []EventType{
		EventThinking,
		EventText,
		EventToolStart,
		EventToolFinish,
		EventThinking,
		EventText,
		EventDone,
	}
	if len(types) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d (%v)", len(types), len(wantTypes), types)
	}
	for i, want := range wantTypes {
		if types[i] != want {
			t.Errorf("event %d type = %v, want %v", i, types[i], want)
		}
	}

	if final == nil || final.Content != "The tool said: tool output" {
		t.Fatalf("final response = %#v, want final tool-informed response", final)
	}
	if toolResult == nil || !toolResult.Success || toolResult.Arguments["value"] != "tool output" {
		t.Errorf("tool result = %#v, want successful result with tool arguments", toolResult)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls)
	}

	continuedMessages := provider.messages[1]
	if got := continuedMessages[len(continuedMessages)-2]; got.Role != providers.AssistantRole || len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "call-1" {
		t.Errorf("assistant tool-call message = %#v, want tool call with its ID", got)
	}
	if got := continuedMessages[len(continuedMessages)-1]; got.Role != providers.ToolRole || got.ToolCallID != "call-1" || got.Content != "tool output" {
		t.Errorf("tool result message = %#v, want matching tool result", got)
	}
}

func TestRunUsesStreamingAgentLoop(t *testing.T) {
	provider := &scriptedProvider{turns: testTurns()}
	runtime := newTestRuntime(provider)

	response, err := runtime.Run("use a tool")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if response.Content != "The tool said: tool output" {
		t.Errorf("response.Content = %q, want final tool-informed response", response.Content)
	}
	if provider.calls != 2 {
		t.Errorf("provider calls = %d, want 2", provider.calls)
	}
}
