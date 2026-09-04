package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/tools"
)

// echoArgsTool is a real executable tool so the scheduler path is genuine.
type echoArgsTool struct{}

func (echoArgsTool) Name() string        { return "echo_args" }
func (echoArgsTool) Description() string { return "echoes its input" }
func (echoArgsTool) InputSchema() map[string]any {
	return map[string]any{"type": "object"}
}
func (echoArgsTool) Execute(_ context.Context, args map[string]any) (tools.Result, error) {
	return tools.Result{Content: fmt.Sprintf("echo:%v", args["value"])}, nil
}

// TestAgentLoopOverResponsesTransport runs the full agent loop against a
// Responses-protocol model: tool definitions are received, a tool call is
// emitted and executed, its result returns, and the turn continues to a
// final answer.
func TestAgentLoopOverResponsesTransport(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"item-1\",\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"echo_args\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"item-1\",\"delta\":\"{\\\"value\\\":\\\"hi\\\"}\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"item-1\",\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"echo_args\",\"arguments\":\"{\\\"value\\\":\\\"hi\\\"}\"}}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"the tool said hi\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	}))
	defer srv.Close()

	provider := providers.NewOpenAIResponses(providers.Spec{
		ID: "opencode-zen", Type: "openai-responses", Label: "OpenCode Zen",
		BaseURL: srv.URL, Model: "gpt-5.5",
	})
	rt := newTestRuntimeWithLimits(provider, DefaultLimits, echoArgsTool{})

	events, err := rt.StreamChat(context.Background(), []providers.Message{{Role: providers.UserRole, Content: "say hi via tool"}})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var final string
	var toolRan bool
	for e := range events {
		if e.Type == EventToolFinish && e.ToolResult != nil && e.ToolResult.Name == "echo_args" {
			toolRan = true
			if !strings.Contains(e.ToolResult.Content, "echo:hi") {
				t.Errorf("tool result = %q", e.ToolResult.Content)
			}
		}
		if e.Type == EventDone && e.Response != nil {
			final = e.Response.Content
		}
		if e.Type == EventError {
			t.Fatalf("run error: %v", e.Err)
		}
	}
	if !toolRan {
		t.Error("echo_args never executed")
	}
	if final != "the tool said hi" {
		t.Errorf("final = %q", final)
	}
	if calls.Load() != 2 {
		t.Errorf("model turns = %d, want 2 (tool turn + final turn)", calls.Load())
	}
}
