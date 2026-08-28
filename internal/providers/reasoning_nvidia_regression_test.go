package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNvidia_DeepSeekV4Flash_Capabilities(t *testing.T) {
	caps := ModelReasoningCapabilities("nvidia", "deepseek-ai/deepseek-v4-flash-0731")
	if !caps.SupportsEffort() {
		t.Fatal("deepseek-ai/deepseek-v4-flash-0731 should support effort")
	}
	want := []string{"none", "high", "max"}
	if strings.Join(caps.Effort.Levels, ",") != strings.Join(want, ",") {
		t.Fatalf("effort levels = %v, want %v", caps.Effort.Levels, want)
	}
	if caps.SupportsThinking() {
		t.Fatalf("deepseek thinking should be unsupported (none is effort, not thinking off)")
	}
	// Validate accepted values
	for _, lvl := range want {
		if err := caps.ValidateEffort(lvl); err != nil {
			t.Errorf("ValidateEffort %q = %v, want nil", lvl, err)
		}
	}
	// Invalid values should be rejected
	for _, invalid := range []string{"low", "medium", "xhigh", "minimal", ""} {
		if err := caps.ValidateEffort(invalid); err == nil {
			t.Errorf("ValidateEffort %q should fail", invalid)
		}
	}
	// Case insensitive
	if err := caps.ValidateEffort("NONE"); err != nil {
		t.Errorf("NONE case insensitive = %v", err)
	}
	if got := caps.CanonicalEffort("NONE"); got != "none" {
		t.Errorf("CanonicalEffort NONE = %q, want none", got)
	}
	// Prefixed form should also be supported via normalization
	caps2 := ModelReasoningCapabilities("nvidia", "nvidia/deepseek-ai/deepseek-v4-flash-0731")
	if !caps2.SupportsEffort() || strings.Join(caps2.Effort.Levels, ",") != strings.Join(want, ",") {
		t.Fatalf("prefixed nvidia/deepseek should also be supported, got %v", caps2)
	}
}

func TestNvidia_MuseGlimmer_Capabilities(t *testing.T) {
	caps := ModelReasoningCapabilities("nvidia", "meta/muse-glimmer-30b")
	if !caps.SupportsEffort() {
		t.Fatal("meta/muse-glimmer-30b should support effort")
	}
	want := []string{"none", "minimal", "low", "medium", "high", "max"}
	if strings.Join(caps.Effort.Levels, ",") != strings.Join(want, ",") {
		t.Fatalf("effort levels = %v, want %v", caps.Effort.Levels, want)
	}
	// Must NOT expose xhigh as valid (model-card wording not API)
	if err := caps.ValidateEffort("xhigh"); err == nil {
		t.Fatal("xhigh should be invalid for Muse (API contract is max, not xhigh)")
	}
	// Thinking should be unsupported unless proven separate
	if caps.SupportsThinking() {
		t.Fatalf("Muse thinking should be unsupported per verified API (effort none is the off state)")
	}
	for _, lvl := range want {
		if err := caps.ValidateEffort(lvl); err != nil {
			t.Errorf("ValidateEffort %q = %v, want nil", lvl, err)
		}
	}
	// Prefixed
	caps2 := ModelReasoningCapabilities("nvidia", "nvidia/meta/muse-glimmer-30b")
	if !caps2.SupportsEffort() {
		t.Fatal("prefixed Muse should also be supported")
	}
}

func TestNvidia_UnsupportedRemainsUnsupported(t *testing.T) {
	unsupported := []string{"google/codgemma-7b", "thinkingmachines/inkling", "minimaxai/minimax-m3", "unknown/model"}
	for _, m := range unsupported {
		caps := ModelReasoningCapabilities("nvidia", m)
		if caps.SupportsEffort() || caps.SupportsThinking() {
			t.Errorf("nvidia/%s should be unsupported, got %v", m, caps)
		}
	}
}

func TestNvidia_DeepSeek_Request_StreamingAndNonStreaming(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(func() string {
			if stream {
				return "streaming"
			}
			return "non-streaming"
		}(), func(t *testing.T) {
			var gotBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewDecoder(r.Body).Decode(&gotBody)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Write([]byte("data: [DONE]\n\n"))
				} else {
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
				}
			}))
			defer server.Close()
			spec := Spec{ID: "nvidia", Type: "openai-compatible", Label: "NVIDIA NIM", BaseURL: server.URL, Model: "deepseek-ai/deepseek-v4-flash-0731"}
			p, err := DefaultFactories().Create(spec)
			if err != nil {
				t.Fatalf("Create = %v", err)
			}
			ra := p.(ReasoningAware)
			ra.SetReasoning(ReasoningConfig{Effort: "max"})
			if stream {
				ch, err := p.StreamChat(context.Background(), nil, nil)
				if err != nil {
					t.Fatalf("StreamChat = %v", err)
				}
				for range ch {
				}
			} else {
				// Use Complete via type assertion to OpenAICompatible
				if oc, ok := p.(*OpenAICompatible); ok {
					_, err := oc.Complete(context.Background(), nil, nil)
					if err != nil {
						t.Fatalf("Complete = %v", err)
					}
				} else {
					t.Skip("not OpenAICompatible")
				}
			}
			// Verify exact API contract: top-level reasoning_effort, no duplicate inside kwargs, no xhigh alias
			if gotBody["reasoning_effort"] != "max" {
				t.Errorf("reasoning_effort = %v, want max", gotBody["reasoning_effort"])
			}
			// Ensure we did NOT duplicate into chat_template_kwargs
			if kwargs, ok := gotBody["chat_template_kwargs"].(map[string]any); ok {
				if _, has := kwargs["reasoning_effort"]; has {
					t.Errorf("chat_template_kwargs should not contain reasoning_effort (API translates internally), got %v", kwargs)
				}
				// For DeepSeek, thinking is not separate, so enable_thinking should not be set by effort
				// The default extraBody for nvidia always has enable_thinking:true, but with no thinking config it should remain default.
				// That is acceptable, but we should not have added duplicate reasoning fields.
			}
			// Ensure thinking field not present (since thinking unsupported)
			if _, has := gotBody["thinking"]; has {
				t.Errorf("thinking should not be present for DeepSeek, got %v", gotBody)
			}
			// Verify unsupported field absence: ensure no reasoning_strength invented
			if kwargs, ok := gotBody["chat_template_kwargs"].(map[string]any); ok {
				if _, has := kwargs["reasoning_strength"]; has {
					t.Errorf("reasoning_strength should not be sent unless verified, got %v", kwargs)
				}
			}
		})
	}
	// Also test none and high
	for _, lvl := range []string{"none", "high"} {
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&gotBody)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: [DONE]\n\n"))
		}))
		spec := Spec{ID: "nvidia", Type: "openai-compatible", Label: "NVIDIA NIM", BaseURL: server.URL, Model: "deepseek-ai/deepseek-v4-flash-0731"}
		p, _ := DefaultFactories().Create(spec)
		p.(ReasoningAware).SetReasoning(ReasoningConfig{Effort: lvl})
		ch, _ := p.StreamChat(context.Background(), nil, nil)
		for range ch {
		}
		server.Close()
		if gotBody["reasoning_effort"] != lvl {
			t.Errorf("deepseek level %q got %v", lvl, gotBody["reasoning_effort"])
		}
	}
}

func TestNvidia_Muse_Request_StreamingAndNonStreaming(t *testing.T) {
	levels := []string{"none", "minimal", "low", "medium", "high", "max"}
	for _, lvl := range levels {
		t.Run(lvl, func(t *testing.T) {
			for _, stream := range []bool{true, false} {
				var gotBody map[string]any
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					json.NewDecoder(r.Body).Decode(&gotBody)
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						w.Write([]byte("data: [DONE]\n\n"))
					} else {
						w.Header().Set("Content-Type", "application/json")
						w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
					}
				}))
				spec := Spec{ID: "nvidia", Type: "openai-compatible", Label: "NVIDIA NIM", BaseURL: server.URL, Model: "meta/muse-glimmer-30b"}
				p, _ := DefaultFactories().Create(spec)
				p.(ReasoningAware).SetReasoning(ReasoningConfig{Effort: lvl})
				if stream {
					ch, _ := p.StreamChat(context.Background(), nil, nil)
					for range ch {
					}
				} else {
					if oc, ok := p.(*OpenAICompatible); ok {
						oc.Complete(context.Background(), nil, nil)
					}
				}
				server.Close()
				if gotBody["reasoning_effort"] != lvl {
					t.Errorf("Muse %s stream=%v reasoning_effort = %v, want %q", lvl, stream, gotBody["reasoning_effort"], lvl)
				}
				// Ensure xhigh not accepted
				if lvl == "max" {
					// Verify that xhigh would be rejected via capability, not request
					caps := ModelReasoningCapabilities("nvidia", "meta/muse-glimmer-30b")
					if err := caps.ValidateEffort("xhigh"); err == nil {
						t.Error("xhigh should be invalid for Muse")
					}
				}
				// No thinking field, no duplicate
				if _, has := gotBody["thinking"]; has {
					t.Errorf("thinking should not be present for Muse")
				}
			}
		})
	}
	// Verify that unsupported field absence: ensure no chat_template_kwargs.thinking invented
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	spec := Spec{ID: "nvidia", Type: "openai-compatible", Label: "NVIDIA NIM", BaseURL: server.URL, Model: "meta/muse-glimmer-30b"}
	p, _ := DefaultFactories().Create(spec)
	p.(ReasoningAware).SetReasoning(ReasoningConfig{Effort: "high"})
	ch, _ := p.StreamChat(context.Background(), nil, nil)
	for range ch {
	}
	server.Close()
	if kwargs, ok := gotBody["chat_template_kwargs"].(map[string]any); ok {
		if _, has := kwargs["reasoning_strength"]; has {
			t.Errorf("Muse should not send reasoning_strength without verified contract, got %v", kwargs)
		}
		if _, has := kwargs["thinking"]; has {
			t.Errorf("Muse should not send thinking without verified capability")
		}
	}
}

func TestNvidia_DeepSeek_NoneIsEffortNotThinking(t *testing.T) {
	caps := ModelReasoningCapabilities("nvidia", "deepseek-ai/deepseek-v4-flash-0731")
	if !caps.SupportsEffort() {
		t.Fatal("should support effort")
	}
	if caps.SupportsThinking() {
		t.Fatal("should not support thinking (none is effort)")
	}
	// Validate that thinking validation fails
	if err := caps.ValidateThinking(ThinkingConfig{Enabled: func() *bool { b := false; return &b }()}); err == nil {
		t.Fatal("thinking validation should fail for DeepSeek")
	}
	// But effort none should pass
	if err := caps.ValidateEffort("none"); err != nil {
		t.Fatalf("effort none = %v", err)
	}
	// Ensure that setting thinking via provider for DeepSeek would be filtered by runtime –
	// here we just verify capability detection, not provider leak
}

func TestNvidia_SwitchingDoesNotLeak(t *testing.T) {
	// Sequence: unsupported -> DeepSeek (max) -> Muse (minimal) -> unsupported
	// Verify no leak
	cases := []struct {
		provider string
		model    string
		effort   string // to set after switch, empty if unsupported
	}{
		{"nvidia", "google/codgemma-7b", ""},
		{"nvidia", "deepseek-ai/deepseek-v4-flash-0731", "max"},
		{"nvidia", "meta/muse-glimmer-30b", "minimal"},
		{"nvidia", "google/codgemma-7b", ""},
	}
	// Simulate runtime map
	selections := make(map[string]ReasoningConfig)
	for i, c := range cases {
		caps := ModelReasoningCapabilities(c.provider, c.model)
		key := c.provider + "\x00" + strings.ToLower(c.model)
		if c.effort != "" {
			if !caps.SupportsEffort() {
				t.Fatalf("step %d %s should support effort", i, c.model)
			}
			if err := caps.ValidateEffort(c.effort); err != nil {
				t.Fatalf("step %d validate %q = %v", i, c.effort, err)
			}
			selections[key] = ReasoningConfig{Effort: c.effort}
		} else {
			if caps.SupportsEffort() {
				t.Fatalf("step %d %s should be unsupported", i, c.model)
			}
			// Ensure no leak from previous: checking that current model's selection is empty
			if cfg, ok := selections[key]; ok && cfg.Effort != "" {
				t.Fatalf("step %d %s leaked effort %q from previous model", i, c.model, cfg.Effort)
			}
		}
		// Verify that if we later query previous model's stored value, it is still there
		if i == 3 {
			// After returning to unsupported, DeepSeek should still have max stored, Muse minimal
			if cfg, ok := selections["nvidia\x00deepseek-ai/deepseek-v4-flash-0731"]; !ok || cfg.Effort != "max" {
				t.Errorf("DeepSeek stored after roundtrip = %v, want max", cfg)
			}
			if cfg, ok := selections["nvidia\x00meta/muse-glimmer-30b"]; !ok || cfg.Effort != "minimal" {
				t.Errorf("Muse stored after roundtrip = %v, want minimal", cfg)
			}
		}
	}
}
