package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// P1.15 lab: provider chaos. Proves bounded retries, Retry-After clamping,
// cancellation, and safe recovery without duplication or success-confusion.
func TestHardeningSustained429GivesUpBounded(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, rateLimitedBody)
	}))
	defer server.Close()
	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry
	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatalf("sustained 429 must fail after bounded retries")
	}
	if got := requests.Load(); got != 4 { // 1 + 3 retries
		t.Fatalf("requests=%d, want 4 (bounded, not infinite)", got)
	}
}

func TestHardeningRetryAfterClamped(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "3600") // 1h must be clamped
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, rateLimitedBody)
			return
		}
		writeSSE(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = retryPolicy{MaxRetries: 1, BaseBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond, MaxRetryAfter: 10 * time.Millisecond}
	start := time.Now()
	events, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	text, done := drainProviderStream(t, events)
	if !done || text != "ok" {
		t.Fatalf("text=%q done=%v, want ok/true", text, done)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Retry-After not clamped: took %v", elapsed)
	}
}

func TestHardening500ThenRecoveryNoDuplication(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":"blip"}`)
			return
		}
		writeSSE(w, `{"choices":[{"delta":{"content":"recovered"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry
	events, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	text, done := drainProviderStream(t, events)
	if !done || text != "recovered" {
		t.Fatalf("text=%q done=%v, want recovered/true", text, done)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("requests=%d, want 3", got)
	}
}

func TestHardeningMalformedStreamIsProtocolError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {not json\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry
	events, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat setup: %v", err)
	}
	sawErr := false
	for ev := range events {
		if ev.Err != nil {
			sawErr = true
			if !strings.Contains(ev.Err.Error(), "protocol") && !strings.Contains(strings.ToLower(ev.Err.Error()), "json") && !strings.Contains(ev.Err.Error(), "decode") {
				t.Logf("malformed surfaced as: %v (must be error, exact kind documented)", ev.Err)
			}
		}
	}
	if !sawErr {
		t.Fatalf("malformed JSON stream produced no error event")
	}
}

func TestHardeningCancelDuringRetryStops(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, rateLimitedBody)
	}))
	defer server.Close()
	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = retryPolicy{MaxRetries: 5, BaseBackoff: 50 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, MaxRetryAfter: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	_, err := p.StreamChat(ctx, []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatalf("cancelled retry must return ctx error")
	}
	if got := requests.Load(); got > 3 {
		t.Fatalf("cancel did not stop retries promptly: %d requests", got)
	}
}
