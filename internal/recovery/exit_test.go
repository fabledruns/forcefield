package recovery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

// transientNetError fakes a net.Error timeout so the classification
// wiring (Classify -> providers.IsTransient) is exercised without
// network timing.
type transientNetError struct{}

func (transientNetError) Error() string   { return "dial timeout" }
func (transientNetError) Timeout() bool   { return true }
func (transientNetError) Temporary() bool { return true }

// providerError runs one provider call against a canned status and
// returns the resulting error, so Classify sees real provider errors
// (wrapping included) rather than hand-built fakes.
func providerError(t *testing.T, status int, body string, headers map[string]string) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer server.Close()

	p := providers.NewNvidiaProvider(server.URL, "test-model", "", nil)
	_, err := p.StreamChat(context.Background(), []providers.Message{
		{Role: providers.UserRole, Content: "hi"},
	}, nil)
	if err == nil {
		t.Fatalf("provider StreamChat against %d succeeded, want an error", status)
	}
	return err
}

func TestClassifyExitCodes(t *testing.T) {
	// Fast transient: 429 with Retry-After: 0 skips the backoff waits.
	transient429 := providerError(t, http.StatusTooManyRequests,
		`{"error":{"message":"rate limit exceeded, please retry"}}`,
		map[string]string{"Retry-After": "0"})
	if !providers.IsTransient(transient429) {
		t.Fatalf("setup: 429 body is not transient: %v", transient429)
	}
	quota429 := providerError(t, http.StatusTooManyRequests,
		`{"error":{"message":"you exceeded your quota","code":"insufficient_quota"}}`,
		map[string]string{"Retry-After": "0"})
	if providers.IsTransient(quota429) {
		t.Fatalf("setup: quota error must never be transient: %v", quota429)
	}
	auth401 := providerError(t, http.StatusUnauthorized,
		`{"error":{"message":"invalid api key"}}`, nil)

	terminalErr := errors.New("stopped after 60 iterations (maximum reached)")

	cases := []struct {
		name  string
		event runtime.EventType
		err   error
		stats Stats
		want  int
	}{
		{"done", runtime.EventDone, nil, Stats{}, ExitOK},
		{"done with earlier denials still ok", runtime.EventDone, nil, Stats{Denied: 3}, ExitOK},
		{"blocked iterations", runtime.EventBlocked, terminalErr, Stats{Done: 5}, ExitTerminal},
		{"blocked with progress and denials", runtime.EventBlocked, terminalErr, Stats{Done: 1, Denied: 2}, ExitTerminal},
		{"cancelled event", runtime.EventCancelled, nil, Stats{}, ExitNeedsHuman},
		{"cancelled event with stats", runtime.EventCancelled, nil, Stats{Done: 4}, ExitNeedsHuman},
		{"context canceled error", runtime.EventError, context.Canceled, Stats{}, ExitNeedsHuman},
		{"wrapped cancel", runtime.EventError, fmt.Errorf("model call failed: %w", context.Canceled), Stats{}, ExitNeedsHuman},
		{"deadline exceeded", runtime.EventError, context.DeadlineExceeded, Stats{}, ExitNeedsHuman},
		// A deadline wrapped by provider layers (but never classified
		// transient by the runtime) is still an ordinary deadline: only
		// the runtime transient mark promotes it to retryable. This
		// guards against reordering IsTransient ahead of the shortcut,
		// which would flip every bare deadline to ExitRetryable.
		{"wrapped deadline without runtime mark stays human", runtime.EventError, fmt.Errorf("gateway timeout: %w", context.DeadlineExceeded), Stats{}, ExitNeedsHuman},
		{"transient 429", runtime.EventError, transient429, Stats{}, ExitRetryable},
		{"transient timeout class", runtime.EventError, transientNetError{}, Stats{}, ExitRetryable},
		{"quota exhaustion", runtime.EventError, quota429, Stats{}, ExitTerminal},
		{"auth failure", runtime.EventError, auth401, Stats{}, ExitTerminal},
		{"plain failure", runtime.EventError, terminalErr, Stats{Failed: 1}, ExitTerminal},
		{"nil error fails closed", runtime.EventError, nil, Stats{}, ExitTerminal},
		{"denied-only blocked needs human", runtime.EventBlocked, terminalErr, Stats{Denied: 2}, ExitNeedsHuman},
		{"denied-only error needs human", runtime.EventError, terminalErr, Stats{Denied: 1}, ExitNeedsHuman},
		{"unknown event fails closed", runtime.EventType(999), terminalErr, Stats{}, ExitTerminal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.event, tc.err, tc.stats); got != tc.want {
				t.Errorf("Classify(%v, %v, %+v) = %d, want %d",
					tc.event, tc.err, tc.stats, got, tc.want)
			}
		})
	}
}

func TestRetryableForSupervisor(t *testing.T) {
	if !RetryableForSupervisor(ExitRetryable) {
		t.Error("ExitRetryable must permit supervised restart")
	}
	for _, code := range []int{ExitOK, ExitTerminal, ExitNeedsHuman, 1, 99} {
		if RetryableForSupervisor(code) {
			t.Errorf("code %d must not permit supervised restart", code)
		}
	}
}

func TestStatsTallyAndDeniedOnly(t *testing.T) {
	var s Stats
	for _, typ := range []runtime.EventType{
		runtime.EventToolFinish, runtime.EventToolFailed,
		runtime.EventToolDenied, runtime.EventToolCancelled,
		runtime.EventText, // non-terminal: ignored
	} {
		s.Tally(typ)
	}
	if s.Done != 1 || s.Failed != 1 || s.Denied != 1 || s.Cancelled != 1 {
		t.Errorf("tally = %+v, want one of each terminal kind", s)
	}

	if (Stats{Denied: 2}).DeniedOnly() != true {
		t.Error("all-denied stats must read denied-only")
	}
	for _, s := range []Stats{
		{},
		{Denied: 1, Done: 1},
		{Denied: 1, Failed: 1},
		{Done: 1},
	} {
		if s.DeniedOnly() {
			t.Errorf("%+v must not read denied-only", s)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	for _, typ := range []runtime.EventType{
		runtime.EventDone, runtime.EventCancelled,
		runtime.EventBlocked, runtime.EventError,
	} {
		if !IsTerminal(typ) {
			t.Errorf("IsTerminal(%v) = false, want true", typ)
		}
	}
	for _, typ := range []runtime.EventType{
		runtime.EventText, runtime.EventThinking, runtime.EventToolStart,
		runtime.EventToolProgress, runtime.EventToolFinish, runtime.EventToolFailed,
		runtime.EventToolCancelled, runtime.EventToolDenied,
	} {
		if IsTerminal(typ) {
			t.Errorf("IsTerminal(%v) = true, want false", typ)
		}
	}
}
