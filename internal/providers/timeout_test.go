package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestDefaultClientHasTimeout pins that every adapter gets a bounded
// http.Client. A missing timeout would let a dead stream hang until the OS
// TCP timeout.
func TestDefaultClientHasTimeout(t *testing.T) {
	cases := []struct {
		name   string
		client *http.Client
	}{
		{"ollama", NewOllamaProvider("http://localhost:11434", "m").client},
		{"openai-compatible", NewOpenAICompatible(Spec{ID: "test", Type: "openai-compatible", BaseURL: "http://example.com", Model: "m"}).client},
		{"anthropic", NewAnthropicProvider(Spec{ID: "anthropic", Type: "anthropic", BaseURL: "https://api.anthropic.com", Model: "m"}).client},
		{"gemini", NewGeminiProvider(Spec{ID: "gemini", Type: "gemini", BaseURL: "https://generativelanguage.googleapis.com", Model: "m"}).client},
		{"nvidia", NewNvidiaProvider("https://integrate.api.nvidia.com/v1", "m", "", nil).client},
	}
	for _, c := range cases {
		if c.client == nil {
			t.Fatalf("%s client is nil", c.name)
		}
		if c.client.Timeout == 0 {
			t.Errorf("%s client Timeout is 0, want %v", c.name, defaultStreamTimeout)
		}
		if c.client.Timeout != defaultStreamTimeout {
			t.Errorf("%s client Timeout = %v, want %v", c.name, c.client.Timeout, defaultStreamTimeout)
		}
	}
}

// TestStreamHangsBoundedByTimeout verifies that a black-holed endpoint does
// not hang beyond the client timeout. It uses a deliberately short client
// timeout so the test runs quickly, and ensures the timeout is classified.
func TestStreamHangsBoundedByTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := NewOpenAICompatible(Spec{ID: "slow", Type: "openai-compatible", BaseURL: server.URL, Model: "m"})
	p.client = &http.Client{Timeout: 80 * time.Millisecond}
	p.retry = retryPolicy{MaxRetries: 0} // no retries, want raw timeout

	start := time.Now()
	_, err := p.StreamChat(context.Background(), nil, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("StreamChat succeeded against hanging server, want timeout")
	}
	if Classify(err) != ErrKindTimeout {
		t.Errorf("Classify = %v, want ErrKindTimeout (err=%v)", Classify(err), err)
	}
	// Allow generous epsilon for scheduling jitter but ensure we didn't wait
	// for OS TCP timeout (which would be seconds). The client timeout is 80ms
	// so elapsed should be close to that, not seconds.
	if elapsed > 800*time.Millisecond {
		t.Errorf("elapsed %v, want < 800ms (should fail at client timeout)", elapsed)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("elapsed %v, want >= 40ms (should actually wait for timeout)", elapsed)
	}
}

// TestOllamaDialFailureFast ensures local Ollama dial failure is still fast
// and is classified as connection failure, not hanging on timeout.
func TestOllamaDialFailureFast(t *testing.T) {
	// Use an unroutable address with a short timeout to simulate fast failure.
	// We use localhost on a closed port so dial fails quickly.
	p := NewOllamaProvider("http://127.0.0.1:1", "m")
	p.client = &http.Client{Timeout: 500 * time.Millisecond}
	p.retry = retryPolicy{MaxRetries: 0}
	start := time.Now()
	_, err := p.StreamChat(context.Background(), nil, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("StreamChat succeeded against closed port, want error")
	}
	if elapsed > 400*time.Millisecond {
		t.Errorf("dial failure took %v, want fast (<400ms)", elapsed)
	}
	if Classify(err) == ErrKindTimeout {
		t.Errorf("Classify = ErrKindTimeout for connection refused, want connection failure (got %v)", err)
	}
}
