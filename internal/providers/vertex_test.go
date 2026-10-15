package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

// vertexServer spins up a fake Vertex AI endpoint. The handler sees every
// request; the provider uses a stub token so no real credentials load.
func vertexServer(t *testing.T, handle func(t *testing.T, w http.ResponseWriter, r *http.Request)) (*httptest.Server, *VertexProvider) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handle(t, w, r)
	}))
	t.Cleanup(server.Close)
	p := NewVertexProvider(Spec{
		ID:       "vtest",
		Type:     "vertex",
		Label:    "Vertex Test",
		BaseURL:  server.URL,
		Project:  "test-project",
		Location: "us-central1",
		Model:    "gemini-2.5-flash",
	})
	p.tokenFunc = func(context.Context) (string, error) { return "fake-token", nil }
	p.retry = fastRetry
	return server, p
}

func TestVertexRequestShape(t *testing.T) {
	var gotPath, gotQuery, gotAuth, gotCT string
	var gotBody map[string]any
	_, p := vertexServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		if err := decodeJSONBody(r, &gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n")
	})

	stream, err := p.StreamChat(context.Background(),
		[]Message{{Role: SystemRole, Content: "be brief"}, {Role: UserRole, Content: "hi"}},
		[]tools.Definition{{Name: "echo", Description: "Echo.", InputSchema: map[string]any{"type": "object"}}},
	)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)

	wantPath := "/v1/projects/test-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:streamGenerateContent"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
	if gotQuery != "alt=sse" {
		t.Errorf("query = %q, want alt=sse", gotQuery)
	}
	if gotAuth != "Bearer fake-token" {
		t.Errorf("Authorization = %q, want bearer token", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
	contents, ok := gotBody["contents"].([]any)
	if !ok || len(contents) != 1 {
		t.Fatalf("contents = %#v, want one user turn", gotBody["contents"])
	}
	if _, ok := gotBody["systemInstruction"]; !ok {
		t.Errorf("systemInstruction missing: %#v", gotBody)
	}
	toolsRaw, ok := gotBody["tools"].([]any)
	if !ok || len(toolsRaw) != 1 {
		t.Fatalf("tools = %#v, want one functionDeclarations entry", gotBody["tools"])
	}
}

func TestVertexStreamToolCallsUsage(t *testing.T) {
	_, p := vertexServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"list","args":{"path":"."}}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`+"\n\n")
	})

	events := drainEvents(t, mustStream(t, p))
	var sawTools bool
	var done FinishReason
	var usage *Usage
	for _, e := range events {
		if e.Err != nil {
			t.Fatalf("stream error: %v", e.Err)
		}
		if len(e.ToolCalls) == 1 {
			sawTools = true
			if e.ToolCalls[0].Name != "list" || e.ToolCalls[0].Arguments["path"] != "." {
				t.Errorf("tool call = %#v, want list path=.", e.ToolCalls[0])
			}
			if e.ToolCalls[0].ID == "" {
				t.Error("tool call ID empty, want synthetic ID")
			}
		}
		if e.Done {
			done = e.StopReason
			usage = e.Usage
		}
	}
	if !sawTools || done != FinishStop {
		t.Errorf("sawTools=%v done=%q, want true/stop", sawTools, done)
	}
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 5 || usage.TotalTokens != 15 {
		t.Errorf("usage = %#v, want 10/5/15", usage)
	}
}

func TestVertexFinishReasonLength(t *testing.T) {
	_, p := vertexServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[]},"finishReason":"MAX_TOKENS"}]}`+"\n\n")
	})

	events := drainEvents(t, mustStream(t, p))
	last := events[len(events)-1]
	if !last.Done || last.StopReason != FinishLength {
		t.Errorf("last = %+v, want done(length)", last)
	}
}

func TestVertexRPCErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   ErrorKind
	}{
		{http.StatusUnauthorized, `{"error":{"code":401,"message":"bad token","status":"UNAUTHENTICATED"}}`, ErrKindAuth},
		{http.StatusForbidden, `{"error":{"code":403,"message":"denied","status":"PERMISSION_DENIED"}}`, ErrKindAuth},
		{http.StatusNotFound, `{"error":{"code":404,"message":"not found","status":"NOT_FOUND"}}`, ErrKindNotFound},
		{http.StatusBadRequest, `{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}`, ErrKindInvalidRequest},
		{http.StatusTooManyRequests, `{"error":{"code":429,"message":"slow down","status":"UNAVAILABLE"}}`, ErrKindRateLimit},
		{http.StatusTooManyRequests, `{"error":{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, ErrKindQuota},
		{http.StatusInternalServerError, `{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`, ErrKindServer},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d", tc.status), func(t *testing.T) {
			_, p := vertexServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
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

func TestVertexMalformedChunkIsProtocolError(t *testing.T) {
	_, p := vertexServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
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

func TestVertexTokenErrorFailsClosed(t *testing.T) {
	const secret = "should-never-appear"
	_, p := vertexServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		t.Error("server hit despite token failure")
	})
	p.tokenFunc = func(context.Context) (string, error) { return "", errors.New("no ADC " + secret) }

	_, err := p.StreamChat(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded with failing token source")
	}
	if got := Classify(err); got != ErrKindAuth {
		t.Errorf("Classify() = %v, want ErrKindAuth", got)
	}
}

func TestVertexBadTokenRejected(t *testing.T) {
	_, p := vertexServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		t.Error("server hit with invalid token")
	})
	p.tokenFunc = func(context.Context) (string, error) { return "bad\ntoken", nil }

	_, err := p.StreamChat(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded with CRLF token")
	}
	if got := Classify(err); got != ErrKindAuth {
		t.Errorf("Classify() = %v, want ErrKindAuth", got)
	}
}

func TestVertexMissingProjectOrLocation(t *testing.T) {
	p := NewVertexProvider(Spec{ID: "v", Type: "vertex", BaseURL: "http://localhost:9", Model: "m"})
	p.tokenFunc = func(context.Context) (string, error) { return "t", nil }
	if _, err := p.StreamChat(context.Background(), nil, nil); err == nil {
		t.Fatal("StreamChat() succeeded without project/location")
	} else if !strings.Contains(err.Error(), "project") {
		t.Errorf("error = %q, want it to name project", err)
	}
	p2 := NewVertexProvider(Spec{ID: "v", Type: "vertex", BaseURL: "http://localhost:9", Model: "m", Project: "p"})
	p2.tokenFunc = func(context.Context) (string, error) { return "t", nil }
	if _, err := p2.StreamChat(context.Background(), nil, nil); err == nil {
		t.Fatal("StreamChat() succeeded without location")
	} else if !strings.Contains(err.Error(), "location") {
		t.Errorf("error = %q, want it to name location", err)
	}
}

func TestVertexFullResourceModelName(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewVertexProvider(Spec{
		ID: "v", Type: "vertex", BaseURL: server.URL,
		Project: "p", Location: "us-central1",
		Model: "projects/p/locations/us-central1/models/mytuned",
	})
	p.tokenFunc = func(context.Context) (string, error) { return "t", nil }
	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)
	want := "/v1/projects/p/locations/us-central1/models/mytuned:streamGenerateContent"
	if gotPath != want {
		t.Errorf("path = %q, want %q (resource name verbatim)", gotPath, want)
	}
}

func TestVertexNonStreamingComplete(t *testing.T) {
	_, p := vertexServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ":generateContent") {
			t.Errorf("path = %q, want :generateContent suffix", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}`)
	})

	resp, err := p.Complete(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if resp.Content != "answer" || resp.StopReason != FinishStop || resp.Usage.TotalTokens != 10 {
		t.Errorf("resp = %+v, want answer/stop/10", resp)
	}
}

func TestVertexCapabilities(t *testing.T) {
	p := NewVertexProvider(Spec{ID: "v", Type: "vertex", BaseURL: "http://localhost:9", Model: "m"})
	caps := p.Capabilities()
	if !caps.Streaming || !caps.ToolCalling {
		t.Errorf("caps = %+v, want streaming+tools", caps)
	}
	if got := CapabilitiesFor("vertex"); !got.Streaming {
		t.Errorf("CapabilitiesFor(vertex) = %+v, want streaming", got)
	}
}

func TestVertexReasoningCapabilities(t *testing.T) {
	caps := ModelReasoningCapabilities("vertex", "gemini-2.5-pro")
	if !caps.SupportsThinking() {
		t.Errorf("vertex gemini-2.5-pro should support thinking, got %+v", caps)
	}
	if caps := ModelReasoningCapabilities("vertex", "unknown-model"); caps.SupportsThinking() {
		t.Errorf("unknown vertex model should not claim thinking, got %+v", caps)
	}
}

func TestVertexThinkingBudgetInRequest(t *testing.T) {
	var gotBody map[string]any
	_, p := vertexServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		if err := decodeJSONBody(r, &gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	budget := 2048
	p.SetReasoning(ReasoningConfig{Thinking: &ThinkingConfig{Budget: &budget}})
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)
	gen, _ := gotBody["generationConfig"].(map[string]any)
	tc, _ := gen["thinkingConfig"].(map[string]any)
	if tc["thinkingBudget"] != float64(2048) {
		t.Errorf("thinkingConfig = %v, want budget 2048", tc)
	}
}

func decodeJSONBody(r *http.Request, into *map[string]any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(into)
}
