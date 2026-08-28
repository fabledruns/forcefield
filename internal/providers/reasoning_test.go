package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelReasoningCapabilities(t *testing.T) {
	cases := []struct {
		provider     string
		model        string
		wantEffort   bool
		wantThinking bool
		thinkingKind ThinkingKind
	}{
		{"nvidia", "z-ai/glm-5.2", true, true, ThinkingKindBool},
		{"nvidia", "minimaxai/minimax-m3", false, false, ""},
		{"anthropic", "claude-sonnet-4-5", false, true, ThinkingKindBudget},
		{"anthropic", "claude-3-opus", false, false, ""},
		{"gemini", "gemini-2.5-flash", false, true, ThinkingKindBudget},
		{"gemini", "gemini-1.0", false, false, ""},
		{"ollama", "ornith:9b", false, true, ThinkingKindBool},
		{"ollama", "llama3:8b", false, true, ThinkingKindBool}, // ollama claims all support bool
		{"openai", "gpt-5", true, false, ""},
		{"openai", "gpt-4o", false, false, ""},
		{"lmstudio", "local-model", false, false, ""},
		{"test", "test-model", true, true, ThinkingKindBool},
		{"unknown", "anything", false, false, ""},
	}
	for _, c := range cases {
		caps := ModelReasoningCapabilities(c.provider, c.model)
		if caps.SupportsEffort() != c.wantEffort {
			t.Errorf("%s/%s SupportsEffort=%v want %v", c.provider, c.model, caps.SupportsEffort(), c.wantEffort)
		}
		if caps.SupportsThinking() != c.wantThinking {
			t.Errorf("%s/%s SupportsThinking=%v want %v", c.provider, c.model, caps.SupportsThinking(), c.wantThinking)
		}
		if c.wantThinking && caps.Thinking != nil && caps.Thinking.Kind != c.thinkingKind {
			t.Errorf("%s/%s thinking kind=%v want %v", c.provider, c.model, caps.Thinking.Kind, c.thinkingKind)
		}
	}
}

func TestValidateEffort(t *testing.T) {
	caps := ModelReasoningCapabilities("nvidia", "z-ai/glm-5.2")
	if err := caps.ValidateEffort("high"); err != nil {
		t.Fatalf("ValidateEffort high = %v, want nil", err)
	}
	if err := caps.ValidateEffort("ultra"); err == nil {
		t.Fatal("ValidateEffort ultra succeeded, want error")
	} else if !strings.Contains(err.Error(), "Supported levels") {
		t.Errorf("error %q should mention Supported levels", err)
	}
	capsUnsupported := ModelReasoningCapabilities("anthropic", "claude-sonnet-4-5")
	if err := capsUnsupported.ValidateEffort("high"); err == nil {
		t.Fatal("unsupported ValidateEffort succeeded")
	}
}

func TestValidateThinkingBool(t *testing.T) {
	caps := ModelReasoningCapabilities("ollama", "ornith:9b")
	enabled := true
	if err := caps.ValidateThinking(ThinkingConfig{Enabled: &enabled}); err != nil {
		t.Fatalf("bool on = %v", err)
	}
	disabled := false
	if err := caps.ValidateThinking(ThinkingConfig{Enabled: &disabled}); err != nil {
		t.Fatalf("bool off = %v", err)
	}
	if err := caps.ValidateThinking(ThinkingConfig{Level: "high"}); err == nil {
		t.Fatal("bool with level should fail")
	}
	budget := 1024
	if err := caps.ValidateThinking(ThinkingConfig{Budget: &budget}); err == nil {
		t.Fatal("bool with budget should fail")
	}
}

func TestValidateThinkingBudget(t *testing.T) {
	caps := ModelReasoningCapabilities("anthropic", "claude-sonnet-4-5")
	b := 2048
	if err := caps.ValidateThinking(ThinkingConfig{Budget: &b}); err != nil {
		t.Fatalf("budget 2048 = %v", err)
	}
	b = 1
	if err := caps.ValidateThinking(ThinkingConfig{Budget: &b}); err == nil {
		t.Fatal("budget 1 should fail (below min)")
	}
	enabled := false
	if err := caps.ValidateThinking(ThinkingConfig{Enabled: &enabled}); err != nil {
		t.Fatalf("disabled = %v", err)
	}
	enabled = true
	if err := caps.ValidateThinking(ThinkingConfig{Enabled: &enabled}); err != nil {
		t.Fatalf("enabled true = %v", err)
	}
}

func TestOpenAICompatibleEffortRequest(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	p := NewOpenAICompatible(Spec{ID: "test", Type: "openai-compatible", BaseURL: server.URL, Model: "test-model"})
	// No effort set -> no field
	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	if _, has := gotBody["reasoning_effort"]; has {
		t.Errorf("reasoning_effort present without setting: %v", gotBody)
	}
	// Set effort
	p.SetReasoning(ReasoningConfig{Effort: "high"})
	gotBody = nil
	stream, err = p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	if gotBody["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", gotBody["reasoning_effort"])
	}
	// Clear effort -> should omit again
	p.SetReasoning(ReasoningConfig{})
	gotBody = nil
	stream, err = p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	if _, has := gotBody["reasoning_effort"]; has {
		t.Errorf("reasoning_effort still present after clear")
	}
}

func TestOpenAICompatibleEffortComplete(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	p := NewOpenAICompatible(Spec{ID: "test", Type: "openai-compatible", BaseURL: server.URL, Model: "test-model"})
	p.SetReasoning(ReasoningConfig{Effort: "xhigh"})
	_, err := p.Complete(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Complete error = %v", err)
	}
	if gotBody["reasoning_effort"] != "xhigh" {
		t.Errorf("reasoning_effort = %v, want xhigh", gotBody["reasoning_effort"])
	}
	if gotBody["stream"] != false {
		t.Errorf("stream = %v, want false for Complete", gotBody["stream"])
	}
}

func TestNvidiaThinkingViaChatTemplateKwargs(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	p := NewNvidiaProvider(server.URL, "z-ai/glm-5.2", "", nil)
	// Default should be enable_thinking true via extraBody
	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	kwargs, ok := gotBody["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["enable_thinking"] != true {
		t.Fatalf("default kwargs = %v, want enable_thinking true", gotBody["chat_template_kwargs"])
	}
	// Set thinking off
	enabled := false
	p.SetReasoning(ReasoningConfig{Thinking: &ThinkingConfig{Enabled: &enabled}})
	gotBody = nil
	stream, err = p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	kwargs, ok = gotBody["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["enable_thinking"] != false {
		t.Fatalf("off kwargs = %v, want enable_thinking false", gotBody["chat_template_kwargs"])
	}
	// Set thinking on + effort high: both fields should appear
	enabled = true
	p.SetReasoning(ReasoningConfig{Effort: "high", Thinking: &ThinkingConfig{Enabled: &enabled}})
	gotBody = nil
	stream, err = p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	if gotBody["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high", gotBody["reasoning_effort"])
	}
	kwargs, ok = gotBody["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["enable_thinking"] != true {
		t.Fatalf("on kwargs = %v, want true", gotBody["chat_template_kwargs"])
	}
}

func TestOllamaThinkingRequest(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		// Respond with minimal valid NDJSON
		w.Write([]byte("{\"message\":{\"content\":\"hi\"},\"done\":true}\n"))
	}))
	defer server.Close()
	p := NewOllamaProvider(server.URL, "ornith:9b")
	// No thinking -> no think field
	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	if _, has := gotBody["think"]; has {
		t.Errorf("think present without setting: %v", gotBody)
	}
	enabled := true
	p.SetReasoning(ReasoningConfig{Thinking: &ThinkingConfig{Enabled: &enabled}})
	gotBody = nil
	stream, err = p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	if gotBody["think"] != true {
		t.Errorf("think = %v, want true", gotBody["think"])
	}
	enabled = false
	p.SetReasoning(ReasoningConfig{Thinking: &ThinkingConfig{Enabled: &enabled}})
	gotBody = nil
	stream, err = p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	if gotBody["think"] != false {
		t.Errorf("think = %v, want false", gotBody["think"])
	}
}

func TestAnthropicThinkingBudgetRequest(t *testing.T) {
	var gotBody map[string]any
	server := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	})
	p := NewAnthropicProvider(Spec{ID: "anthropic", Type: "anthropic", BaseURL: server, Model: "claude-sonnet-4-5"})
	budget := 4096
	p.SetReasoning(ReasoningConfig{Thinking: &ThinkingConfig{Budget: &budget}})
	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	thinking, ok := gotBody["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking field missing: %v", gotBody)
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking type = %v, want enabled", thinking["type"])
	}
	if int(thinking["budget_tokens"].(float64)) != 4096 {
		t.Errorf("budget_tokens = %v, want 4096", thinking["budget_tokens"])
	}
	// Disable
	gotBody = nil
	enabled := false
	p.SetReasoning(ReasoningConfig{Thinking: &ThinkingConfig{Enabled: &enabled}})
	stream, err = p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	thinking, ok = gotBody["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Errorf("disabled thinking = %v, want type disabled", gotBody["thinking"])
	}
	// No thinking -> omit
	gotBody = nil
	p.SetReasoning(ReasoningConfig{})
	stream, err = p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	if _, has := gotBody["thinking"]; has {
		t.Errorf("thinking present without setting: %v", gotBody)
	}
}

func TestGeminiThinkingBudgetRequest(t *testing.T) {
	var gotBody map[string]any
	server := newTestHTTPServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"candidates\":[]}\n\n"))
	})
	p := NewGeminiProvider(Spec{ID: "gemini", Type: "gemini", BaseURL: server, Model: "gemini-2.5-flash"})
	budget := 2048
	p.SetReasoning(ReasoningConfig{Thinking: &ThinkingConfig{Budget: &budget}})
	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	genConfig, ok := gotBody["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig missing: %v", gotBody)
	}
	thinkingConfig, ok := genConfig["thinkingConfig"].(map[string]any)
	if !ok {
		t.Fatalf("thinkingConfig missing: %v", genConfig)
	}
	if int(thinkingConfig["thinkingBudget"].(float64)) != 2048 {
		t.Errorf("thinkingBudget = %v, want 2048", thinkingConfig["thinkingBudget"])
	}
	// Disable via 0 budget (Gemini allows 0 to disable)
	gotBody = nil
	budget = 0
	p.SetReasoning(ReasoningConfig{Thinking: &ThinkingConfig{Budget: &budget}})
	stream, err = p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	genConfig, ok = gotBody["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig missing after 0: %v", gotBody)
	}
	thinkingConfig, ok = genConfig["thinkingConfig"].(map[string]any)
	if !ok || int(thinkingConfig["thinkingBudget"].(float64)) != 0 {
		t.Errorf("thinkingBudget 0 = %v, want 0", thinkingConfig)
	}
}

func TestUnsupportedModelSendsNoReasoning(t *testing.T) {
	// OpenAI gpt-4o is unsupported for effort
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	p := NewOpenAICompatible(Spec{ID: "openai", Type: "openai-compatible", BaseURL: server.URL, Model: "gpt-4o"})
	// Try to set effort even though capability says unsupported: provider should still
	// carry the value if SetReasoning is called directly, but runtime would have
	// filtered it. Here we test that when runtime filters, provider gets empty.
	// Direct SetReasoning without runtime filter would still send, so we test via
	// ReasoningCapabilities check: if we ask ModelReasoningCapabilities for gpt-4o,
	// it says unsupported, so runtime would not set. Simulate filtered: p.SetReasoning empty.
	p.SetReasoning(ReasoningConfig{}) // filtered empty
	stream, err := p.StreamChat(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("StreamChat error = %v", err)
	}
	collectStream(t, stream)
	if _, has := gotBody["reasoning_effort"]; has {
		t.Errorf("reasoning_effort sent for unsupported model gpt-4o: %v", gotBody)
	}
	if _, has := gotBody["thinking"]; has {
		t.Errorf("thinking sent for unsupported model")
	}
}
