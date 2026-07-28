package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"forcefield/internal/tools"
)

func TestOllamaStreamChatEmitsToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("path = %q, want /api/chat", r.URL.Path)
		}

		var request ollamaChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !request.Stream {
			t.Error("request did not enable streaming")
		}
		if len(request.Tools) != 1 || request.Tools[0].Function.Name != "echo" {
			t.Errorf("tools = %#v, want echo definition", request.Tools)
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		if err := json.NewEncoder(w).Encode(ollamaStreamResponse{
			Message: struct {
				Content   string           `json:"content"`
				Thinking  string           `json:"thinking"`
				ToolCalls []ollamaToolCall `json:"tool_calls"`
			}{Content: "Looking that up. "},
		}); err != nil {
			t.Fatalf("write text chunk: %v", err)
		}
		if err := json.NewEncoder(w).Encode(ollamaStreamResponse{
			Message: struct {
				Content   string           `json:"content"`
				Thinking  string           `json:"thinking"`
				ToolCalls []ollamaToolCall `json:"tool_calls"`
			}{ToolCalls: []ollamaToolCall{{
				ID: "call-1",
				Function: ollamaFunctionCall{
					Name:      "echo",
					Arguments: map[string]any{"value": "hello"},
				},
			}}},
			Done: true,
		}); err != nil {
			t.Fatalf("write tool-call chunk: %v", err)
		}
	}))
	defer server.Close()

	provider := NewOllamaProvider(server.URL, "test-model")
	events, err := provider.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "test"}}, []tools.Definition{{
		Name:        "echo",
		Description: "Echoes a value.",
		InputSchema: map[string]any{"type": "object"},
	}})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	var received []StreamEvent
	for event := range events {
		if event.Err != nil {
			t.Fatalf("stream event error = %v", event.Err)
		}
		received = append(received, event)
	}

	if len(received) != 3 {
		t.Fatalf("received %d events, want text, tool call, done", len(received))
	}
	if received[0].Text != "Looking that up. " {
		t.Errorf("text = %q", received[0].Text)
	}
	if calls := received[1].ToolCalls; len(calls) != 1 || calls[0].ID != "call-1" || calls[0].Name != "echo" || calls[0].Arguments["value"] != "hello" {
		t.Errorf("tool calls = %#v, want parsed echo call", calls)
	}
	if !received[2].Done {
		t.Error("final event was not marked done")
	}
}

func TestToOllamaMessagesPreservesToolCallID(t *testing.T) {
	messages := toOllamaMessages([]Message{{
		Role: AssistantRole,
		ToolCalls: []ToolCall{{
			ID:        "call-1",
			Name:      "echo",
			Arguments: map[string]any{"value": "hello"},
		}},
	}})

	if got := messages[0].ToolCalls[0].ID; got != "call-1" {
		t.Errorf("tool call ID = %q, want call-1", got)
	}
}
