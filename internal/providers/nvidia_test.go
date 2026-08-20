package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func nvidiaSSE(t *testing.T, lines ...string) []StreamEvent {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range lines {
			fmt.Fprintf(w, "%s\n\n", line)
		}
	}))
	defer server.Close()

	provider := NewNvidiaProvider(server.URL, "test-model", "", nil)
	stream, err := provider.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	var events []StreamEvent
	for event := range stream {
		if event.Err != nil {
			t.Fatalf("unexpected stream error: %v", event.Err)
		}
		events = append(events, event)
	}
	return events
}

// TestNvidiaStreamRequestsThinkingViaChatTemplateKwargs pins the fix for
// GLM-5.x and other NIM-hosted models that gate reasoning behind a
// non-standard request field instead of streaming it unconditionally: NIM
// docs and the model cards for z-ai/glm5, glm5.1, and glm-5.2 require
// "chat_template_kwargs": {"enable_thinking": true} in the request body,
// or reasoning_content is never populated even on models that support it.
// Without this field, Forcefield's parsing is correct but has nothing to
// parse - the model just answers directly, which looks identical to "no
// reasoning support" from the outside. This test catches a regression
// where that field is dropped from the request.
func TestNvidiaStreamRequestsThinkingViaChatTemplateKwargs(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewNvidiaProvider(server.URL, "z-ai/glm-5.2", "", nil)
	stream, err := provider.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	for range stream {
	}

	kwargs, ok := gotBody["chat_template_kwargs"].(map[string]any)
	if !ok {
		t.Fatalf("request body missing chat_template_kwargs: %+v", gotBody)
	}
	if enable, _ := kwargs["enable_thinking"].(bool); !enable {
		t.Errorf("chat_template_kwargs.enable_thinking = %v, want true", kwargs["enable_thinking"])
	}
	if clear, ok := kwargs["clear_thinking"].(bool); !ok || clear {
		t.Errorf("chat_template_kwargs.clear_thinking = %v, want false (preserve reasoning across turns)", kwargs["clear_thinking"])
	}
}

func summarize(events []StreamEvent) []string {
	var out []string
	for _, e := range events {
		switch {
		case e.Thinking != "":
			out = append(out, "thinking:"+e.Thinking)
		case e.Text != "":
			out = append(out, "text:"+e.Text)
		case len(e.ToolCalls) > 0:
			out = append(out, fmt.Sprintf("tools:%d", len(e.ToolCalls)))
		case e.Done:
			out = append(out, "done")
		}
	}
	return out
}

func equalSeq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

// TestNvidiaStreamDecodesReasoningContentDeltas covers the field NIM
// documents for reasoning models (DeepSeek-R1, Qwen3, Nemotron…): the
// chain of thought streams in delta.reasoning_content, separately from and
// before the answer content.
func TestNvidiaStreamDecodesReasoningContentDeltas(t *testing.T) {
	events := nvidiaSSE(t,
		`data: {"choices":[{"delta":{"reasoning_content":"I need to check the file first"}}]}`,
		`data: {"choices":[{"delta":{"reasoning_content":" then compile it"}}]}`,
		`data: {"choices":[{"delta":{"content":"All done."},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	)
	equalSeq(t, summarize(events),
		"thinking:I need to check the file first",
		"thinking: then compile it",
		"text:All done.",
		"done",
	)
}

// TestNvidiaStreamDecodesReasoningFieldDelta covers the delta.reasoning
// spelling some other OpenAI-compatible gateways use for the same thing.
func TestNvidiaStreamDecodesReasoningFieldDelta(t *testing.T) {
	events := nvidiaSSE(t,
		`data: {"choices":[{"delta":{"reasoning":"weighing options"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	)
	equalSeq(t, summarize(events), "thinking:weighing options", "done")
}

// TestNvidiaStreamWithoutReasoningEmitsNoThinkingEvents makes sure a plain
// (non-reasoning) model stream is unchanged: content and done only.
func TestNvidiaStreamWithoutReasoningEmitsNoThinkingEvents(t *testing.T) {
	events := nvidiaSSE(t,
		`data: {"choices":[{"delta":{"content":"Hi"}}]}`,
		`data: {"choices":[{"delta":{"content":" there"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	)
	equalSeq(t, summarize(events), "text:Hi", "text: there", "done")
}

// TestNvidiaStreamReasoningThenToolCalls covers the reasoning-model tool
// path: reasoning deltas stream first, the tool call arrives in fragments,
// and finish_reason "tool_calls" completes the turn with both intact.
func TestNvidiaStreamReasoningThenToolCalls(t *testing.T) {
	events := nvidiaSSE(t,
		`data: {"choices":[{"delta":{"reasoning_content":"I should list the files"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"list","arguments":"{\"path\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\".\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	equalSeq(t, summarize(events), "thinking:I should list the files", "tools:1", "done")

	call := events[1].ToolCalls[0]
	if call.ID != "call-1" || call.Name != "list" || call.Arguments["path"] != "." {
		t.Errorf("tool call = %#v, want reassembled call-1 list {path:.}", call)
	}
}

// TestNvidiaStreamFlushesToolCallsWithoutFinishReason is the regression
// test for tool-call turns the server ends with a bare [DONE] (no
// finish_reason "tool_calls"): the buffered call used to be silently
// dropped, leaving the runtime an empty response that looked like the
// agent hanging in "thinking".
func TestNvidiaStreamFlushesToolCallsWithoutFinishReason(t *testing.T) {
	events := nvidiaSSE(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"echo","arguments":"{}"}}]}}]}`,
		`data: [DONE]`,
	)
	equalSeq(t, summarize(events), "tools:1", "done")
}

// TestNvidiaStreamFlushesToolCallsOnStopFinishReason covers the same drop
// when the server ends a tool-call turn with finish_reason "stop".
func TestNvidiaStreamFlushesToolCallsOnStopFinishReason(t *testing.T) {
	events := nvidiaSSE(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"echo","arguments":"{}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	)
	equalSeq(t, summarize(events), "tools:1", "done")
}

// TestNvidiaStreamFlushesToolCallsOnEOF covers a stream that just ends
// (connection closed) without [DONE] or any finish reason.
func TestNvidiaStreamFlushesToolCallsOnEOF(t *testing.T) {
	events := nvidiaSSE(t,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"echo","arguments":"{}"}}]}}]}`,
	)
	equalSeq(t, summarize(events), "tools:1", "done")
}

// TestNvidiaStreamPlainContentUnchanged pins the non-tool, non-reasoning
// happy path: content chunks stream as text and the turn completes.
func TestNvidiaStreamPlainContentUnchanged(t *testing.T) {
	events := nvidiaSSE(t,
		`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
		`data: {"choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"choices":[{"delta":{"content":" world"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	)
	equalSeq(t, summarize(events), "text:Hello", "text: world", "done")
}
