package providers

import (
	"net/http"
	"strings"
	"time"
)

// This file keeps the historical NVIDIA NIM and LM Studio constructors
// working on top of the shared OpenAI-compatible transport. Both services
// speak that wire protocol; only defaults and error wording differ.

// defaultNvidiaTimeout bounds how long the client will wait for the first
// response headers from NVIDIA NIM. Kimi K3 on NIM can take >60s to start
// streaming reasoning (TTFB), but must not be treated as unreachable.
// The body is streamed without an overall timeout; only the header phase is
// bounded. 300s is generous for Kimi K3's slow TTFB while still failing
// fast on dead endpoints.
const defaultNvidiaTimeout = 300 * time.Second

// NvidiaProvider is the OpenAI-compatible transport configured for
// NVIDIA NIM. It exists so existing callers and tests keep compiling;
// new code should construct OpenAICompatible from a resolved spec.
type NvidiaProvider = OpenAICompatible

// NewNvidiaProvider builds a provider pointed at the given endpoint
// (e.g. "https://integrate.api.nvidia.com/v1"), model name, and API key.
// If client is nil, a default client is used.
func NewNvidiaProvider(endpoint, model, apiKey string, client *http.Client) *NvidiaProvider {
	p := NewOpenAICompatible(Spec{
		ID:      "nvidia",
		Type:    "openai-compatible",
		Label:   "NVIDIA NIM",
		BaseURL: endpoint,
		APIKey:  apiKey,
		Model:   model,
	})
	if client != nil {
		p.client = client
	} else {
		timeout := defaultStreamTimeout
		if strings.Contains(strings.ToLower(model), "kimi") {
			timeout = defaultNvidiaTimeout
		}
		p.client = &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: timeout,
			},
		}
	}
	p.authHintEnv = "NVIDIA_API_KEY"
	// Ask NIM to actually stream reasoning for models that support thinking.
	// For models where thinking is not a separate capability (e.g., DeepSeek
	// V4 Flash where none is an effort level), do not send enable_thinking
	// by default. Models that don't recognize the field ignore it, but we
	// remain conservative and only send when capability indicates support.
	if caps := ModelReasoningCapabilities("nvidia", model); caps.SupportsThinking() {
		p.extraBody = map[string]any{
			"chat_template_kwargs": map[string]any{
				"enable_thinking": true,
				"clear_thinking":  false,
			},
		}
	}
	return p
}

// NewLMStudioProvider builds a provider for LM Studio's local server,
// which exposes the same OpenAI-compatible chat completions API as NVIDIA
// NIM but is unauthenticated. Errors are worded for LM Studio so a user
// running locally is never told to go debug "NVIDIA NIM".
func NewLMStudioProvider(endpoint, model string) *NvidiaProvider {
	p := NewNvidiaProvider(endpoint, model, "", nil)
	p.spec.Label = "LM Studio"
	p.authHintEnv = ""
	p.noKeyHint = "LM Studio does not need an API key - make sure this endpoint points at an actual LM Studio server"
	p.extraBody = nil
	return p
}
