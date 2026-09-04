package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

func responsesSpec(url, model string) Spec {
	return Spec{ID: "zen", Type: "openai-responses", Label: "OpenCode Zen", BaseURL: url, Model: model, APIKey: "test-key"}
}

func writeResponsesSSE(w http.ResponseWriter, payloads ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, p := range payloads {
		w.Write([]byte("data: " + p + "\n\n"))
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func TestResponsesStreamsText(t *testing.T) {
	var gotPath, gotAuth, gotUA string
	var gotBody map[string]any
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeResponsesSSE(w,
			`{"type":"response.output_text.delta","delta":"Hello"}`,
			`{"type":"response.output_text.delta","delta":" world"}`,
			`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`,
		)
	})

	p := NewOpenAIResponses(responsesSpec(url, "gpt-5.5"))
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	events := collectStream(t, stream)

	if gotPath != "/responses" {
		t.Errorf("path = %q, want /responses", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if gotUA == "" || strings.HasPrefix(gotUA, "Go-http") {
		t.Errorf("User-Agent = %q, want an identifying client value", gotUA)
	}
	if gotBody["model"] != "gpt-5.5" {
		t.Errorf("model = %v", gotBody["model"])
	}
	if store, _ := gotBody["store"].(bool); store {
		t.Errorf("store = true, want stateless false")
	}

	var text string
	var done bool
	var usage *Usage
	for _, ev := range events {
		if ev.Err != nil {
			t.Fatalf("stream error: %v", ev.Err)
		}
		text += ev.Text
		if ev.Done {
			done = true
			usage = ev.Usage
		}
	}
	if text != "Hello world" {
		t.Errorf("text = %q", text)
	}
	if !done {
		t.Error("missing Done event")
	}
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 5 || usage.TotalTokens != 15 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestResponsesToolCallRoundTrip(t *testing.T) {
	var gotBody map[string]any
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeResponsesSSE(w,
			`{"type":"response.output_item.added","item":{"id":"item-1","type":"function_call","call_id":"call-abc","name":"read_file"}}`,
			`{"type":"response.function_call_arguments.delta","item_id":"item-1","delta":"{\"path\":"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"item-1","delta":"\"go.mod\"}"}`,
			`{"type":"response.output_item.done","item":{"id":"item-1","type":"function_call","call_id":"call-abc","name":"read_file","arguments":"{\"path\":\"go.mod\"}"}}`,
			`{"type":"response.completed","response":{"status":"completed"}}`,
		)
	})

	defs := []tools.Definition{{Name: "read_file", Description: "read", InputSchema: map[string]any{"type": "object"}}}
	p := NewOpenAIResponses(responsesSpec(url, "gpt-5.5"))
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "read"}}, defs)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	events := collectStream(t, stream)

	// Tools sent in Responses function shape.
	toolsSent, _ := gotBody["tools"].([]any)
	if len(toolsSent) != 1 {
		t.Fatalf("tools sent = %v", gotBody["tools"])
	}
	if toolsSent[0].(map[string]any)["type"] != "function" {
		t.Errorf("tool type = %v, want function", toolsSent[0])
	}

	var calls []ToolCall
	var stop FinishReason
	for _, ev := range events {
		if ev.Err != nil {
			t.Fatalf("stream error: %v", ev.Err)
		}
		calls = append(calls, ev.ToolCalls...)
		if ev.Done {
			stop = ev.StopReason
		}
	}
	if len(calls) != 1 {
		t.Fatalf("tool calls = %+v, want exactly 1", calls)
	}
	if calls[0].ID != "call-abc" || calls[0].Name != "read_file" {
		t.Errorf("call = %+v, want API call_id preserved", calls[0])
	}
	if path, _ := calls[0].Arguments["path"].(string); path != "go.mod" {
		t.Errorf("args = %v", calls[0].Arguments)
	}
	if stop != FinishToolCalls {
		t.Errorf("stop = %q, want tool_calls", stop)
	}
}

func TestResponsesMultipleParallelToolCalls(t *testing.T) {
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeResponsesSSE(w,
			`{"type":"response.output_item.added","item":{"id":"item-1","type":"function_call","call_id":"call-1","name":"read_file"}}`,
			`{"type":"response.output_item.added","item":{"id":"item-2","type":"function_call","call_id":"call-2","name":"pwd"}}`,
			`{"type":"response.function_call_arguments.delta","item_id":"item-1","delta":"{\"path\":\"a\"}"}`,
			`{"type":"response.function_call_arguments.delta","item_id":"item-2","delta":"{}"}`,
			`{"type":"response.output_item.done","item":{"id":"item-1","type":"function_call","call_id":"call-1","name":"read_file","arguments":"{\"path\":\"a\"}"}}`,
			`{"type":"response.output_item.done","item":{"id":"item-2","type":"function_call","call_id":"call-2","name":"pwd","arguments":"{}"}}`,
			`{"type":"response.completed","response":{"status":"completed"}}`,
		)
	})

	p := NewOpenAIResponses(responsesSpec(url, "gpt-5.5"))
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "x"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var calls []ToolCall
	for _, ev := range collectStream(t, stream) {
		if ev.Err != nil {
			t.Fatalf("stream error: %v", ev.Err)
		}
		calls = append(calls, ev.ToolCalls...)
	}
	if len(calls) != 2 || calls[0].ID != "call-1" || calls[1].ID != "call-2" {
		t.Fatalf("calls = %+v, want both in order", calls)
	}
}

func TestResponsesReasoningDeltas(t *testing.T) {
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeResponsesSSE(w,
			`{"type":"response.reasoning_summary_text.delta","delta":"Considering options"}`,
			`{"type":"response.output_text.delta","delta":"Answer"}`,
			`{"type":"response.completed","response":{"status":"completed"}}`,
		)
	})

	p := NewOpenAIResponses(responsesSpec(url, "gpt-5.5"))
	p.SetReasoning(ReasoningConfig{Effort: "medium"})
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "x"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var thinking, text string
	for _, ev := range collectStream(t, stream) {
		if ev.Err != nil {
			t.Fatalf("stream error: %v", ev.Err)
		}
		thinking += ev.Thinking
		text += ev.Text
	}
	if thinking != "Considering options" {
		t.Errorf("thinking = %q", thinking)
	}
	if text != "Answer" {
		t.Errorf("text = %q, want reasoning kept out of text", text)
	}
}

func TestResponsesEffortSentInRequest(t *testing.T) {
	var gotBody map[string]any
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeResponsesSSE(w, `{"type":"response.completed","response":{"status":"completed"}}`)
	})

	p := NewOpenAIResponses(responsesSpec(url, "gpt-5.5"))
	p.SetReasoning(ReasoningConfig{Effort: "high"})
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "x"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	collectStream(t, stream)
	reasoning, _ := gotBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" || reasoning["summary"] != "auto" {
		t.Errorf("reasoning = %v, want effort+summary", gotBody["reasoning"])
	}
}

func TestResponsesReplaysHistoryWithAPICallIDs(t *testing.T) {
	var gotBody map[string]any
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeResponsesSSE(w, `{"type":"response.completed","response":{"status":"completed"}}`)
	})

	msgs := []Message{
		{Role: UserRole, Content: "read it"},
		{Role: AssistantRole, Content: "on it", ToolCalls: []ToolCall{{ID: "call-abc", Name: "read_file", Arguments: map[string]any{"path": "go.mod"}}}},
		{Role: ToolRole, ToolCallID: "call-abc", Name: "read_file", Content: "module x"},
	}
	p := NewOpenAIResponses(responsesSpec(url, "gpt-5.5"))
	stream, err := p.StreamChat(context.Background(), msgs, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	collectStream(t, stream)

	input, _ := gotBody["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("input items = %v", gotBody["input"])
	}
	fnCall, _ := input[2].(map[string]any)
	if fnCall["type"] != "function_call" || fnCall["call_id"] != "call-abc" {
		t.Errorf("assistant call replay = %v", input[2])
	}
	fnOut, _ := input[3].(map[string]any)
	if fnOut["type"] != "function_call_output" || fnOut["call_id"] != "call-abc" || fnOut["output"] != "module x" {
		t.Errorf("tool result replay = %v", input[3])
	}
}

func TestResponsesMalformedChunkFailsCleanly(t *testing.T) {
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeResponsesSSE(w,
			`{"type":"response.output_text.delta","delta":"partial"}`,
			`this is not json`,
		)
	})

	p := NewOpenAIResponses(responsesSpec(url, "gpt-5.5"))
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "x"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var text string
	var sawErr bool
	for _, ev := range collectStream(t, stream) {
		text += ev.Text
		if ev.Err != nil {
			sawErr = true
			if Classify(ev.Err) != ErrKindProtocol {
				t.Errorf("kind = %v, want protocol", Classify(ev.Err))
			}
		}
	}
	if text != "partial" {
		t.Errorf("text before error = %q", text)
	}
	if !sawErr {
		t.Error("malformed chunk produced no error")
	}
}

func TestResponsesHTTPErrorSurfacesHint(t *testing.T) {
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	})

	p := NewOpenAIResponses(responsesSpec(url, "gpt-5.5"))
	p.authHintEnv = "OPENCODE_API_KEY"
	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "x"}}, nil)
	if err == nil {
		t.Fatal("want error for 401")
	}
	if Classify(err) != ErrKindAuth {
		t.Errorf("kind = %v, want auth", Classify(err))
	}
	if !strings.Contains(err.Error(), "OPENCODE_API_KEY") {
		t.Errorf("error lacks key hint: %v", err)
	}
}

func TestResponsesRedactsKeyFromErrors(t *testing.T) {
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"key test-key is bad"}}`))
	})

	p := NewOpenAIResponses(responsesSpec(url, "gpt-5.5"))
	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "x"}}, nil)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Errorf("API key leaked in error: %v", err)
	}
}

func TestResponsesMissingKeyHint(t *testing.T) {
	spec := responsesSpec("http://localhost:9", "gpt-5.5")
	spec.APIKey = ""
	p := NewOpenAIResponses(spec)
	p.authHintEnv = "OPENCODE_API_KEY"
	hint := p.statusHint(401, "")
	if !strings.Contains(hint, "OPENCODE_API_KEY") {
		t.Errorf("hint = %q", hint)
	}
}

func TestResponsesFailedResponseErrors(t *testing.T) {
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeResponsesSSE(w, `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"}}}`)
	})

	p := NewOpenAIResponses(responsesSpec(url, "gpt-5.5"))
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "x"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var sawErr bool
	for _, ev := range collectStream(t, stream) {
		if ev.Err != nil && strings.Contains(ev.Err.Error(), "boom") {
			sawErr = true
		}
	}
	if !sawErr {
		t.Error("failed response produced no error")
	}
}

func TestResponsesIncompleteLengthMaps(t *testing.T) {
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeResponsesSSE(w, `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`)
	})

	p := NewOpenAIResponses(responsesSpec(url, "gpt-5.5"))
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "x"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var stop FinishReason
	var done bool
	for _, ev := range collectStream(t, stream) {
		if ev.Err != nil {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Done {
			done = true
			stop = ev.StopReason
		}
	}
	if !done || stop != FinishLength {
		t.Errorf("done=%v stop=%q, want length", done, stop)
	}
}

func TestResponsesListModels(t *testing.T) {
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Write([]byte(`{"data":[{"id":"gpt-5.5"},{"id":""}]}`))
	})

	p := NewOpenAIResponses(responsesSpec(url, "gpt-5.5"))
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.5" {
		t.Errorf("models = %+v", models)
	}
}

func TestResponsesCapabilities(t *testing.T) {
	caps := NewOpenAIResponses(responsesSpec("http://localhost:9", "m")).Capabilities()
	if !caps.Streaming || !caps.ToolCalling || !caps.Reasoning || !caps.ParallelToolCalls {
		t.Errorf("caps = %+v", caps)
	}
	if caps.Vision || caps.StructuredOutput {
		t.Errorf("must not overpromise: %+v", caps)
	}
}
