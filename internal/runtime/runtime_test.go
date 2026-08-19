package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
		provider:  p,
		agent:     agent.New("test", "system", ""),
		manager:   manager,
		scheduler: newScheduler(manager, nil, nil, DefaultSchedulerConfig),
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

	events, err := runtime.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "use a tool"}})
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

	response, err := runtime.Run([]providers.Message{{Role: providers.UserRole, Content: "use a tool"}})
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

// TestRunSurvivesRateLimitedTurn is the regression test for runs that used
// to abort the whole agent loop on the provider's first 429: a real
// NvidiaProvider (over an httptest server) gets rate-limited on the first
// request, then serves a tool-call turn and a final turn. The run must
// recover via the provider's retry layer and complete instead of failing
// with EventError.
func TestRunSurvivesRateLimitedTurn(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch requests.Add(1) {
		case 1:
			// Retry-After: 0 makes the default retry policy retry
			// immediately, so the test needs no timing hooks.
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate limit exceeded, please retry"}}`)
		case 2:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"function\":{\"name\":\"echo\",\"arguments\":\"{\\\"value\\\":\\\"x\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"all done\"},\"finish_reason\":\"stop\"}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))
	defer server.Close()

	rt := newTestRuntime(providers.NewNvidiaProvider(server.URL, "test-model", "", nil))

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "use a tool"}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	var done bool
	for event := range events {
		if event.Type == EventError {
			t.Fatalf("run aborted on rate limit: %v", event.Err)
		}
		if event.Type == EventDone {
			done = true
			if event.Response == nil || event.Response.Content != "all done" {
				t.Fatalf("final response = %#v, want all done", event.Response)
			}
		}
	}
	if !done {
		t.Fatal("run finished without EventDone")
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3 (rate-limited turn, tool turn, final turn)", got)
	}
}
