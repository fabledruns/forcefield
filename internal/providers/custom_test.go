package providers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forcefield/internal/tools"
)

func boolPtr(b bool) *bool { return &b }

func TestCustomAuthBearerDefault(t *testing.T) {
	var gotAuth string
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	p.spec.APIKey = "sk-abc"

	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)
	if gotAuth != "Bearer sk-abc" {
		t.Errorf("Authorization = %q, want Bearer sk-abc", gotAuth)
	}
}

func TestCustomAuthNoneIgnoresKey(t *testing.T) {
	var gotAuth, gotCustom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Custom-Auth")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAICompatible(Spec{
		ID: "c", Type: "openai-compatible", BaseURL: server.URL, Model: "m",
		APIKey: "sk-should-not-send", AuthMode: "none",
	})
	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)
	if gotAuth != "" || gotCustom != "" {
		t.Errorf("auth headers = %q/%q, want both empty in none mode", gotAuth, gotCustom)
	}
}

func TestCustomAuthHeaderMode(t *testing.T) {
	var gotAuth, gotCustom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("X-Custom-Auth")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAICompatible(Spec{
		ID: "c", Type: "openai-compatible", BaseURL: server.URL, Model: "m",
		APIKey: "raw-key-123", AuthMode: "header", AuthHeader: "X-Custom-Auth",
	})
	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty in header mode", gotAuth)
	}
	if gotCustom != "raw-key-123" {
		t.Errorf("X-Custom-Auth = %q, want raw key", gotCustom)
	}
}

func TestCustomAuthHeaderWithScheme(t *testing.T) {
	var gotCustom string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCustom = r.Header.Get("X-Custom-Auth")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAICompatible(Spec{
		ID: "c", Type: "openai-compatible", BaseURL: server.URL, Model: "m",
		APIKey: "k", AuthMode: "header", AuthHeader: "X-Custom-Auth", AuthScheme: "Token",
	})
	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)
	if gotCustom != "Token k" {
		t.Errorf("X-Custom-Auth = %q, want 'Token k'", gotCustom)
	}
}

func TestCustomAuthBearerWithScheme(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAICompatible(Spec{
		ID: "c", Type: "openai-compatible", BaseURL: server.URL, Model: "m",
		APIKey: "k", AuthMode: "bearer", AuthScheme: "Token",
	})
	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)
	if gotAuth != "Token k" {
		t.Errorf("Authorization = %q, want 'Token k'", gotAuth)
	}
}

func TestCustomChatPathOverride(t *testing.T) {
	var gotChatPath, gotModelsPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/models"):
			gotModelsPath = r.URL.Path
			fmt.Fprint(w, `{"data":[]}`)
		default:
			gotChatPath = r.URL.Path
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))
	defer server.Close()

	p := NewOpenAICompatible(Spec{
		ID: "c", Type: "openai-compatible", BaseURL: server.URL, Model: "m",
		ChatPath: "/v1/custom/chat",
	})
	if p.ChatPath() != "/v1/custom/chat" {
		t.Fatalf("ChatPath() = %q, want override", p.ChatPath())
	}
	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)
	if gotChatPath != "/v1/custom/chat" {
		t.Errorf("chat path = %q, want override", gotChatPath)
	}
	if _, err := p.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if gotModelsPath != "/models" {
		t.Errorf("models path = %q, want /models unchanged", gotModelsPath)
	}
}

func TestCustomSpecValidation(t *testing.T) {
	cases := []struct {
		name string
		spec Spec
		want string
	}{
		{"bad auth mode", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", AuthMode: "oauth"}, "auth_mode"},
		{"header without name", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", AuthMode: "header"}, "auth_header"},
		{"header name reserved", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", AuthMode: "header", AuthHeader: "Authorization"}, "reserved"},
		{"header name invalid", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", AuthMode: "header", AuthHeader: "Bad Header"}, "invalid"},
		{"auth_header without mode", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", AuthHeader: "X-K"}, "auth_header requires"},
		{"scheme without mode", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", AuthScheme: "Bearer"}, "auth_scheme requires"},
		{"scheme with none", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", AuthMode: "none", AuthScheme: "Bearer"}, "auth_scheme requires"},
		{"chat path no slash", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", ChatPath: "chat"}, "api_path"},
		{"chat path query", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", ChatPath: "/chat?x=1"}, "api_path"},
		{"chat path traversal", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", ChatPath: "/a/../b"}, `".."`},
		{"timeout negative", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", TimeoutSeconds: -1}, "timeout_seconds"},
		{"timeout too big", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", TimeoutSeconds: 301}, "timeout_seconds"},
		{"header value CRLF", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", Headers: map[string]string{"X-A": "a\r\nB"}}, "control characters"},
		{"auth collision", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", AuthMode: "header", AuthHeader: "X-K", Headers: map[string]string{"X-K": "v"}}, "collides"},
		{"authorization override", Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", Headers: map[string]string{"Authorization": "x"}}, "reserved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.spec.Validate(); err == nil {
				t.Fatal("Validate() succeeded, want error")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}

	// Valid specs pass.
	valid := Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://h", AuthMode: "header", AuthHeader: "X-K", ChatPath: "/custom/chat", TimeoutSeconds: 30}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}
}

func TestCustomTimeoutOverride(t *testing.T) {
	p := NewOpenAICompatible(Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://localhost:9", Model: "m", TimeoutSeconds: 45})
	if p.client == nil {
		t.Fatal("client is nil")
	}
	tr, ok := p.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", p.client.Transport)
	}
	if tr.ResponseHeaderTimeout != 45*time.Second {
		t.Errorf("timeout = %v, want 45s", tr.ResponseHeaderTimeout)
	}
	def := NewOpenAICompatible(Spec{ID: "x", Type: "openai-compatible", BaseURL: "http://localhost:9", Model: "m"})
	if tr, _ := def.client.Transport.(*http.Transport); tr.ResponseHeaderTimeout != defaultStreamTimeout {
		t.Errorf("default timeout = %v, want %v", tr.ResponseHeaderTimeout, defaultStreamTimeout)
	}
}

func TestCustomCapabilitiesOverride(t *testing.T) {
	base := Capabilities{Streaming: true, ToolCalling: true, Reasoning: true}
	got := (&CapabilitiesOverride{Streaming: boolPtr(false)}).Apply(base)
	if got.Streaming || !got.ToolCalling {
		t.Errorf("Apply() = %+v, want only streaming forced false", got)
	}
	empty := CapabilitiesOverride{}
	if empty.Apply(base) != base {
		t.Errorf("empty override changed capabilities")
	}
	if ((*CapabilitiesOverride)(nil)).Apply(base) != base {
		t.Errorf("nil override changed capabilities")
	}

	// Streaming disabled fails fast with UnsupportedCapability.
	p := NewOpenAICompatible(Spec{
		ID: "c", Type: "openai-compatible", BaseURL: "http://localhost:9", Model: "m",
		CapabilitiesOverride: &CapabilitiesOverride{Streaming: boolPtr(false)},
	})
	if caps := p.Capabilities(); caps.Streaming {
		t.Error("Capabilities().Streaming = true, want false")
	}
	if _, err := p.StreamChat(context.Background(), nil, nil); err == nil {
		t.Fatal("StreamChat() succeeded with streaming disabled")
	} else if Classify(err) != ErrKindUnsupportedCapability {
		t.Errorf("Classify() = %v, want UnsupportedCapability (%v)", Classify(err), err)
	}

	// Tool calling disabled with defs fails fast.
	p2 := NewOpenAICompatible(Spec{
		ID: "c", Type: "openai-compatible", BaseURL: "http://localhost:9", Model: "m",
		CapabilitiesOverride: &CapabilitiesOverride{ToolCalling: boolPtr(false)},
	})
	defs := []tools.Definition{{Name: "x", Description: "x", InputSchema: map[string]any{"type": "object"}}}
	if _, err := p2.StreamChat(context.Background(), nil, defs); err == nil {
		t.Fatal("StreamChat() with tools succeeded when tool calling disabled")
	} else if Classify(err) != ErrKindUnsupportedCapability {
		t.Errorf("Classify() = %v, want UnsupportedCapability", Classify(err))
	}
	// Without defs it proceeds to the network (fails with connection, not capability).
	if _, err := p2.StreamChat(context.Background(), nil, nil); err != nil {
		if Classify(err) == ErrKindUnsupportedCapability {
			t.Errorf("StreamChat() without tools wrongly reported unsupported capability: %v", err)
		}
	}
}

func TestCustomHeaderInjectionSkipped(t *testing.T) {
	var gotEvil string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEvil = r.Header.Get("X-Evil")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	// Bypass Validate to simulate a programmatic Spec; newRequest must not
	// forward the CRLF value.
	p := NewOpenAICompatible(Spec{
		ID: "c", Type: "openai-compatible", BaseURL: server.URL, Model: "m",
		Headers: map[string]string{"X-Evil": "a\r\nInjected: yes"},
	})
	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	drainEvents(t, stream)
	if gotEvil != "" && strings.Contains(gotEvil, "Injected") {
		t.Errorf("injected header forwarded: %q", gotEvil)
	}
}

func TestCustomKeyRedactionHeaderMode(t *testing.T) {
	const secret = "hdr-secret-xyz"
	_, p := ocServer(t, func(t *testing.T, w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"bad key %s"}}`, secret)
	})
	p.spec.APIKey = secret
	p.spec.AuthMode = "header"
	p.spec.AuthHeader = "X-Custom-Auth"
	p.retry = fastRetry

	_, err := p.StreamChat(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("StreamChat() succeeded, want auth error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked key: %q", err.Error())
	}
}
