package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forcefield/internal/tools"
)

func TestNormalizeLemonadeEndpoints(t *testing.T) {
	cases := []struct {
		raw      string
		wantAPI  string
		wantRoot string
	}{
		{"", DefaultLemonadeBaseURL, "http://localhost:13305"},
		{"http://localhost:13305", "http://localhost:13305/api/v1", "http://localhost:13305"},
		{"http://localhost:13305/", "http://localhost:13305/api/v1", "http://localhost:13305"},
		{"http://localhost:13305/api/v1", "http://localhost:13305/api/v1", "http://localhost:13305"},
		{"http://localhost:13305/v1/", "http://localhost:13305/v1", "http://localhost:13305"},
		{"http://localhost:13305/api/v0", "http://localhost:13305/api/v0", "http://localhost:13305"},
		{"http://localhost:13305/v0", "http://localhost:13305/v0", "http://localhost:13305"},
		{"https://proxy.example/lemonade/api/v1", "https://proxy.example/lemonade/api/v1", "https://proxy.example/lemonade"},
	}
	for _, tc := range cases {
		apiBase, rootBase := NormalizeLemonadeEndpoints(tc.raw)
		if apiBase != tc.wantAPI || rootBase != tc.wantRoot {
			t.Errorf("NormalizeLemonadeEndpoints(%q) = (%q, %q), want (%q, %q)",
				tc.raw, apiBase, rootBase, tc.wantAPI, tc.wantRoot)
		}
	}
}

func TestLemonadeChatEndpoint(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", "http://localhost:13305/v1/chat/completions"},
		{"http://localhost:13305", "http://localhost:13305/v1/chat/completions"},
		{"http://localhost:13305/", "http://localhost:13305/v1/chat/completions"},
		{"http://localhost:13305/api/v1", "http://localhost:13305/v1/chat/completions"},
		{"http://localhost:13305/api/v1/", "http://localhost:13305/v1/chat/completions"},
		{"http://localhost:13305/v1", "http://localhost:13305/v1/chat/completions"},
		{"http://localhost:13305/v1/", "http://localhost:13305/v1/chat/completions"},
		{"http://localhost:13305/api/v0", "http://localhost:13305/v1/chat/completions"},
		{"http://localhost:13305/v0", "http://localhost:13305/v1/chat/completions"},
		{"https://proxy.example/lemonade/api/v1", "https://proxy.example/lemonade/v1/chat/completions"},
		{"https://proxy.example/lemonade/v1", "https://proxy.example/lemonade/v1/chat/completions"},
		{"https://proxy.example/lemonade/v1/", "https://proxy.example/lemonade/v1/chat/completions"},
		{"https://proxy.example/lemonade", "https://proxy.example/lemonade/v1/chat/completions"},
		{"https://proxy.example/lemonade/", "https://proxy.example/lemonade/v1/chat/completions"},
		{"http://localhost:13305/api/v1/chat", "http://localhost:13305/v1/chat/completions"},
		{"http://localhost:13305/api/v1/chat/completions", "http://localhost:13305/v1/chat/completions"},
		{"http://localhost:13305/v1/chat/completions", "http://localhost:13305/v1/chat/completions"},
	}
	for _, tc := range cases {
		got := LemonadeChatEndpoint(tc.raw)
		if got != tc.want {
			t.Errorf("LemonadeChatEndpoint(%q) = %q, want %q", tc.raw, got, tc.want)
		}
		if strings.Contains(got, "/v1/v1") {
			t.Errorf("LemonadeChatEndpoint(%q) duplicated /v1: %q", tc.raw, got)
		}
		if strings.Contains(got, "/api/v1/chat") {
			t.Errorf("LemonadeChatEndpoint(%q) routed to management path: %q", tc.raw, got)
		}
	}
}

func TestLemonadeStreamChat_DrainsTrailingUsageAndTimingsAfterFinishReason(t *testing.T) {
	mock := newMockLemonadeServer()
	defer mock.close()

	// Point directly at host root (no /api/v1) to also exercise URL normalization.
	p := NewLemonade(Spec{
		ID:      "lemonade",
		Type:    "lemonade",
		Label:   "AMD Lemonade",
		BaseURL: mock.url(),
		Model:   "Qwen2.5-Coder-3B-Instruct-GGUF",
	})
	enabled := true
	p.SetReasoning(ReasoningConfig{
		Thinking: &ThinkingConfig{Enabled: &enabled},
	})

	ch, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, []tools.Definition{
		{Name: "read_file", Description: "read"},
	})
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}

	var texts, thinkings []string
	var gotTools []ToolCall
	var doneEvent *StreamEvent
	for ev := range ch {
		if ev.Err != nil {
			t.Fatalf("unexpected stream error: %v", ev.Err)
		}
		if ev.Thinking != "" {
			thinkings = append(thinkings, ev.Thinking)
		}
		if ev.Text != "" {
			texts = append(texts, ev.Text)
		}
		if len(ev.ToolCalls) > 0 {
			gotTools = append(gotTools, ev.ToolCalls...)
		}
		if ev.Done {
			copyEv := ev
			doneEvent = &copyEv
		}
	}

	if strings.Join(thinkings, "") != "checking prefix" {
		t.Errorf("thinking = %q, want %q", strings.Join(thinkings, ""), "checking prefix")
	}
	if strings.Join(texts, "") != "hello world" {
		t.Errorf("text = %q, want %q", strings.Join(texts, ""), "hello world")
	}
	if len(gotTools) != 1 || gotTools[0].Name != "read_file" || gotTools[0].Arguments["path"] != "README.md" {
		t.Errorf("tool calls = %+v, want read_file(path=README.md)", gotTools)
	}
	if doneEvent == nil {
		t.Fatal("missing Done StreamEvent")
	}
	if doneEvent.StopReason != FinishToolCalls {
		t.Errorf("StopReason = %q, want %q", doneEvent.StopReason, FinishToolCalls)
	}
	if doneEvent.Usage == nil {
		t.Fatal("Usage is nil - trailing SSE chunk after finish_reason was dropped")
	}
	if doneEvent.Usage.PromptTokens != 42 || doneEvent.Usage.CompletionTokens != 9 || doneEvent.Usage.CachedTokens != 30 {
		t.Errorf("Usage tokens = %+v, want prompt=42 completion=9 cached=30", doneEvent.Usage)
	}
	if doneEvent.Usage.Timings.CacheN != 30 || doneEvent.Usage.Timings.PredictedPerSecond != 90.0 {
		t.Errorf("Usage timings = %+v, want cache_n=30 predicted_per_second=90.0", doneEvent.Usage.Timings)
	}

	// Verify wire request carried stream_options.include_usage and top-level enable_thinking.
	mock.mu.Lock()
	req := mock.lastChatReq
	mock.mu.Unlock()
	so, ok := req["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Errorf("stream_options = %#v, want include_usage:true", req["stream_options"])
	}
	if req["enable_thinking"] != true {
		t.Errorf("enable_thinking = %#v, want true", req["enable_thinking"])
	}
	if _, hasNVIDIA := req["chat_template_kwargs"]; hasNVIDIA {
		t.Errorf("unexpected NVIDIA chat_template_kwargs in Lemonade payload: %#v", req["chat_template_kwargs"])
	}
}

func TestLemonadeStreamChat_MissingDoneReturnsProtocolError(t *testing.T) {
	mock := newMockLemonadeServer()
	defer mock.close()
	mock.mu.Lock()
	mock.dropDoneMarker = true
	mock.mu.Unlock()

	p := NewLemonade(Spec{
		ID:      "lemonade",
		Type:    "lemonade",
		BaseURL: mock.url() + "/v1",
		Model:   "Qwen2.5-Coder-3B-Instruct-GGUF",
	})
	ch, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("StreamChat() error = %v", err)
	}
	var gotErr error
	for ev := range ch {
		if ev.Err != nil {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected protocol error when SSE stream ends without [DONE]")
	}
	if Classify(gotErr) != ErrKindProtocol {
		t.Errorf("Classify(%v) = %v, want ErrKindProtocol", gotErr, Classify(gotErr))
	}
}

func TestLemonadeComplete_WithCachedTokensAndTimings(t *testing.T) {
	mock := newMockLemonadeServer()
	defer mock.close()

	p := NewLemonade(Spec{
		ID:      "lemonade",
		Type:    "lemonade",
		BaseURL: mock.url() + "/api/v1",
		Model:   "Qwen2.5-Coder-3B-Instruct-GGUF",
	})
	resp, err := p.Complete(context.Background(), []Message{{Role: UserRole, Content: "ping"}}, nil)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if resp.Content != "pong" || resp.Usage.CachedTokens != 8 || resp.Usage.Timings.CacheN != 8 {
		t.Errorf("Complete() resp = %+v", resp)
	}
}

func TestLemonadeListModelsAndDiscoveryContextWindow(t *testing.T) {
	mock := newMockLemonadeServer()
	defer mock.close()

	reg := NewFactoryRegistry()
	reg.MustRegister("lemonade", func(spec Spec) (ModelProvider, error) {
		return NewLemonade(spec), nil
	})
	disc := NewDiscovery(reg)
	models, err := disc.Fetch(context.Background(), Spec{
		ID:      "lemonade",
		Type:    "lemonade",
		BaseURL: mock.url() + "/api/v1",
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1 (un-downloaded model must be filtered)", len(models))
	}
	if models[0].ID != "Qwen2.5-Coder-3B-Instruct-GGUF" || models[0].ContextWindow != 32768 {
		t.Errorf("discovered model = %+v, want ContextWindow=32768", models[0])
	}
}

func TestLemonadeRefreshContextWindow_PrecedenceAndCache(t *testing.T) {
	mock := newMockLemonadeServer()
	defer mock.close()

	p := NewLemonade(Spec{
		ID:      "lemonade",
		Type:    "lemonade",
		BaseURL: mock.url() + "/api/v1",
		Model:   "Qwen2.5-Coder-3B-Instruct-GGUF",
	})

	// Fresh adapter must match static Capabilities() with zero eager network I/O.
	if caps := p.Capabilities(); caps.ContextWindow != 0 {
		t.Fatalf("initial ContextWindow = %d, want 0 before RefreshContextWindow", caps.ContextWindow)
	}

	now := time.Unix(1000, 0)
	p.now = func() time.Time { return now }

	if got := p.RefreshContextWindow(context.Background()); got != 8192 {
		t.Fatalf("RefreshContextWindow() = %d, want 8192 from /options resolved_ctx_size", got)
	}
	if got := p.Capabilities().ContextWindow; got != 8192 {
		t.Errorf("Capabilities().ContextWindow = %d, want 8192", got)
	}

	// Second call inside TTL must hit cache, not HTTP /options.
	if got := p.RefreshContextWindow(context.Background()); got != 8192 {
		t.Fatalf("cached RefreshContextWindow() = %d, want 8192", got)
	}
	mock.mu.Lock()
	hits := mock.optionsHits
	mock.mu.Unlock()
	if hits != 1 {
		t.Errorf("optionsHits = %d, want 1 (cached inside TTL)", hits)
	}

	// Advance past TTL and verify refresh re-queries.
	now = now.Add(31 * time.Second)
	_ = p.RefreshContextWindow(context.Background())
	mock.mu.Lock()
	hits = mock.optionsHits
	mock.mu.Unlock()
	if hits != 2 {
		t.Errorf("optionsHits = %d after TTL expiry, want 2", hits)
	}
}

func TestLemonadeStatusHintsAndRedaction(t *testing.T) {
	mock := newMockLemonadeServer()
	defer mock.close()

	secret := "sk-lemonade-secret-token-xyz"
	mock.mu.Lock()
	mock.forceChatStatus = http.StatusConflict
	mock.forceChatBody = "slots_pinned_error with key " + secret
	mock.mu.Unlock()

	p := NewLemonade(Spec{
		ID:      "lemonade",
		Type:    "lemonade",
		Label:   "AMD Lemonade",
		BaseURL: mock.url() + "/api/v1",
		APIKey:  secret,
		Model:   "Qwen2.5-Coder-3B-Instruct-GGUF",
	})
	_, err := p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error on 409 Conflict")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked secret: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "residency conflict") {
		t.Errorf("409 error missing residency hint: %q", err.Error())
	}

	// 400 on-demand hint.
	mock.mu.Lock()
	mock.forceChatStatus = http.StatusBadRequest
	mock.forceChatBody = "cannot serve on-demand model without loading"
	mock.mu.Unlock()
	_, err = p.StreamChat(context.Background(), []Message{{Role: UserRole, Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "on-demand") {
		t.Errorf("400 error missing on-demand hint: %v", err)
	}
}

func TestLemonadeWSL2HintOnLoopbackRefused(t *testing.T) {
	orig := isWSL2
	defer func() { isWSL2 = orig }()
	isWSL2 = func() bool { return true }

	hint := WSL2LoopbackConnectionHint("http://localhost:13305", errors.New("dial tcp 127.0.0.1:13305: connect: connection refused"))
	if !strings.Contains(hint, "networkingMode=mirrored") {
		t.Errorf("expected mirrored networking WSL2 hint, got %q", hint)
	}

	if winErrHint := WSL2LoopbackConnectionHint("http://127.0.0.1:11557", errors.New("dial tcp 127.0.0.1:11557: connectex: No connection could be made because the target machine actively refused it.")); !strings.Contains(winErrHint, "networkingMode=mirrored") {
		t.Errorf("expected mirrored networking hint for refused error text, got %q", winErrHint)
	}

	// Non-loopback host must NOT get WSL2 localhost hint.
	if noHint := WSL2LoopbackConnectionHint("http://192.168.1.50:13305", errors.New("connect: connection refused")); noHint != "" {
		t.Errorf("expected empty hint for non-loopback host, got %q", noHint)
	}
}

func TestLemonadeReasoningCapabilities(t *testing.T) {
	caps := ModelReasoningCapabilities("lemonade", "Qwen3-4B")
	if !caps.SupportsThinking() || caps.Thinking.Kind != ThinkingKindBool {
		t.Fatalf("lemonade thinking caps = %+v, want ThinkingKindBool", caps)
	}
	if caps.SupportsEffort() {
		t.Errorf("lemonade should not advertise OpenAI effort levels by default")
	}

	specCaps := ReasoningCapabilitiesForSpec(Spec{
		ID:    "work-lemon",
		Type:  "lemonade",
		Model: "Qwen3-4B",
	})
	if !specCaps.SupportsThinking() {
		t.Errorf("ReasoningCapabilitiesForSpec custom ID with type:lemonade missing Thinking")
	}
}

func TestLemonadeOfflineMetaCooldown(t *testing.T) {
	// Closed server triggers immediate connection error and records cooldown.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	p := NewLemonade(Spec{
		ID:      "lemonade",
		Type:    "lemonade",
		BaseURL: url + "/api/v1",
		Model:   "Qwen2.5-Coder-3B-Instruct-GGUF",
	})
	now := time.Unix(2000, 0)
	p.now = func() time.Time { return now }

	if got := p.RefreshContextWindow(context.Background()); got != 0 {
		t.Fatalf("offline RefreshContextWindow() = %d, want 0", got)
	}
	entry, ok := p.ctxCache.get(p.spec.Model)
	if !ok || !entry.failed {
		t.Fatalf("expected failed cache entry recorded after offline probe, got (%+v, %v)", entry, ok)
	}
}
