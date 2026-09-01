package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var fastRetry = retryPolicy{
	MaxRetries:    3,
	BaseBackoff:   time.Millisecond,
	MaxBackoff:    4 * time.Millisecond,
	MaxRetryAfter: time.Minute,
}

const rateLimitedBody = `{"error":{"message":"rate limit exceeded, please retry"}}`

func writeSSE(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, chunk := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func drainProviderStream(t *testing.T, events <-chan StreamEvent) (text string, done bool) {
	t.Helper()
	for event := range events {
		if event.Err != nil {
			t.Fatalf("stream event error: %v", event.Err)
		}
		text += event.Text
		if event.Done {
			done = true
		}
	}
	return text, done
}

func TestNvidiaSuccessfulRequestIsNotRetried(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		writeSSE(w, `{"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry

	events, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	text, done := drainProviderStream(t, events)
	if !done || text != "hello" {
		t.Fatalf("text = %q, done = %v; want hello/true", text, done)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestNvidiaRetriesSingle429ThenSucceeds(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, rateLimitedBody)
			return
		}
		writeSSE(w, `{"choices":[{"delta":{"content":"recovered"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry

	events, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	text, done := drainProviderStream(t, events)
	if !done || text != "recovered" {
		t.Fatalf("text = %q, done = %v; want recovered/true", text, done)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (one 429, one success)", got)
	}
}

func TestOllamaRetriesSingle429ThenSucceeds(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, rateLimitedBody)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"message":{"content":"recovered"},"done":true}`)
	}))
	defer server.Close()

	p := NewOllamaProvider(server.URL, "test-model")
	p.retry = fastRetry

	events, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	text, done := drainProviderStream(t, events)
	if !done || text != "recovered" {
		t.Fatalf("text = %q, done = %v; want recovered/true", text, done)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (one 429, one success)", got)
	}
}

func TestNvidiaRetriesMultiple429sThenSucceeds(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= 3 {
			// No Retry-After: exercises the exponential backoff path.
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, rateLimitedBody)
			return
		}
		writeSSE(w, `{"choices":[{"delta":{"content":"recovered"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry

	events, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	text, done := drainProviderStream(t, events)
	if !done || text != "recovered" {
		t.Fatalf("text = %q, done = %v; want recovered/true", text, done)
	}
	if got := requests.Load(); got != 4 {
		t.Fatalf("requests = %d, want 4 (three 429s, one success)", got)
	}
}

func TestRetryAfterHeaderIsHonored(t *testing.T) {
	var requests atomic.Int64
	var firstRequest atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			firstRequest.Store(time.Now().UnixNano())
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, rateLimitedBody)
			return
		}
		elapsed := time.Duration(time.Now().UnixNano() - firstRequest.Load())
		if elapsed < 900*time.Millisecond {
			writeSSE(w, `{"choices":[{"delta":{"content":"too early"},"finish_reason":"stop"}]}`)
			return
		}
		writeSSE(w, `{"choices":[{"delta":{"content":"waited"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry

	events, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	text, _ := drainProviderStream(t, events)
	if text != "waited" {
		t.Fatalf("text = %q, want %q (retry fired before Retry-After elapsed)", text, "waited")
	}
}

func TestRetryBackoffRespectsContextCancellation(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, rateLimitedBody)
	}))
	defer server.Close()

	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = retryPolicy{
		MaxRetries:    10,
		BaseBackoff:   30 * time.Second,
		MaxBackoff:    time.Minute,
		MaxRetryAfter: time.Minute,
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	_, err := p.StreamChat(ctx, []Message{{Role: UserRole, Content: "hi"}}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StreamChat() error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancellation took %v, want a prompt return", elapsed)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 (no further attempt after cancellation)", got)
	}
}

func TestQuotaExhaustion429IsNotRetried(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"message":"You have exceeded your monthly quota for this API key"}}`)
	}))
	defer server.Close()

	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry

	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want quota error")
	}
	var statusErr *statusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error type = %T, want *statusError", err)
	}
	if !statusErr.NonRetryable {
		t.Error("NonRetryable = false, want true for quota exhaustion")
	}
	if !strings.Contains(err.Error(), "not retryable") {
		t.Errorf("error message = %q, want it to explain the limit isn't retryable", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 (quota 429 must not be retried)", got)
	}
}

func TestSustained429FailsAfterBoundedRetries(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, rateLimitedBody)
	}))
	defer server.Close()

	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry

	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want bounded-exhaustion error")
	}
	if !strings.Contains(err.Error(), "gave up after 3 retries") {
		t.Errorf("error message = %q, want it to report the retry budget", err)
	}
	if got := requests.Load(); got != 4 {
		t.Fatalf("requests = %d, want 4 (first attempt + 3 retries)", got)
	}
}

func TestSecondConcurrentRequestIsRejected(t *testing.T) {
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"slow\"}}]}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Hold the stream open until the test is done with it.
		<-release
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewNvidiaProvider(server.URL, "test-model", "", nil)

	events, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "one"}}, nil)
	if err != nil {
		t.Fatalf("first StreamChat() error = %v", err)
	}

	select {
	case event := <-events:
		if event.Err != nil {
			t.Fatalf("first stream error: %v", event.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first stream produced no events")
	}

	if _, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "two"}}, nil); !errors.Is(err, errRequestInFlight) {
		t.Fatalf("second concurrent StreamChat() error = %v, want errRequestInFlight", err)
	}

	close(release)
	released = true
	for event := range events {
		if event.Err != nil {
			t.Fatalf("first stream error after release: %v", event.Err)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{"empty", "", 0, false},
		{"zero seconds", "0", 0, true},
		{"positive seconds", "30", 30 * time.Second, true},
		{"negative clamps to zero", "-5", 0, true},
		{"garbage", "soon", 0, false},
		{"http date", now.Add(90 * time.Second).UTC().Format(http.TimeFormat), 90 * time.Second, true},
		{"past http date clamps", now.Add(-time.Hour).UTC().Format(http.TimeFormat), 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRetryAfter(tc.value, now)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("duration = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestQuotaExhaustedClassification(t *testing.T) {
	exhausted := []string{
		"You have exceeded your quota",
		"billing limit reached",
		"insufficient credits",
		"Please upgrade your plan",
		"402: payment required",
	}
	for _, body := range exhausted {
		if !quotaExhausted(body) {
			t.Errorf("quotaExhausted(%q) = false, want true", body)
		}
	}

	transient := []string{
		rateLimitedBody,
		"too many requests per minute",
		"",
	}
	for _, body := range transient {
		if quotaExhausted(body) {
			t.Errorf("quotaExhausted(%q) = true, want false", body)
		}
	}
}

func TestRetriesTransient503ThenSucceeds(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"service unavailable"}}`)
			return
		}
		writeSSE(w, `{"choices":[{"delta":{"content":"recovered"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry

	events, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	text, done := drainProviderStream(t, events)
	if !done || text != "recovered" {
		t.Fatalf("text = %q, done = %v; want recovered/true", text, done)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want 3 (two 503s then success)", got)
	}
}

func TestPermanent503FailsAfterRetries(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"message":"service unavailable"}}`)
	}))
	defer server.Close()

	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry

	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want error after retries")
	}
	var se *statusError
	if !errors.As(err, &se) {
		t.Fatalf("error type = %T, want *statusError", err)
	}
	if se.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", se.Status)
	}
	if got := Classify(err); got != ErrKindServer {
		t.Errorf("Classify = %v, want ErrKindServer", got)
	}
	if got := requests.Load(); got != 4 {
		t.Fatalf("requests = %d, want 4 (first attempt + 3 retries)", got)
	}
}

func TestAuthErrorIsNotRetried(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer server.Close()

	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry

	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want auth error")
	}
	if got := Classify(err); got != ErrKindAuth {
		t.Errorf("Classify = %v, want ErrKindAuth", got)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 (auth must not be retried)", got)
	}
}

func Test500IsRetried(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"internal"}}`)
			return
		}
		writeSSE(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry
	events, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	text, _ := drainProviderStream(t, events)
	if text != "ok" {
		t.Fatalf("text = %q, want ok", text)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

func TestNotImplemented501IsNotRetried(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotImplemented)
		fmt.Fprint(w, `{"error":{"message":"not implemented"}}`)
	}))
	defer server.Close()
	p := NewNvidiaProvider(server.URL, "test-model", "", nil)
	p.retry = fastRetry
	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want error")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 (501 must not be retried)", got)
	}
}

func TestTransportTimeoutIsRetried(t *testing.T) {
	var attempts atomic.Int64
	// Use a custom transport that fails first then succeeds
	base := http.DefaultTransport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	// Wrap client with failing transport for first attempt
	origClient := &http.Client{Timeout: 5 * time.Second, Transport: base}
	p := NewNvidiaProvider(server.URL, "test-model", "", origClient)
	p.retry = fastRetry
	// Replace client's transport with flaky one
	flaky := &flakyTransport{
		base:      base,
		failFirst: true,
		attempts:  &attempts,
	}
	p.client.Transport = flaky

	events, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	text, _ := drainProviderStream(t, events)
	if text != "ok" {
		t.Fatalf("text = %q, want ok", text)
	}
	// First transport failure counted, second succeeded via server
	if got := attempts.Load(); got < 1 {
		t.Fatalf("attempts = %d, want at least 1 failure before success", got)
	}
}

type flakyTransport struct {
	base      http.RoundTripper
	failFirst bool
	attempts  *atomic.Int64
	done      atomic.Bool
}

func (f *flakyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if f.failFirst && !f.done.Load() {
		f.done.Store(true)
		f.attempts.Add(1)
		return nil, &fakeTimeoutError{msg: "timeout"}
	}
	return f.base.RoundTrip(req)
}

type fakeTimeoutError struct{ msg string }

func (e *fakeTimeoutError) Error() string   { return e.msg }
func (e *fakeTimeoutError) Timeout() bool   { return true }
func (e *fakeTimeoutError) Temporary() bool { return true }
