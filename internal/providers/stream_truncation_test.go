package providers

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// Regression tests: a content delta followed by bare EOF (no
// protocol-specific terminal marker) must surface as an error, never as
// a successful Done turn. Each adapter's legitimate terminal path is
// covered by the existing full-turn tests; these cover the truncated
// path only.

func streamAll(t *testing.T, ch <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var out []StreamEvent
	for e := range ch {
		out = append(out, e)
	}
	return out
}

func assertTruncated(t *testing.T, name string, events []StreamEvent) {
	t.Helper()
	if len(events) == 0 {
		t.Fatalf("%s: no events, want an error event for truncated stream", name)
	}
	for _, e := range events {
		if e.Done {
			t.Fatalf("%s: got Done event for truncated stream, want error (events=%+v)", name, events)
		}
	}
	last := events[len(events)-1]
	if last.Err == nil {
		t.Fatalf("%s: final event has no error, want truncation error (events=%+v)", name, events)
	}
	if _, ok := last.Err.(*protocolError); ok {
		return
	}
	// Wrapped protocol errors are acceptable; anything else is not.
	if Classify(last.Err) != ErrKindProtocol {
		t.Fatalf("%s: error kind = %v, want ErrKindProtocol (%v)", name, Classify(last.Err), last.Err)
	}
}

func TestOpenAICompatible_TruncatedStreamIsError(t *testing.T) {
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`+"\n\n")
		// No finish_reason, no [DONE]: bare EOF.
	})
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	assertTruncated(t, "openai-compatible", streamAll(t, stream))
}

func TestAnthropic_TruncatedStreamIsError(t *testing.T) {
	p := anthropicServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		// NB: writeSSEPayloads, not writeSSE: the latter appends a
		// [DONE] sentinel which Anthropic would reject as malformed
		// JSON, masking the bare-EOF path under test.
		writeSSEPayloads(w,
			`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`,
			// No message_stop: bare EOF.
		)
	})
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	assertTruncated(t, "anthropic", streamAll(t, stream))
}

func TestGemini_TruncatedStreamIsError(t *testing.T) {
	p := geminiServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		// Text part with no finishReason: bare EOF.
		writeSSEPayloads(w, `{"candidates":[{"content":{"parts":[{"text":"partial"}],"role":"model"}}]}`)
	})
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	assertTruncated(t, "gemini", streamAll(t, stream))
}

func TestResponses_TruncatedStreamIsError(t *testing.T) {
	url := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeResponsesSSE(w, `{"type":"response.output_text.delta","delta":"partial"}`)
		// No response.completed/incomplete: bare EOF.
	})
	p := NewOpenAIResponses(responsesSpec(url, "gpt-5.5"))
	stream, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	assertTruncated(t, "responses", collectStream(t, stream))
}
