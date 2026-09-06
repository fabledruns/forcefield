package providers

import (
	"strings"
	"testing"
)

func TestContextLimitsForModel_Known(t *testing.T) {
	cases := []struct {
		model      string
		wantWindow int
		wantMaxOut int
	}{
		{"gpt-4o-mini", 128000, 4096},
		{"GPT-4O", 128000, 4096},
		{"claude-sonnet-4-5", 200000, 8192},
		{"claude-haiku-4-5", 200000, 8192},
		{"gemini-2.5-flash", 1048576, 8192},
		{"gemini-2.5-pro", 1048576, 8192},
		{"grok-3-mini", 131072, 4096},
	}
	for _, tc := range cases {
		window, maxOut := ContextLimitsForModel(tc.model)
		if window != tc.wantWindow {
			t.Errorf("ContextLimitsForModel(%q) window = %d, want %d", tc.model, window, tc.wantWindow)
		}
		if maxOut != tc.wantMaxOut {
			t.Errorf("ContextLimitsForModel(%q) maxOut = %d, want %d", tc.model, maxOut, tc.wantMaxOut)
		}
	}
}

func TestContextLimitsForModel_UnknownStaysZero(t *testing.T) {
	// Unknown models must never get a fabricated window. The runtime
	// falls back to message-count windowing instead.
	for _, model := range []string{"", "   ", "some-future-model-999", "ornith:9b-custom-variant-xyz"} {
		// ornith:9b itself is intentionally unknown: local windows vary.
		if strings.Contains(model, "ornith") {
			continue
		}
		if window, _ := ContextLimitsForModel(model); window != 0 {
			t.Errorf("ContextLimitsForModel(%q) window = %d, want 0 (unknown)", model, window)
		}
	}
	if window, _ := ContextLimitsForModel(""); window != 0 {
		t.Errorf("empty model window = %d, want 0", window)
	}
}

func TestCapabilitiesForModel_MergesTransportAndTable(t *testing.T) {
	caps := CapabilitiesForModel("anthropic", "claude-sonnet-4-5")
	if !caps.ToolCalling || !caps.Streaming {
		t.Errorf("transport caps lost: %+v", caps)
	}
	if caps.ContextWindow != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", caps.ContextWindow)
	}
	if caps.MaxOutputTokens != 8192 {
		t.Errorf("MaxOutputTokens = %d, want 8192", caps.MaxOutputTokens)
	}

	// Unknown model keeps transport behavior, gets conservative reserve.
	unknown := CapabilitiesForModel("ollama", "mystery-model-123")
	if unknown.ContextWindow != 0 {
		t.Errorf("unknown ContextWindow = %d, want 0", unknown.ContextWindow)
	}
	if unknown.MaxOutputTokens <= 0 {
		t.Errorf("unknown MaxOutputTokens = %d, want conservative default > 0", unknown.MaxOutputTokens)
	}

	// Unknown transport reports nothing fabricated.
	bogus := CapabilitiesForModel("no-such-transport", "gpt-4o")
	if bogus.ContextWindow != 0 && bogus.Streaming {
		t.Errorf("bogus transport should not stream: %+v", bogus)
	}
}

func TestResolveCapabilities(t *testing.T) {
	// Unreported provider + known model: table fills limits only.
	got := ResolveCapabilities(&silentProvider{}, "gpt-4o-mini")
	if got.ContextWindow != 128000 || got.MaxOutputTokens != 4096 {
		t.Errorf("unreported + known = %+v, want table limits", got)
	}
	if got.ToolCalling {
		t.Errorf("unreported transport must not claim tool calling: %+v", got)
	}
	if ReportsCapabilities(&silentProvider{}) {
		t.Error("non-reporting provider must be distinguishable from incapable")
	}

	// Reporting instance: transport features preserved, table fills gaps.
	rep := &fakeCapsProvider{caps: Capabilities{Streaming: true, ToolCalling: true, ParallelToolCalls: true}}
	got = ResolveCapabilities(rep, "gpt-4o-mini")
	if !got.ToolCalling || !got.ParallelToolCalls || got.ContextWindow != 128000 {
		t.Errorf("reported merge = %+v", got)
	}
	if !ReportsCapabilities(rep) {
		t.Error("reporting provider not recognized")
	}

	// Provider-reported window wins over the table.
	rep = &fakeCapsProvider{caps: Capabilities{ToolCalling: true, ContextWindow: 64000, MaxOutputTokens: 2048}}
	got = ResolveCapabilities(rep, "gpt-4o-mini")
	if got.ContextWindow != 64000 || got.MaxOutputTokens != 2048 {
		t.Errorf("provider values must win: %+v", got)
	}

	// Unknown model: conservative reserve, unknown window.
	got = ResolveCapabilities(rep, "mystery-999")
	if got.ContextWindow != 64000 {
		t.Errorf("transport window must survive unknown model: %+v", got)
	}
	got = ResolveCapabilities(nil, "mystery-999")
	if got.ContextWindow != 0 || got.MaxOutputTokens != DefaultReserveTokens {
		t.Errorf("nil provider = %+v, want unknown window + reserve", got)
	}
	if ReportsCapabilities(nil) {
		t.Error("nil provider reports capabilities")
	}
}

type fakeCapsProvider struct {
	ModelProvider
	caps Capabilities
}

func (f *fakeCapsProvider) Capabilities() Capabilities { return f.caps }

// silentProvider implements ModelProvider without Capabilities.
type silentProvider struct{ ModelProvider }

func TestCapabilities_MaxOutputTokensPresent(t *testing.T) {
	// Every built-in transport must name a default so the runtime can
	// reserve reply space without provider-specific branches.
	for _, typeID := range DefaultFactories().Types() {
		caps := CapabilitiesFor(typeID)
		if caps.MaxOutputTokens <= 0 {
			t.Errorf("transport %q MaxOutputTokens = %d, want > 0", typeID, caps.MaxOutputTokens)
		}
	}
}
