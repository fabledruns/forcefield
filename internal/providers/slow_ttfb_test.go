package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSlowTTFB_ValidResponseNotClassifiedAsUnreachable verifies that a
// provider that is slow to start streaming (TTFB) due to reasoning (e.g.
// Kimi K3 on NVIDIA NIM) is not incorrectly reported as "could not reach"
// (ErrKindConnection). With the fix, ResponseHeaderTimeout bounds only the
// headers, while Client.Timeout is 0 for streaming, so a slow but valid
// body is allowed.

// slowTTFBSSEHandler sleeps before the first SSE chunk, but sends headers
// immediately. This simulates Kimi K3 thinking before the first token.
func slowTTFBSSEHandler(delay time.Duration, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(delay)
		fmt.Fprint(w, body)
		fmt.Fprint(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// slowHeaderHandler sleeps before sending headers at all.
func slowHeaderHandler(delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

func TestSlowTTFB_ValidSlowBodyNotTimedOut(t *testing.T) {
	// Server sends headers immediately, then 200ms later sends the first SSE chunk.
	// This is a valid slow Kimi K3 response (reasoning). With the old
	// Client.Timeout=80ms, the body read would timeout. With the new
	// ResponseHeaderTimeout=300ms and Client.Timeout=0, it should succeed.
	server := httptest.NewServer(slowTTFBSSEHandler(200*time.Millisecond, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
	defer server.Close()

	// Old client: overall Timeout 80ms should timeout on slow body
	oldClient := &http.Client{Timeout: 80 * time.Millisecond}
	pOld := NewOpenAICompatible(Spec{ID: "slow-old", Type: "openai-compatible", BaseURL: server.URL, Model: "m"})
	pOld.client = oldClient
	pOld.retry = retryPolicy{MaxRetries: 0}

	_, errOld := pOld.StreamChat(context.Background(), nil, nil)
	if errOld == nil {
		// StreamChat itself may succeed (Do succeeds) but the stream will fail
		// during body read. We need to drain the stream.
		t.Log("old client StreamChat did not error on Do, checking stream")
	} else {
		if Classify(errOld) == ErrKindConnection {
			t.Errorf("old client should not classify slow body timeout as unreachable, got %v", errOld)
		}
	}

	// Drain old stream if it succeeded
	if errOld == nil {
		// Actually StreamChat returned a channel, we need to check the stream error
		events, _ := pOld.StreamChat(context.Background(), nil, nil)
		// This is racy because we already called StreamChat, but for the test we
		// want to check the body timeout. Simpler: test with new client.
		_ = events
	}

	// New client: no overall timeout, header timeout 300ms, should succeed
	newClient := &http.Client{
		Transport: &http.Transport{ResponseHeaderTimeout: 300 * time.Millisecond},
	}
	pNew := NewOpenAICompatible(Spec{ID: "slow-new", Type: "openai-compatible", BaseURL: server.URL, Model: "m"})
	pNew.client = newClient
	pNew.retry = retryPolicy{MaxRetries: 0}

	events, err := pNew.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("new client StreamChat failed for valid slow body, want success: %v (Classify=%v)", err, Classify(err))
	}
	// Drain stream and ensure we get the hello content and not a timeout
	var got string
	var streamErr error
	timeout := time.After(2 * time.Second)
	done := make(chan struct{})
	go func() {
		for ev := range events {
			if ev.Err != nil {
				streamErr = ev.Err
			}
			got += ev.Text
		}
		close(done)
	}()
	select {
	case <-done:
	case <-timeout:
		t.Fatal("stream did not complete")
	}
	if streamErr != nil {
		t.Fatalf("valid slow body stream failed: %v (Classify=%v)", streamErr, Classify(streamErr))
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("expected hello in stream, got %q", got)
	}
	if streamErr != nil && Classify(streamErr) == ErrKindConnection {
		t.Errorf("valid slow body should not be classified as unreachable")
	}
}

func TestSlowTTFB_HeaderTimeoutCorrectlyClassified(t *testing.T) {
	// Server sleeps 200ms before sending headers at all.
	server := httptest.NewServer(slowHeaderHandler(200 * time.Millisecond))
	defer server.Close()

	// Client with short header timeout should timeout and be classified as Timeout, not Connection
	client := &http.Client{
		Transport: &http.Transport{ResponseHeaderTimeout: 80 * time.Millisecond},
	}
	p := NewOpenAICompatible(Spec{ID: "slow-header", Type: "openai-compatible", BaseURL: server.URL, Model: "m"})
	p.client = client
	p.retry = retryPolicy{MaxRetries: 0}

	_, err := p.StreamChat(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected timeout for slow headers, got success")
	}
	if Classify(err) != ErrKindTimeout {
		t.Errorf("slow header should be ErrKindTimeout, got %v (err=%v)", Classify(err), err)
	}
	if Classify(err) == ErrKindConnection {
		t.Errorf("slow header incorrectly classified as unreachable (ErrKindConnection)")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "timed out") {
		t.Errorf("slow header error should mention timed out, got %q", err.Error())
	}
	if strings.Contains(strings.ToLower(err.Error()), "could not reach") {
		t.Errorf("slow header timeout should not be reported as 'could not reach', got %q", err.Error())
	}

	// With a longer header timeout, the same slow server should succeed
	client2 := &http.Client{
		Transport: &http.Transport{ResponseHeaderTimeout: 400 * time.Millisecond},
	}
	p2 := NewOpenAICompatible(Spec{ID: "slow-header-ok", Type: "openai-compatible", BaseURL: server.URL, Model: "m"})
	p2.client = client2
	p2.retry = retryPolicy{MaxRetries: 0}
	events, err := p2.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("with longer header timeout, valid slow header should succeed, got %v", err)
	}
	// Drain
	var got string
	for ev := range events {
		if ev.Err != nil {
			t.Fatalf("stream error with longer timeout: %v", ev.Err)
		}
		got += ev.Text
	}
	if !strings.Contains(got, "ok") {
		t.Errorf("expected ok, got %q", got)
	}
}

func TestNvidiaKimi_HasLongerHeaderTimeout(t *testing.T) {
	// Kimi K3 on NIM is slow; the provider should have a longer header timeout
	// than the default so a valid 60s+ TTFB is not mis-classified.
	pDefault := NewOpenAICompatible(Spec{ID: "openai", Type: "openai-compatible", BaseURL: "https://api.openai.com", Model: "gpt-4"})
	var defaultHeader time.Duration
	if pDefault.client.Transport != nil {
		if tr, ok := pDefault.client.Transport.(*http.Transport); ok {
			defaultHeader = tr.ResponseHeaderTimeout
		}
	}
	pKimi := NewOpenAICompatible(Spec{ID: "nvidia", Type: "openai-compatible", BaseURL: "https://integrate.api.nvidia.com/v1", Model: "kimi-k3"})
	var kimiHeader time.Duration
	if pKimi.client.Transport != nil {
		if tr, ok := pKimi.client.Transport.(*http.Transport); ok {
			kimiHeader = tr.ResponseHeaderTimeout
		}
	}
	if kimiHeader <= defaultHeader {
		t.Errorf("Kimi K3 header timeout %v should be longer than default %v", kimiHeader, defaultHeader)
	}
	if kimiHeader < 250*time.Second {
		t.Errorf("Kimi K3 header timeout %v seems too short for slow thinking, want >=250s (got %v)", kimiHeader, kimiHeader)
	}
	pNvidia := NewNvidiaProvider("https://integrate.api.nvidia.com/v1", "kimi-k3", "", nil)
	var nvidiaHeader time.Duration
	if pNvidia.client.Transport != nil {
		if tr, ok := pNvidia.client.Transport.(*http.Transport); ok {
			nvidiaHeader = tr.ResponseHeaderTimeout
		}
	}
	if nvidiaHeader != defaultNvidiaTimeout {
		t.Errorf("NewNvidiaProvider Kimi header %v want %v", nvidiaHeader, defaultNvidiaTimeout)
	}
}
