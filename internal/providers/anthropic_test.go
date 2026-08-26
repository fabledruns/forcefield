package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

func anthropicServer(t *testing.T, handle func(t *testing.T, w http.ResponseWriter, r *http.Request)) *AnthropicProvider {
	t.Helper()
	server := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		handle(t, w, r)
	})
	return NewAnthropicProvider(Spec{
		ID:      "anthropic",
		Type:    "anthropic",
		BaseURL: server,
		Model:   "claude-test",
	})
}

func TestAnthropicRequestShape(t *testing.T) {
	var gotPath, gotKey, gotVersion string
	var gotBody map[string]any
	p := anthropicServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeSSE(w, `{"type":"message_stop"}`)
	})
	p.spec.APIKey = "ak-test"

	stream, err := p.StreamChat(context.Background(),
		[]Message{
			{Role: SystemRole, Content: "be helpful"},
			{Role: UserRole, Content: "hi"},
		},
		[]tools.Definition{{Name: "echo", Description: "Echo.", InputSchema: map[string]any{"type": "object"}}},
	)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)

	if gotPath != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", gotPath)
	}
	if gotKey != "ak-test" {
		t.Errorf("x-api-key = %q", gotKey)
	}
	if gotVersion != anthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", gotVersion, anthropicVersion)
	}
	if gotBody["system"] != "be helpful" {
		t.Errorf("system = %v, want top-level system field", gotBody["system"])
	}
	if _, ok := gotBody["max_tokens"]; !ok {
		t.Error("max_tokens missing; the Messages API requires it")
	}
	if gotBody["stream"] != true {
		t.Errorf("stream = %v, want true", gotBody["stream"])
	}
	msgs := gotBody["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["role"] != "user" {
		t.Errorf("first role = %v, want user (system must not appear as a message)", first["role"])
	}
	blocks := first["content"].([]any)
	if blocks[0].(map[string]any)["type"] != "text" {
		t.Errorf("first block = %v, want text", blocks[0])
	}
	toolsRaw := gotBody["tools"].([]any)
	toolDef := toolsRaw[0].(map[string]any)
	if toolDef["name"] != "echo" || toolDef["input_schema"] == nil {
		t.Errorf("tool def = %#v, want name + input_schema", toolDef)
	}
}

func TestAnthropicToolCallRoundTrip(t *testing.T) {
	var gotMessages []anthropicMessage
	p := anthropicServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		var body anthropicRequest
		json.NewDecoder(r.Body).Decode(&body)
		gotMessages = body.Messages
		writeSSE(w, `{"type":"message_stop"}`)
	})

	history := []Message{
		{Role: UserRole, Content: "list files"},
		{Role: AssistantRole, ToolCalls: []ToolCall{
			{ID: "toolu-1", Name: "list", Arguments: map[string]any{"path": "."}},
			{ID: "toolu-2", Name: "read", Arguments: map[string]any{"file": "a.txt"}},
		}},
		{Role: ToolRole, Name: "list", ToolCallID: "toolu-1", Content: "a.txt\nb.txt"},
		{Role: ToolRole, Name: "read", ToolCallID: "toolu-2", Content: "contents"},
	}

	stream, err := p.StreamChat(context.Background(), history, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)

	if len(gotMessages) != 3 {
		t.Fatalf("messages = %d (%+v), want user + assistant + merged tool results", len(gotMessages), gotMessages)
	}
	asst := gotMessages[1]
	if asst.Role != "assistant" || len(asst.Content) != 2 {
		t.Fatalf("assistant message = %+v, want two tool_use blocks", asst)
	}
	if asst.Content[0].Type != "tool_use" || asst.Content[0].ID != "toolu-1" || asst.Content[0].Name != "list" {
		t.Errorf("first block = %+v, want tool_use toolu-1 list", asst.Content[0])
	}
	if asst.Content[0].Input["path"] != "." {
		t.Errorf("input = %#v, want path=.", asst.Content[0].Input)
	}

	results := gotMessages[2]
	if results.Role != "user" {
		t.Fatalf("results role = %q, want user", results.Role)
	}
	if len(results.Content) != 2 {
		t.Fatalf("consecutive tool results were not merged: %+v", results)
	}
	if results.Content[0].Type != "tool_result" || results.Content[0].ToolUseID != "toolu-1" {
		t.Errorf("result[0] = %+v, want tool_result for toolu-1", results.Content[0])
	}
	if results.Content[1].Content != "contents" {
		t.Errorf("result[1].Content = %q", results.Content[1].Content)
	}
}

func TestAnthropicStreamFullTurn(t *testing.T) {
	p := anthropicServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		writeSSE(w,
			`{"type":"message_start","message":{"usage":{"input_tokens":11}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"ping"}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"pondering"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu-9","name":"echo"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"val"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"ue\":42}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
			`{"type":"message_stop"}`,
		)
	})

	events := drainEvents(t, mustStream(t, p))

	text := ""
	thinking := ""
	for _, e := range events {
		if e.Err != nil {
			t.Fatalf("stream error: %v", e.Err)
		}
		text += e.Text
		thinking += e.Thinking
	}
	if text != "Hello" {
		t.Errorf("text = %q, want Hello", text)
	}
	if thinking != "pondering" {
		t.Errorf("thinking = %q, want pondering", thinking)
	}

	last := events[len(events)-1]
	if !last.Done {
		t.Fatal("final event not Done")
	}
	if last.StopReason != FinishToolCalls {
		t.Errorf("stop reason = %q, want tool_calls", last.StopReason)
	}
	if last.Usage == nil || last.Usage.PromptTokens != 11 || last.Usage.CompletionTokens != 7 {
		t.Errorf("usage = %#v, want prompt 11 completion 7", last.Usage)
	}

	var callsEvent *StreamEvent
	for i := range events {
		if len(events[i].ToolCalls) > 0 {
			callsEvent = &events[i]
		}
	}
	if callsEvent == nil || len(callsEvent.ToolCalls) != 1 {
		t.Fatalf("tool call events = %+v, want one batched event with one call", callsEvent)
	}
	call := callsEvent.ToolCalls[0]
	if call.ID != "toolu-9" || call.Name != "echo" || call.Arguments["value"] != float64(42) {
		t.Errorf("call = %#v, want reassembled echo(42)", call)
	}
}

func TestAnthropicStreamErrorEventSurfacesMessage(t *testing.T) {
	p := anthropicServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		writeSSE(w,
			`{"type":"message_start"}`,
			`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
		)
	})

	events := drainEvents(t, mustStream(t, p))
	if len(events) == 0 || events[len(events)-1].Err == nil {
		t.Fatalf("events = %#v, want an error event", events)
	}
	if msg := events[len(events)-1].Err.Error(); !strings.Contains(msg, "Overloaded") {
		t.Errorf("error = %q, want the provider's message", msg)
	}
}

func TestAnthropicMalformedEventIsProtocolError(t *testing.T) {
	p := anthropicServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		writeSSE(w, `data: {"type":`)
	})

	events := drainEvents(t, mustStream(t, p))
	if len(events) == 0 || events[len(events)-1].Err == nil {
		t.Fatalf("events = %#v, want an error event", events)
	}
	if got := Classify(events[len(events)-1].Err); got != ErrKindProtocol {
		t.Errorf("kind = %v, want ErrKindProtocol", got)
	}
}

func TestAnthropicAuthFailureNamesEnvVar(t *testing.T) {
	server := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	})

	p := NewAnthropicProvider(Spec{ID: "anthropic", Type: "anthropic", BaseURL: server, Model: "m"})
	p.retry = fastRetry

	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want auth error")
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("error = %q, want a hint naming ANTHROPIC_API_KEY", err)
	}
	if Classify(err) != ErrKindAuth {
		t.Errorf("kind = %v, want ErrKindAuth", Classify(err))
	}
}

func TestAnthropicAPIKeyRedactedFromErrors(t *testing.T) {
	const key = "sk-ant-secret"
	server := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"message":"key %s is not allowed"}`, key)
	})

	p := NewAnthropicProvider(Spec{ID: "anthropic", Type: "anthropic", BaseURL: server, Model: "m", APIKey: key})
	p.retry = fastRetry

	_, err := p.StreamChat(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want error")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("error leaked the key: %q", err.Error())
	}
}

func TestAnthropicCompleteAndListModels(t *testing.T) {
	p := anthropicServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages":
			fmt.Fprint(w, `{
				"content":[
					{"type":"text","text":"partial "},
					{"type":"tool_use","id":"t1","name":"run","input":{"cmd":"ls"}}
				],
				"stop_reason":"tool_use",
				"usage":{"input_tokens":4,"output_tokens":6}
			}`)
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"claude-x","display_name":"Claude X"},{"id":"claude-y"}]}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})

	resp, err := p.Complete(context.Background(), []Message{{Role: UserRole, Content: "go"}}, nil)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if resp.Content != "partial " || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Arguments["cmd"] != "ls" {
		t.Errorf("response = %#v, want text + parsed tool call", resp)
	}
	if resp.Usage.TotalTokens != 10 || resp.StopReason != FinishToolCalls {
		t.Errorf("usage=%#v stop=%q, want total 10 / tool_calls", resp.Usage, resp.StopReason)
	}

	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 2 || models[0].Name != "Claude X" || models[1].Name != "claude-y" {
		t.Errorf("models = %#v, want display names with fallback", models)
	}
}

func TestAnthropicCapabilities(t *testing.T) {
	caps := CapabilitiesFor("anthropic")
	if !caps.Streaming || !caps.ToolCalling || !caps.Reasoning || !caps.ParallelToolCalls {
		t.Errorf("capabilities = %+v, want streaming/tools/reasoning/parallel", caps)
	}
}
