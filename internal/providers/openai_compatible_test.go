package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forcefield/internal/tools"
)

// ocServer spins up an OpenAI-compatible test server. The handler sees
// every request; SSE lines it writes are flushed verbatim.
func ocServer(t *testing.T, handle func(t *testing.T, w http.ResponseWriter, r *http.Request)) (*httptest.Server, *OpenAICompatible) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(t, w, r)
	}))
	t.Cleanup(server.Close)
	p := NewOpenAICompatible(Spec{
		ID:      "test",
		Type:    "openai-compatible",
		Label:   "Test Service",
		BaseURL: server.URL,
		Model:   "test-model",
	})
	return server, p
}

func drainEvents(t *testing.T, events <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var out []StreamEvent
	for event := range events {
		out = append(out, event)
	}
	return out
}

func TestOpenAICompatibleRequestShape(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	var gotBody map[string]any
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	p.spec.APIKey = "sk-test-123"

	stream, err := p.StreamChat(context.Background(),
		[]Message{{Role: SystemRole, Content: "be brief"}, {Role: UserRole, Content: "hi"}},
		[]tools.Definition{{Name: "echo", Description: "Echo.", InputSchema: map[string]any{"type": "object"}}},
	)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)

	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-test-123" {
		t.Errorf("Authorization = %q, want bearer token", gotAuth)
	}
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", gotAccept)
	}
	if gotBody["model"] != "test-model" || gotBody["stream"] != true {
		t.Errorf("model/stream = %v/%v, want test-model/true", gotBody["model"], gotBody["stream"])
	}
	toolsRaw, ok := gotBody["tools"].([]any)
	if !ok || len(toolsRaw) != 1 {
		t.Fatalf("tools = %#v, want one function definition", gotBody["tools"])
	}
	fn := toolsRaw[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "echo" {
		t.Errorf("tool name = %v, want echo", fn["name"])
	}
}

func TestOpenAICompatibleCustomHeadersAndTrailingSlash(t *testing.T) {
	var gotReferer, gotXCustom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("HTTP-Referer")
		gotXCustom = r.Header.Get("X-Custom")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAICompatible(Spec{
		ID:      "custom",
		Type:    "openai-compatible",
		BaseURL: server.URL + "/", // trailing slash must not double
		Model:   "m",
		Headers: map[string]string{
			"HTTP-Referer": "https://example.com",
			"X-Custom":     "yes",
		},
	})

	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)

	if p.spec.BaseURL != strings.TrimRight(server.URL+"/", "/") {
		t.Errorf("base URL = %q, want trailing slash trimmed", p.spec.BaseURL)
	}
	if gotReferer != "https://example.com" || gotXCustom != "yes" {
		t.Errorf("custom headers = %q / %q, want both sent", gotReferer, gotXCustom)
	}
}

func TestOpenAICompatibleStreamMultipleToolCalls(t *testing.T) {
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-a","function":{"name":"list","arguments":"{\"path\":"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call-b","function":{"name":"read","arguments":"{\"file\":"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\".\"}"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"a.txt\"}"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})

	events := drainEvents(t, mustStream(t, p))

	var summary []string
	for _, e := range events {
		switch {
		case len(e.ToolCalls) > 0:
			summary = append(summary, fmt.Sprintf("tools:%d", len(e.ToolCalls)))
		case e.Done:
			summary = append(summary, fmt.Sprintf("done(%s)", e.StopReason))
		case e.Err != nil:
			t.Fatalf("stream error: %v", e.Err)
		}
	}
	if strings.Join(summary, ",") != "tools:2,done(tool_calls)" {
		t.Fatalf("events = %v, want tools:2,done(tool_calls)", summary)
	}
	a, b := events[0].ToolCalls[0], events[0].ToolCalls[1]
	if a.ID != "call-a" || a.Name != "list" || a.Arguments["path"] != "." {
		t.Errorf("call A = %#v, want reassembled list call", a)
	}
	if b.ID != "call-b" || b.Name != "read" || b.Arguments["file"] != "a.txt" {
		t.Errorf("call B = %#v, want reassembled read call", b)
	}
	if events[1].Usage == nil || events[1].Usage.TotalTokens != 15 {
		t.Errorf("usage = %#v, want total 15", events[1].Usage)
	}
}

func mustStream(t *testing.T, p ModelProvider) <-chan StreamEvent {
	t.Helper()
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "go"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	return stream
}

func TestOpenAICompatibleMalformedChunkIsProtocolError(t *testing.T) {
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {not json}\n\n")
	})

	events := drainEvents(t, mustStream(t, p))
	if len(events) != 1 || events[0].Err == nil {
		t.Fatalf("events = %#v, want a single error event", events)
	}
	if got := Classify(events[0].Err); got != ErrKindProtocol {
		t.Errorf("kind = %v, want ErrKindProtocol", got)
	}
}

func TestOpenAICompatibleInvalidToolArgumentsIsProtocolError(t *testing.T) {
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"x","arguments":"{oops"}}]}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})

	events := drainEvents(t, mustStream(t, p))
	if len(events) == 0 || events[len(events)-1].Err == nil && !hasErr(events) {
		t.Fatalf("events = %#v, want an error event for invalid tool arguments", events)
	}
	if !hasErr(events) {
		t.Fatal("no error event")
	}
	for _, e := range events {
		if e.Err != nil && Classify(e.Err) != ErrKindProtocol {
			t.Fatalf("error kind = %v, want ErrKindProtocol", Classify(e.Err))
		}
	}
}

func hasErr(events []StreamEvent) bool {
	for _, e := range events {
		if e.Err != nil {
			return true
		}
	}
	return false
}

func TestOpenAICompatibleErrorStatusesAreClassified(t *testing.T) {
	cases := []struct {
		status int
		want   ErrorKind
	}{
		{http.StatusUnauthorized, ErrKindAuth},
		{http.StatusForbidden, ErrKindAuth},
		{http.StatusNotFound, ErrKindNotFound},
		{http.StatusInternalServerError, ErrKindServer},
		{http.StatusBadGateway, ErrKindServer},
		{http.StatusBadRequest, ErrKindInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d", tc.status), func(t *testing.T) {
			_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"error":{"message":"boom"}}`)
			})
			p.retry = fastRetry

			_, err := p.StreamChat(context.Background(), nil, nil)
			if err == nil {
				t.Fatal("StreamChat() succeeded, want status error")
			}
			if got := Classify(err); got != tc.want {
				t.Errorf("Classify() = %v, want %v (err: %v)", got, tc.want, err)
			}
		})
	}
}

func TestOpenAICompatibleAPIKeyNeverLeaksIntoErrors(t *testing.T) {
	const secretKey = "sk-super-secret-value"
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		// Some providers echo credentials back in error bodies.
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"bad key %s (from header %s)"}}`, secretKey, secretKey)
	})
	p.spec.APIKey = secretKey
	p.retry = fastRetry

	_, err := p.StreamChat(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want auth error")
	}
	if strings.Contains(err.Error(), secretKey) {
		t.Fatalf("error message leaked the API key: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("error = %q, want the key replaced by [redacted]", err.Error())
	}
}

func TestOpenAICompleteNonStreaming(t *testing.T) {
	var body map[string]any
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}
		}`)
	})

	resp, err := p.Complete(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if resp.Content != "answer" {
		t.Errorf("content = %q, want answer", resp.Content)
	}
	if resp.Usage.TotalTokens != 10 || resp.StopReason != FinishStop {
		t.Errorf("usage=%#v stop=%q, want 10/stop", resp.Usage, resp.StopReason)
	}
	if body["stream"] != false {
		t.Errorf("stream = %v, want false", body["stream"])
	}
}

func TestOpenAICompleteParsesToolCalls(t *testing.T) {
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"choices":[{
				"message":{
					"content":"",
					"tool_calls":[
						{"id":"c1","type":"function","function":{"name":"a","arguments":"{\"x\":1}"}},
						{"id":"c2","type":"function","function":{"name":"b","arguments":"{}"}}
					]
				},
				"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}
		}`)
	})

	resp, err := p.Complete(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("tool calls = %#v, want two", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Name != "a" || resp.ToolCalls[0].Arguments["x"] != float64(1) {
		t.Errorf("first call = %#v, want parsed arguments", resp.ToolCalls[0])
	}
	if resp.StopReason != FinishToolCalls {
		t.Errorf("stop reason = %q, want tool_calls", resp.StopReason)
	}
}

func TestOpenAICompleteMalformedBodyIsProtocolError(t *testing.T) {
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "{truncated")
	})

	_, err := p.Complete(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("Complete() succeeded on malformed body")
	}
	if got := Classify(err); got != ErrKindProtocol {
		t.Errorf("kind = %v, want ErrKindProtocol", got)
	}
}

func TestOpenAIListModels(t *testing.T) {
	var sawAuth string
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[{"id":"m-one"},{"id":"m-two"},{"id":""}]}`)
	})
	p.spec.APIKey = "sk-x"

	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if sawAuth != "Bearer sk-x" {
		t.Errorf("auth = %q, want bearer sk-x", sawAuth)
	}
	if len(models) != 2 || models[0].ID != "m-one" || models[1].ID != "m-two" {
		t.Errorf("models = %#v, want m-one and m-two (blank dropped)", models)
	}
}

func TestOpenAICancellationClosesPromptly(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`+"\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-block // hold the stream open
	})

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := p.StreamChat(ctx, nil, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	select {
	case event := <-stream:
		if event.Err != nil || event.Text != "partial" {
			t.Fatalf("first event = %+v, want partial text", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no first event")
	}

	cancel()
	for event := range stream {
		if event.Done {
			t.Fatal("stream reported Done after cancellation")
		}
	}
	// The channel closed promptly; nothing more to assert beyond no hang.
}

func TestOpenAITimeoutIsClassified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := NewOpenAICompatible(Spec{ID: "slow", Type: "openai-compatible", BaseURL: server.URL, Model: "m"})
	p.client = &http.Client{Timeout: 30 * time.Millisecond}
	p.retry = retryPolicy{} // no retries; we want the raw timeout

	_, err := p.StreamChat(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded against a timing-out server")
	}
	if got := Classify(err); got != ErrKindTimeout {
		t.Errorf("Classify() = %v (%v), want ErrKindTimeout", got, err)
	}
}

func TestOpenAIRateLimitRetriedThenSucceedsWithUsage(t *testing.T) {
	var requests atomic.Int64
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate limit exceeded"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	p.retry = fastRetry

	events := drainEvents(t, mustStream(t, p))
	text, done := "", false
	var usage *Usage
	for _, e := range events {
		if e.Err != nil {
			t.Fatalf("stream error: %v", e.Err)
		}
		text += e.Text
		if e.Usage != nil {
			usage = e.Usage
		}
		done = done || e.Done
	}
	if !done || text != "ok" || usage == nil || usage.PromptTokens != 2 {
		t.Fatalf("text=%q done=%v usage=%#v, want ok/true/prompt=2", text, done, usage)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2 (one 429, one success)", requests.Load())
	}
}

func TestOpenAICompatibleCapabilities(t *testing.T) {
	caps := CapabilitiesFor("openai-compatible")
	if !caps.Streaming || !caps.ToolCalling || !caps.Reasoning {
		t.Errorf("capabilities = %+v, want streaming/tools/reasoning", caps)
	}
}

func TestQuota429KindIsQuota(t *testing.T) {
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"you have exceeded your quota"}}`)
	})
	p.retry = fastRetry

	_, err := p.StreamChat(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want quota error")
	}
	if got := Classify(err); got != ErrKindQuota {
		t.Errorf("Classify() = %v, want ErrKindQuota", got)
	}
	var se *statusError
	if errors.As(err, &se) && !se.NonRetryable {
		t.Error("NonRetryable = false, want true for quota exhaustion")
	}
}
