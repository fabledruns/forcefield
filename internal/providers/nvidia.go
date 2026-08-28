package providers

import (
	"net/http"
)

// This file keeps the historical NVIDIA NIM and LM Studio constructors
// working on top of the shared OpenAI-compatible transport. Both services
// speak that wire protocol; only defaults and error wording differ.

// defaultNvidiaTimeout bounds how long a single request to NVIDIA NIM may
// take before it's aborted. It mirrors defaultStreamTimeout so the
// historical constructor shares the same bound.
const defaultNvidiaTimeout = defaultStreamTimeout

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
		p.client = &http.Client{Timeout: defaultNvidiaTimeout}
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
