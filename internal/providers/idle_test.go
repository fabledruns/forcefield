package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// idleTestHandler streams SSE chunks with a fixed gap between them,
// then [DONE]. A non-negative prefix sends only that many chunks and
// then stalls forever with the body held open (the exact reported
// failure mode); -1 never stalls.
func idleTestHandler(chunks []string, gap time.Duration, prefix int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		flush := func() {
			if flusher != nil {
				flusher.Flush()
			}
		}
		flush()
		for i, c := range chunks {
			if prefix >= 0 && i >= prefix {
				break
			}
			time.Sleep(gap)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
			flush()
		}
		if prefix >= 0 {
			// Stall: hold the body open with no bytes.
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
			return
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flush()
	}
}

// withShortIdle shrinks the body idle budget for one test. Production
// keeps 60s; tests must not wait that long.
func withShortIdle(t *testing.T, d time.Duration) {
	t.Helper()
	prev := defaultBodyIdleTimeout
	defaultBodyIdleTimeout = d
	t.Cleanup(func() { defaultBodyIdleTimeout = prev })
}

// idleTestProvider builds an OpenAI-compatible provider against server
// with transport retries disabled: post-200 stream failures never
// re-enter doWithRetry by design, so MaxRetries is irrelevant to the
// assertions and zero keeps the test fast.
func idleTestProvider(t *testing.T, server *httptest.Server) *OpenAICompatible {
	t.Helper()
	p := NewOpenAICompatible(Spec{ID: "idle-test", Type: "openai-compatible", BaseURL: server.URL, Model: "m"})
	p.retry = retryPolicy{MaxRetries: 0}
	return p
}

// drainStream collects a provider event channel until it closes or the
// watchdog fires, returning assembled text and the stream error, if any.
func drainStream(t *testing.T, events <-chan StreamEvent, watchdog time.Duration) (string, error) {
	t.Helper()
	var got strings.Builder
	var streamErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range events {
			if ev.Err != nil && streamErr == nil {
				streamErr = ev.Err
			}
			got.WriteString(ev.Text)
		}
	}()
	select {
	case <-done:
		return got.String(), streamErr
	case <-time.After(watchdog):
		t.Fatal("stream did not terminate")
		return "", nil
	}
}

// TestIdleTimeout_NormalStreamSucceeds pins requirement 1: regular
// chunks well inside the idle budget stream to completion.
func TestIdleTimeout_NormalStreamSucceeds(t *testing.T) {
	withShortIdle(t, 150*time.Millisecond)
	server := httptest.NewServer(idleTestHandler(
		[]string{"a", "b", "c", "d", "e", "f"}, 30*time.Millisecond, -1))
	defer server.Close()

	p := idleTestProvider(t, server)
	events, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	got, streamErr := drainStream(t, events, 5*time.Second)
	if streamErr != nil {
		t.Fatalf("normal stream failed: %v (Classify=%v)", streamErr, Classify(streamErr))
	}
	if got != "abcdef" {
		t.Errorf("content = %q, want %q", got, "abcdef")
	}
}

// TestIdleTimeout_StalledStreamFails pins requirement 2: headers plus
// one chunk, then silence, must fail instead of hanging.
func TestIdleTimeout_StalledStreamFails(t *testing.T) {
	withShortIdle(t, 150*time.Millisecond)
	server := httptest.NewServer(idleTestHandler([]string{"part"}, 0, 1))
	defer server.Close()

	p := idleTestProvider(t, server)
	events, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	start := time.Now()
	_, streamErr := drainStream(t, events, 5*time.Second)
	if streamErr == nil {
		t.Fatal("stalled stream succeeded, want an idle-timeout failure")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("failure took %v, want prompt idle expiry not a hang", elapsed)
	}
	if Classify(streamErr) != ErrKindTimeout {
		t.Errorf("Classify = %v, want %v (%v)", Classify(streamErr), ErrKindTimeout, streamErr)
	}
	if !IsTransient(streamErr) {
		t.Errorf("IsTransient = false, want true so turn-retry/ExitRetryable apply (%v)", streamErr)
	}
}

// TestIdleTimeout_TrickleFails pins requirement 7 (the exact reported
// failure mode): chunks arriving slower than the idle budget never make
// progress and must fail instead of holding the turn forever.
func TestIdleTimeout_TrickleFails(t *testing.T) {
	withShortIdle(t, 150*time.Millisecond)
	server := httptest.NewServer(idleTestHandler(
		[]string{"a", "b", "c"}, 400*time.Millisecond, -1))
	defer server.Close()

	p := idleTestProvider(t, server)
	events, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	_, streamErr := drainStream(t, events, 5*time.Second)
	if streamErr == nil {
		t.Fatal("trickle stream succeeded, want an idle-timeout failure")
	}
	if Classify(streamErr) != ErrKindTimeout || !IsTransient(streamErr) {
		t.Errorf("trickle failure = %v (Classify=%v transient=%v), want retryable timeout",
			streamErr, Classify(streamErr), IsTransient(streamErr))
	}
}

// TestIdleTimeout_LongActiveStreamSurvives pins requirement 5: a stream
// that stays active far longer than the idle budget (12 chunks x 30ms =
// ~360ms of streaming against a 150ms budget) must complete. There is
// no total timeout, only an idle one.
func TestIdleTimeout_LongActiveStreamSurvives(t *testing.T) {
	withShortIdle(t, 150*time.Millisecond)
	chunks := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "a", "b"}
	server := httptest.NewServer(idleTestHandler(chunks, 30*time.Millisecond, -1))
	defer server.Close()

	p := idleTestProvider(t, server)
	events, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	got, streamErr := drainStream(t, events, 5*time.Second)
	if streamErr != nil {
		t.Fatalf("long active stream failed: %v (Classify=%v)", streamErr, Classify(streamErr))
	}
	if got != "0123456789ab" {
		t.Errorf("content = %q, want full %q", got, "0123456789ab")
	}
}

// TestIdleTimeout_CleanStallIsTurnRetryable pins requirement 4 at the
// layer this package owns: a stall before any content is emitted
// classifies transient, which is exactly the input the runtime turn
// gate (canRetryTurn) and the exit contract (transient Error ->
// ExitRetryable, already pinned by TestClassifyExitCodes) consume.
// Mid-stream stalls take the same classification without turn replay,
// matching every other mid-stream failure by design.
func TestIdleTimeout_CleanStallIsTurnRetryable(t *testing.T) {
	withShortIdle(t, 150*time.Millisecond)
	server := httptest.NewServer(idleTestHandler(nil, 0, 0))
	defer server.Close()

	p := idleTestProvider(t, server)
	events, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	got, streamErr := drainStream(t, events, 5*time.Second)
	if streamErr == nil {
		t.Fatal("fully stalled stream succeeded, want failure")
	}
	if got != "" {
		t.Errorf("content = %q, want nothing emitted before the stall", got)
	}
	if !IsTransient(streamErr) {
		t.Errorf("clean-stall failure must be transient (turn-retryable), got %v", streamErr)
	}
}

// TestIdleReader_CloseThenReadFailsClosed pins that Close ends reads
// with an explicit error that can never be mistaken for clean EOF.
func TestIdleReader_CloseThenReadFailsClosed(t *testing.T) {
	r := newIdleTimeoutReader(io.NopCloser(strings.NewReader("hi")), time.Second)
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := r.Read(make([]byte, 8)); err == nil || err == io.EOF {
		t.Errorf("Read after Close = %v, want a non-EOF error", err)
	}
	// Second Close is a no-op, second read still fails closed.
	if err := r.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}

// TestIdleReader_CleanEOFNeverTrips pins that a completed body reports
// EOF (twice, like every Go reader) even with a tiny idle budget: only
// stalled bodies time out.
func TestIdleReader_CleanEOFNeverTrips(t *testing.T) {
	r := newIdleTimeoutReader(io.NopCloser(strings.NewReader("hi")), 50*time.Millisecond)
	defer r.Close()
	buf := make([]byte, 8)
	n, err := r.Read(buf)
	if err != nil || string(buf[:n]) != "hi" {
		t.Fatalf("Read = %d, %v; want 2, nil", n, err)
	}
	// Drain past EOF twice with time to spare for a spurious timeout.
	for i := 0; i < 2; i++ {
		if _, err := r.Read(buf); err != io.EOF {
			t.Fatalf("Read %d after body = %v, want EOF", i, err)
		}
		time.Sleep(80 * time.Millisecond)
	}
}
