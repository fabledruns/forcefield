package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLMStudioErrorsNameLMStudioNotNIM pins the provider-label fix: a
// user pointing Forcefield at LM Studio must never be told to debug
// "NVIDIA NIM".
func TestLMStudioErrorsNameLMStudioNotNIM(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"message":"model not found"}}`))
	}))
	defer server.Close()

	p := NewLMStudioProvider(server.URL, "local-model")
	p.retry = fastRetry

	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want status error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "LM Studio") {
		t.Errorf("error = %q, want it to name LM Studio", msg)
	}
	if strings.Contains(msg, "NVIDIA") || strings.Contains(msg, "NIM") {
		t.Errorf("error = %q mentions NVIDIA for an LM Studio failure", msg)
	}
	if !strings.Contains(msg, "local-model") {
		t.Errorf("error = %q, want the model name for discoverability", msg)
	}
}

func TestConnectionErrorNamesLMStudio(t *testing.T) {
	// Port 0 on a closed listener: connection refused.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	p := NewLMStudioProvider(url, "local-model")

	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded against a dead endpoint, want transport error")
	}
	if !strings.Contains(err.Error(), "LM Studio") {
		t.Errorf("transport error = %q, want it to name LM Studio", err)
	}
}

func TestNvidiaAuthFailuresSuggestAPIKeyFix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer server.Close()

	t.Run("missing key", func(t *testing.T) {
		p := NewNvidiaProvider(server.URL, "test-model", "", nil)
		p.retry = fastRetry

		_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
		if err == nil {
			t.Fatal("StreamChat() succeeded, want 401 error")
		}
		if !strings.Contains(err.Error(), "NVIDIA_API_KEY") {
			t.Errorf("error = %q, want a hint to set NVIDIA_API_KEY", err)
		}
	})

	t.Run("invalid key", func(t *testing.T) {
		p := NewNvidiaProvider(server.URL, "test-model", "nvapi-invalid", nil)
		p.retry = fastRetry

		_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
		if err == nil {
			t.Fatal("StreamChat() succeeded, want 401 error")
		}
		msg := err.Error()
		if !strings.Contains(msg, "check that NVIDIA_API_KEY is valid") {
			t.Errorf("error = %q, want a hint to check the existing key", msg)
		}
		if strings.Contains(msg, "set the NVIDIA_API_KEY environment variable and restart") {
			t.Errorf("error = %q suggests setting a key that is already configured", msg)
		}
	})
}

func TestOllamaModelNotFoundSuggestsPull(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"model 'ornith:9b' not found"}`))
	}))
	defer server.Close()

	p := NewOllamaProvider(server.URL, "ornith:9b")
	p.retry = fastRetry

	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want 404 error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ollama pull ornith:9b") {
		t.Errorf("error = %q, want a concrete `ollama pull` suggestion", msg)
	}
}

func TestStatusHintAnnotationLeavesOtherErrorsAlone(t *testing.T) {
	plain := errors.New("boom")
	got := annotateStatusHint(plain, func(int, string) string { return "hint" })
	if got != plain && got.Error() != "boom" {
		t.Errorf("annotateStatusHint changed a non-status error: %v", got)
	}

	se := &statusError{Provider: "ollama", Model: "m", Status: 404}
	got = annotateStatusHint(se, func(status int, _ string) string {
		if status != 404 {
			t.Fatalf("hintFor status = %d, want 404", status)
		}
		return "run ollama pull m"
	})
	var out *statusError
	if !errors.As(got, &out) {
		t.Fatalf("annotated error type = %T, want *statusError", got)
	}
	if out.Hint == "" || !strings.Contains(out.Error(), "ollama pull m") {
		t.Errorf("annotated error = %q, want hint included", out.Error())
	}
}
