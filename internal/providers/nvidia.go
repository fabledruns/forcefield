package providers

import (
	"net/http"
)

// This file keeps the historical NVIDIA NIM and LM Studio constructors
// working on top of the shared OpenAI-compatible transport. Both services
// speak that wire protocol; only defaults and error wording differ.

// defaultNvidiaTimeout bounds how long a single request to NVIDIA NIM may
// take before it's aborted.
const defaultNvidiaTimeout = 0

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
	// Ask NIM to actually stream reasoning. Models that stream it
	// unconditionally (or don't support reasoning at all) are unaffected;
	// models that gate it behind this field (GLM-5.x and others) only emit
	// reasoning_content deltas when it's set. clear_thinking:false
	// preserves reasoning across turns for agentic/tool workflows,
	// matching NVIDIA's own documented usage. Models that don't recognize
	// the field ignore it.
	p.extraBody = map[string]any{
		"chat_template_kwargs": map[string]any{
			"enable_thinking": true,
			"clear_thinking":  false,
		},
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
