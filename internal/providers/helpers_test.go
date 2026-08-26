package providers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestHTTPServer starts an httptest server for the lifetime of the
// test and returns its base URL.
func newTestHTTPServer(t *testing.T, handle http.HandlerFunc) string { //nolint:revive
	t.Helper()
	server := httptest.NewServer(handle)
	t.Cleanup(server.Close)
	return server.URL
}

// collectStream drains a provider stream, failing the test on transport
// misuse (never on in-band stream errors; those are data).
func collectStream(t *testing.T, stream <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var out []StreamEvent
	for event := range stream {
		out = append(out, event)
	}
	return out
}

// streamTextAndDone drains a stream expecting no errors and returns the
// concatenated text plus whether Done arrived.
func streamTextAndDone(t *testing.T, stream <-chan StreamEvent) (string, bool) {
	t.Helper()
	text := ""
	done := false
	for event := range stream {
		if event.Err != nil {
			t.Fatalf("stream error: %v", event.Err)
		}
		text += event.Text
		done = done || event.Done
	}
	return text, done
}

// writeSSEPayloads writes raw SSE data events without a trailing
// [DONE] sentinel - for protocols (Anthropic, Gemini) whose streams
// simply end instead of sending one.
func writeSSEPayloads(w http.ResponseWriter, payloads ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, payload := range payloads {
		fmt.Fprintf(w, "data: %s\n\n", payload)
	}
}
