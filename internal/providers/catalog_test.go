package providers

import (
	"context"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

func TestPresetByID(t *testing.T) {
	for _, id := range []string{"ollama", "lmstudio", "nvidia", "openai", "anthropic", "gemini", "xai", "openrouter", "groq", "mistral", "together", "opencode-zen", "opencode-go"} {
		if _, ok := PresetByID(id); !ok {
			t.Errorf("PresetByID(%q) missing from catalog", id)
		}
	}
	if _, ok := PresetByID("nope"); ok {
		t.Error("PresetByID(nope) succeeded")
	}
}

func TestOpenAICompatibleServicesShareTransport(t *testing.T) {
	for _, id := range []string{"lmstudio", "nvidia", "openai", "xai", "openrouter", "groq", "mistral", "together"} {
		preset, ok := PresetByID(id)
		if !ok {
			t.Fatalf("%q missing", id)
		}
		if preset.Type != "openai-compatible" {
			t.Errorf("%s type = %q, want the shared OpenAI-compatible transport", id, preset.Type)
		}
	}
}

func TestIsKnownType(t *testing.T) {
	for _, known := range []string{"ollama", "openai-compatible", "anthropic", "gemini", "openai", "xai"} {
		if !IsKnownType(known) {
			t.Errorf("IsKnownType(%q) = false, want true", known)
		}
	}
	if IsKnownType("telegram") || IsKnownType("") {
		t.Error("IsKnownType accepted an unknown or empty type")
	}
}

func TestFactoryRegistryCreateAndErrors(t *testing.T) {
	reg := NewFactoryRegistry()
	called := ""
	if err := reg.Register("fake", func(spec Spec) (ModelProvider, error) {
		called = spec.ID
		return &scriptedTestProvider{}, nil
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// Duplicate registration must fail.
	if err := reg.Register("fake", func(Spec) (ModelProvider, error) { return nil, nil }); err == nil {
		t.Fatal("duplicate Register() succeeded")
	}

	p, err := reg.Create(Spec{ID: "x", Type: "fake", BaseURL: "http://localhost:1", Model: "m"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if called != "x" {
		t.Errorf("factory saw ID %q, want x", called)
	}
	if p == nil {
		t.Fatal("Create() returned nil provider")
	}

	if _, err := reg.Create(Spec{ID: "x", Type: "unknown", BaseURL: "http://localhost:1", Model: "m"}); err == nil {
		t.Fatal("Create() with unknown type succeeded")
	}
}

type scriptedTestProvider struct{}

func (scriptedTestProvider) StreamChat(_ context.Context, _ []Message, _ []tools.Definition) (<-chan StreamEvent, error) {
	return nil, nil
}

func TestDefaultFactoriesCoverShippedProtocols(t *testing.T) {
	types := DefaultFactories().Types()
	want := map[string]bool{"ollama": false, "openai-compatible": false, "anthropic": false, "gemini": false}
	for _, typ := range types {
		if _, ok := want[typ]; ok {
			want[typ] = true
		}
	}
	for typ, seen := range want {
		if !seen {
			t.Errorf("default factories missing %q (have %v)", typ, types)
		}
	}
}

func TestSpecValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Spec)
		wantErr string
	}{
		{"valid", func(*Spec) {}, ""},
		{"missing type", func(s *Spec) { s.Type = "" }, "no type"},
		{"missing base url", func(s *Spec) { s.BaseURL = "" }, "no base_url"},
		{"bad scheme", func(s *Spec) { s.BaseURL = "ftp://x" }, "http"},
		{"missing host", func(s *Spec) { s.BaseURL = "http://" }, "host"},
		{"bad header name", func(s *Spec) { s.Headers = map[string]string{"Bad Header": "v"} }, "header name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := Spec{ID: "s", Type: "openai-compatible", BaseURL: "http://localhost:1", Model: "m"}
			tc.mutate(&spec)
			err := spec.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestCapabilitiesForUnknownTypeIsEmpty(t *testing.T) {
	if caps := CapabilitiesFor("nonexistent-protocol"); caps.Streaming {
		t.Errorf("unknown type capabilities = %+v, want zero value", caps)
	}
}

func TestRegistryDerivedFromCatalog(t *testing.T) {
	if len(Registry) != len(Catalog) {
		t.Fatalf("Registry has %d entries for %d presets", len(Registry), len(Catalog))
	}
	for i, info := range Registry {
		preset := Catalog[i]
		if info.ID != preset.ID || info.Endpoint != preset.BaseURL {
			t.Errorf("Registry[%d] = %+v, want derived from preset %q", i, info, preset.ID)
		}
		if !info.Capabilities.Streaming {
			t.Errorf("%s capabilities = %+v, want streaming", info.ID, info.Capabilities)
		}
	}
}

func TestDisplayNameFallbacks(t *testing.T) {
	if got := DisplayName("ollama"); got != "Ollama" {
		t.Errorf("DisplayName(ollama) = %q", got)
	}
	if got := DisplayName("custom-thing"); got != "custom-thing" {
		t.Errorf("DisplayName fallback = %q, want the raw id", got)
	}
	if got := ModelDisplayName("nvidia", "z-ai/glm-5.2"); got != "GLM 5.2" {
		t.Errorf("ModelDisplayName = %q, want GLM 5.2", got)
	}
	if got := ModelDisplayName("nvidia", "unknown-model"); got != "unknown-model" {
		t.Errorf("ModelDisplayName fallback = %q", got)
	}
}
