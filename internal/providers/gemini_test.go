package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

func geminiServer(t *testing.T, handle func(t *testing.T, w http.ResponseWriter, r *http.Request)) *GeminiProvider {
	t.Helper()
	server := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		handle(t, w, r)
	})
	return NewGeminiProvider(Spec{
		ID:      "gemini",
		Type:    "gemini",
		BaseURL: server,
		Model:   "gemini-test",
	})
}

func TestGeminiRequestShape(t *testing.T) {
	var gotPath, gotRawQuery, gotKey string
	var gotBody map[string]any
	p := geminiServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		gotKey = r.Header.Get("x-goog-api-key")
		json.NewDecoder(r.Body).Decode(&gotBody)
		writeSSEPayloads(w, `{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP"}]}`)
	})
	p.spec.APIKey = "g-key"

	stream, err := p.StreamChat(context.Background(),
		[]Message{
			{Role: SystemRole, Content: "be terse"},
			{Role: UserRole, Content: "hi"},
		},
		[]tools.Definition{{Name: "echo", Description: "Echo.", InputSchema: map[string]any{"type": "object"}}},
	)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)

	if gotPath != "/v1beta/models/gemini-test:streamGenerateContent" {
		t.Errorf("path = %q", gotPath)
	}
	if q, _ := url.ParseQuery(gotRawQuery); q.Get("alt") != "sse" {
		t.Errorf("query = %q, want alt=sse", gotRawQuery)
	}
	if gotKey != "g-key" {
		t.Errorf("x-goog-api-key = %q, want the key on a header (never the URL)", gotKey)
	}

	sys, ok := gotBody["systemInstruction"].(map[string]any)
	if !ok {
		t.Fatalf("systemInstruction missing: %#v", gotBody["systemInstruction"])
	}
	sysParts := sys["parts"].([]any)
	if sysParts[0].(map[string]any)["text"] != "be terse" {
		t.Errorf("system text = %v", sysParts[0])
	}
	toolsRaw := gotBody["tools"].([]any)
	decls := toolsRaw[0].(map[string]any)["functionDeclarations"].([]any)
	if decls[0].(map[string]any)["name"] != "echo" {
		t.Errorf("declarations = %#v, want echo", decls)
	}
}

func TestGeminiToolCallRoundTrip(t *testing.T) {
	var gotContents []geminiContent
	p := geminiServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		var body struct {
			Contents []geminiContent `json:"contents"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		gotContents = body.Contents
		writeSSEPayloads(w, `{"candidates":[{"content":{"parts":[],"role":"model"},"finishReason":"STOP"}]}`)
	})

	history := []Message{
		{Role: UserRole, Content: "read a.txt"},
		{Role: AssistantRole, ToolCalls: []ToolCall{
			{ID: "call-1", Name: "read_file", Arguments: map[string]any{"path": "a.txt"}},
		}},
		{Role: ToolRole, Name: "read_file", ToolCallID: "call-1", Content: "file body"},
	}

	stream, err := p.StreamChat(context.Background(), history, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)

	if len(gotContents) != 3 {
		t.Fatalf("contents = %+v, want user/model/toolresult", gotContents)
	}
	modelTurn := gotContents[1]
	if modelTurn.Role != "model" {
		t.Errorf("assistant mapped to role %q, want model", modelTurn.Role)
	}
	call := modelTurn.Parts[0].FunctionCall
	if call == nil || call.Name != "read_file" || call.Args["path"] != "a.txt" {
		t.Errorf("functionCall part = %+v, want read_file(a.txt)", modelTurn.Parts[0].FunctionCall)
	}

	resultTurn := gotContents[2]
	resp := resultTurn.Parts[0].FunctionResponse
	if resp == nil || resp.Name != "read_file" || resp.Response["result"] != "file body" {
		t.Errorf("functionResponse = %+v, want read_file result", resp)
	}
}

func TestGeminiStreamFullTurn(t *testing.T) {
	p := geminiServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		writeSSEPayloads(w,
			`{"candidates":[{"content":{"parts":[{"text":"He"},{"text":"llo"}],"role":"model"}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`,
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"run","args":{"cmd":"ls"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":9,"totalTokenCount":12}}`,
		)
	})

	events := drainEvents(t, mustStream(t, p))

	text := ""
	for _, e := range events {
		if e.Err != nil {
			t.Fatalf("stream error: %v", e.Err)
		}
		text += e.Text
	}
	if text != "Hello" {
		t.Errorf("text = %q, want Hello", text)
	}

	last := events[len(events)-1]
	if !last.Done || last.StopReason != FinishStop {
		t.Fatalf("final = %+v, want Done/stop", last)
	}
	if last.Usage == nil || last.Usage.TotalTokens != 12 {
		t.Errorf("usage = %#v, want total 12 (last chunk wins)", last.Usage)
	}

	var callsEvent *StreamEvent
	for i := range events {
		if len(events[i].ToolCalls) > 0 {
			callsEvent = &events[i]
		}
	}
	if callsEvent == nil || len(callsEvent.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v, want one batched event", callsEvent)
	}
	call := callsEvent.ToolCalls[0]
	if call.ID == "" || call.Name != "run" || call.Arguments["cmd"] != "ls" {
		t.Errorf("call = %#v, want synthesized ID + run(ls)", call)
	}
}

func TestGeminiFinishReasonLengthMapsToLength(t *testing.T) {
	p := geminiServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		writeSSEPayloads(w, `{"candidates":[{"content":{"parts":[{"text":"cut off"}],"role":"model"},"finishReason":"MAX_TOKENS"}]}`)
	})

	events := drainEvents(t, mustStream(t, p))
	last := events[len(events)-1]
	if last.StopReason != FinishLength {
		t.Errorf("stop reason = %q, want length", last.StopReason)
	}
}

func TestGeminiErrorChunkSurfacesMessage(t *testing.T) {
	p := geminiServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		writeSSEPayloads(w, `{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`)
	})

	events := drainEvents(t, mustStream(t, p))
	if len(events) == 0 || events[len(events)-1].Err == nil {
		t.Fatalf("events = %#v, want an error event", events)
	}
	msg := events[len(events)-1].Err.Error()
	if !strings.Contains(msg, "Resource exhausted") || !strings.Contains(msg, "RESOURCE_EXHAUSTED") {
		t.Errorf("error = %q, want message and status", msg)
	}
}

func TestGeminiMalformedChunkIsProtocolError(t *testing.T) {
	p := geminiServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		writeSSEPayloads(w, `not-json-at-all`)
	})

	events := drainEvents(t, mustStream(t, p))
	if len(events) == 0 || events[len(events)-1].Err == nil {
		t.Fatal("want an error event for malformed chunk")
	}
	if got := Classify(events[len(events)-1].Err); got != ErrKindProtocol {
		t.Errorf("kind = %v, want ErrKindProtocol", got)
	}
}

func TestGeminiModelEscapedInPath(t *testing.T) {
	p := geminiServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/") && strings.Count(r.URL.Path, "/") > 4 {
			t.Errorf("path = %q, model should be path-escaped", r.URL.Path)
		}
		writeSSEPayloads(w, `{"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}]}`)
	})
	p.spec.Model = "models/weird name"

	events := collectStream(t, mustStream(t, p))
	for _, e := range events {
		if e.Err != nil {
			t.Fatalf("stream error: %v", e.Err)
		}
	}
}

func TestGeminiAuthFailureNamesEnvVar(t *testing.T) {
	server := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"error":{"code":403,"message":"API key not valid","status":"PERMISSION_DENIED"}}`)
	})

	p := NewGeminiProvider(Spec{ID: "gemini", Type: "gemini", BaseURL: server, Model: "m"})
	p.retry = fastRetry

	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want auth error")
	}
	if !strings.Contains(err.Error(), "GEMINI_API_KEY") {
		t.Errorf("error = %q, want a hint naming GEMINI_API_KEY", err)
	}
}

func TestGeminiCompleteAndListModels(t *testing.T) {
	p := geminiServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ":generateContent"):
			fmt.Fprint(w, `{
				"candidates":[{"content":{"parts":[{"text":"hi "},{"functionCall":{"name":"go","args":{"x":1}}}]},"finishReason":"STOP"}],
				"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":4,"totalTokenCount":6}
			}`)
		case r.URL.Path == "/v1beta/models":
			fmt.Fprint(w, `{"models":[
				{"name":"models/gemini-a","displayName":"Gemini A"},
				{"name":"models/gemini-b"}
			]}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})

	resp, err := p.Complete(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if resp.Content != "hi " || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "go" {
		t.Errorf("response = %#v, want text + function call", resp)
	}
	if resp.Usage.TotalTokens != 6 {
		t.Errorf("usage = %#v, want total 6", resp.Usage)
	}

	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(models) != 2 || models[0].ID != "gemini-a" || models[0].Name != "Gemini A" || models[1].ID != "gemini-b" {
		t.Errorf("models = %#v, want stripped IDs with display fallback", models)
	}
}

func TestGeminiListModelsFiltersNonChatModels(t *testing.T) {
	p := geminiServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		fmt.Fprint(w, `{"models":[
			{"name":"models/gemini-pro","displayName":"Gemini Pro","supportedGenerationMethods":["generateContent"]},
			{"name":"models/embedding-001","supportedGenerationMethods":["embedContent"]},
			{"name":"models/legacy","supportedGenerationMethods":[]},
			{"name":"models/mystery"}
		]}`)
	})

	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	// Only models that can serve generateContent (or report no methods at
	// all) are offered - embedding endpoints are excluded on protocol
	// evidence, not guesswork.
	if strings.Join(ids, ",") != "gemini-pro,mystery" {
		t.Fatalf("ids = %v, want gemini-pro,mystery", ids)
	}
}

func TestGeminiCapabilities(t *testing.T) {
	caps := CapabilitiesFor("gemini")
	if !caps.Streaming || !caps.ToolCalling || !caps.ParallelToolCalls {
		t.Errorf("capabilities = %+v, want streaming/tools/parallel", caps)
	}
}
