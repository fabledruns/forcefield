package providers

import (
	"context"
	"strings"
	"testing"
)

func TestIsValidHeaderValue(t *testing.T) {
	valid := []string{"", "abc", "Bearer sk-123", "https://example.com", "a\tb", "café"}
	for _, v := range valid {
		if !IsValidHeaderValue(v) {
			t.Errorf("IsValidHeaderValue(%q) = false, want true", v)
		}
	}
	invalid := []string{"a\nb", "a\rb", "a\r\nb", "a\x00b", "a\x1fb", "a\x7fb"}
	for _, v := range invalid {
		if IsValidHeaderValue(v) {
			t.Errorf("IsValidHeaderValue(%q) = true, want false", v)
		}
	}
	if !IsValidAPIKey("") {
		t.Error("IsValidAPIKey(\"\") = false, want true (no credential)")
	}
	if !IsValidAPIKey("sk-clean-123") {
		t.Error("IsValidAPIKey(clean) = false, want true")
	}
	if IsValidAPIKey("sk-bad\nkey") {
		t.Error("IsValidAPIKey(CRLF) = true, want false")
	}
}

func TestSpecRejectsBadAPIKeyWithoutLeak(t *testing.T) {
	const secret = "sk-super-secret-bad\nkey"
	spec := Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://localhost:9", APIKey: secret}
	err := spec.Validate()
	if err == nil {
		t.Fatal("Validate() accepted a CRLF key")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("validation error leaked the key: %q", err.Error())
	}
}

func TestSpecMetadataBlockedByDefault(t *testing.T) {
	for _, raw := range []string{
		"http://169.254.169.254/v1",
		"http://169.254.169.254:8080/v1",
		"http://metadata.google.internal/v1",
	} {
		spec := Spec{ID: "x", Type: "openai-compatible", BaseURL: raw}
		if err := spec.Validate(); err == nil {
			t.Errorf("Validate(%q) succeeded, want metadata refusal", raw)
		} else if !strings.Contains(strings.ToLower(err.Error()), "metadata") {
			t.Errorf("Validate(%q) error = %q, want metadata mention", raw, err)
		}
	}
}

func TestSpecMetadataOptIn(t *testing.T) {
	spec := Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://169.254.169.254/v1", AllowMetadataEndpoint: true}
	if err := spec.Validate(); err != nil {
		t.Errorf("Validate() with opt-in error = %v, want nil", err)
	}
}

func TestSpecLocalhostUnaffected(t *testing.T) {
	for _, raw := range []string{
		"http://localhost:11434",
		"http://127.0.0.1:11434",
		"http://192.168.1.10:1234/v1",
		"https://integrate.api.nvidia.com/v1",
	} {
		spec := Spec{ID: "x", Type: "openai-compatible", BaseURL: raw}
		if err := spec.Validate(); err != nil {
			t.Errorf("Validate(%q) error = %v, want nil (local/NIM preserved)", raw, err)
		}
	}
}

func TestAdaptersRejectBadKeyWithoutSending(t *testing.T) {
	const secret = "bad\nkey"
	// OpenAI-compatible: newRequest fails closed.
	oc := NewOpenAICompatible(Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://localhost:9", Model: "m", APIKey: secret})
	if _, err := oc.StreamChat(context.Background(), nil, nil); err == nil {
		t.Fatal("OpenAICompatible.StreamChat accepted a CRLF key")
	} else if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked the key: %q", err.Error())
	}

	// Anthropic / Gemini / Responses: request construction fails.
	a := NewAnthropicProvider(Spec{ID: "x", Type: "anthropic", BaseURL: "http://localhost:9", Model: "m", APIKey: secret})
	if _, err := a.newRequest(context.Background(), "GET", "http://localhost:9/v1/models", "", nil); err == nil {
		t.Fatal("Anthropic newRequest accepted a CRLF key")
	}
	g := NewGeminiProvider(Spec{ID: "x", Type: "gemini", BaseURL: "http://localhost:9", Model: "m", APIKey: secret})
	if _, err := g.newRequest(context.Background(), "GET", "http://localhost:9/v1beta/models", "", nil); err == nil {
		t.Fatal("Gemini newRequest accepted a CRLF key")
	}
	r := NewOpenAIResponses(Spec{ID: "x", Type: "openai-responses", BaseURL: "http://localhost:9", Model: "m", APIKey: secret})
	if _, err := r.newRequest(context.Background(), "GET", "http://localhost:9/models", "", nil); err == nil {
		t.Fatal("Responses newRequest accepted a CRLF key")
	}
}
