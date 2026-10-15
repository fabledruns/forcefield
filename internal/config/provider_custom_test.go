package config

import (
	"strings"
	"testing"

	"forcefield/internal/providers"
)

func TestCustomProviderFieldsResolve(t *testing.T) {
	isolateHome(t)
	body := `model:
  provider: local
  name: m1

providers:
  local:
    type: openai-compatible
    base_url: http://localhost:1234/v1
    api_path: /v1/custom/chat
    auth_mode: header
    auth_header: X-Custom-Auth
    timeout_seconds: 45
    capabilities:
      streaming: true
      tool_calling: false
    models:
      - m1
`
	writeProvidersConfig(t, body)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("local", cfg.Model.Name)
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	if resolved.ChatPath != "/v1/custom/chat" {
		t.Errorf("ChatPath = %q, want override", resolved.ChatPath)
	}
	if resolved.AuthMode != "header" || resolved.AuthHeader != "X-Custom-Auth" {
		t.Errorf("auth = %q/%q, want header/X-Custom-Auth", resolved.AuthMode, resolved.AuthHeader)
	}
	if resolved.TimeoutSeconds != 45 {
		t.Errorf("timeout = %d, want 45", resolved.TimeoutSeconds)
	}
	if resolved.CapabilitiesOverride == nil || resolved.CapabilitiesOverride.ToolCalling == nil || *resolved.CapabilitiesOverride.ToolCalling {
		t.Errorf("capabilities = %+v, want tool_calling false", resolved.CapabilitiesOverride)
	}
	spec := resolved.Spec(cfg.Model.Name)
	if spec.ChatPath != "/v1/custom/chat" || spec.TimeoutSeconds != 45 {
		t.Errorf("spec passthrough = %+v", spec)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("Spec.Validate() error = %v", err)
	}
	// Capabilities flow into the adapter.
	p, err := providers.DefaultFactories().Create(spec)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	caps := p.(providers.CapabilitiesProvider).Capabilities()
	if caps.ToolCalling {
		t.Error("adapter still reports tool calling after explicit false override")
	}
	if !caps.Streaming {
		t.Error("adapter lost streaming after unrelated override")
	}
}

func TestCustomProviderValidationRejects(t *testing.T) {
	cases := map[string]struct {
		body     string
		wantText string
	}{
		"bad api_path": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    base_url: http://h\n    api_path: nope\n",
			"api_path",
		},
		"api_path traversal": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    base_url: http://h\n    api_path: /a/../b\n",
			"..",
		},
		"bad auth_mode": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    base_url: http://h\n    auth_mode: oauth\n",
			"auth_mode",
		},
		"header without mode": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    base_url: http://h\n    auth_header: X-K\n",
			"auth_header requires",
		},
		"header mode without name": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    base_url: http://h\n    auth_mode: header\n",
			"requires auth_header",
		},
		"header reserved": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    base_url: http://h\n    auth_mode: header\n    auth_header: Authorization\n",
			"reserved",
		},
		"scheme without mode": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    base_url: http://h\n    auth_scheme: Token\n",
			"auth_scheme requires",
		},
		"timeout too big": {
			"model:\n  provider: x\n  name: m\nproviders:\n  x:\n    type: openai-compatible\n    base_url: http://h\n    timeout_seconds: 999\n",
			"timeout_seconds",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			isolateHome(t)
			writeProvidersConfig(t, tc.body)
			_, err := Load()
			if err == nil {
				t.Fatal("Load() accepted invalid custom config")
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error = %q, want %q", err, tc.wantText)
			}
		})
	}
}

func TestCustomHeaderValueCRLFRejected(t *testing.T) {
	// Header values with line breaks would enable response-splitting /
	// header injection. YAML escaping makes this awkward to express in a
	// config fixture, so validate the struct directly (the same function
	// Load calls).
	for _, v := range []string{"a\nb", "a\rb", "a\r\nb"} {
		err := validateEntry("x", ProviderConfig{
			Type:    "openai-compatible",
			BaseURL: "http://h",
			Headers: map[string]string{"X-A": v},
		})
		if err == nil {
			t.Errorf("validateEntry accepted header value %q", v)
		} else if !strings.Contains(err.Error(), "control characters") {
			t.Errorf("error = %q, want control-characters mention", err)
		}
	}
}

func TestCustomProviderBackwardCompat(t *testing.T) {
	// Configs without any custom keys keep working and resolve to
	// historic defaults: default chat path, auto auth, default timeout,
	// nil capability overrides.
	isolateHome(t)
	writeProvidersConfig(t, "model:\n  provider: openai\n  name: gpt-4o-mini\n")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	resolved, err := cfg.ResolveProvider("openai", cfg.Model.Name)
	if err != nil {
		t.Fatalf("ResolveProvider error = %v", err)
	}
	if resolved.ChatPath != "" || resolved.AuthMode != "" || resolved.TimeoutSeconds != 0 || resolved.CapabilitiesOverride != nil {
		t.Errorf("legacy resolution gained custom fields: %+v", resolved)
	}
	spec := resolved.Spec(cfg.Model.Name)
	if spec.EffectiveChatPath() != providers.DefaultChatPath {
		t.Errorf("EffectiveChatPath() = %q, want default", spec.EffectiveChatPath())
	}
	if spec.NormalizedAuthMode() != "" {
		t.Errorf("NormalizedAuthMode() = %q, want auto", spec.NormalizedAuthMode())
	}
}
