package providers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"forcefield/internal/tools"
)

// This file implements OpenCode Zen and OpenCode Go support on top of the
// existing generic transports. Both services are multi-protocol gateways:
// a single service hosts models that require different wire protocols
// (OpenAI Responses, OpenAI Chat Completions, Anthropic Messages), with
// the protocol determined per model by OpenCode's published endpoint
// tables (https://opencode.ai/docs/zen, https://opencode.ai/docs/go).
//
// The architecture stays data-driven: each service has a static model
// table mapping model IDs to protocols, and OpenCodeRouter resolves the
// active model to a protocol once at construction, then delegates the
// entire turn to the matching generic adapter. There is no per-provider
// branching anywhere else — no trial-and-error requests, and a Chat
// Completions request is never sent to a Responses-only model.
//
// Wire model IDs are the bare IDs from OpenCode's "Model ID" columns
// (e.g. "gpt-5.5", not "opencode/gpt-5.5"); the opencode/ prefix in
// OpenCode's own config format is their provider namespacing, not part
// of the API model name. This is an assumption to verify against a live
// key (see docs/Providers.md).

// opencodeProtocol names the wire protocols an OpenCode gateway serves.
// Values reuse the registered factory type names so tables stay readable.
const (
	opencodeResponses       = "openai-responses"
	opencodeChatCompletions = "openai-compatible"
	opencodeMessages        = "anthropic"
)

// opencodeModel is one curated catalog entry: display metadata plus the
// protocol serving it on that gateway.
type opencodeModel struct {
	ID          string
	Name        string
	Description string
	Protocol    string
}

// zenModels is the curated OpenCode Zen catalog: non-deprecated,
// coding-relevant models from the published endpoint table. Gemini models
// are intentionally absent — Zen serves them on per-model native
// endpoints (/v1/models/<id>) that no existing Forcefield transport
// speaks; see docs/Providers.md.
var zenModels = []opencodeModel{
	// Responses API.
	{ID: "gpt-5.5", Name: "GPT 5.5", Description: "OpenAI flagship reasoning model via Zen.", Protocol: opencodeResponses},
	{ID: "gpt-5.4", Name: "GPT 5.4", Description: "OpenAI reasoning model via Zen.", Protocol: opencodeResponses},
	{ID: "gpt-5.3-codex", Name: "GPT 5.3 Codex", Description: "OpenAI coding model via Zen.", Protocol: opencodeResponses},
	{ID: "grok-4.6", Name: "Grok 4.6", Description: "xAI reasoning model via Zen.", Protocol: opencodeResponses},
	{ID: "muse-spark-1.2", Name: "Muse Spark 1.2", Description: "Meta coding model via Zen.", Protocol: opencodeResponses},
	// Anthropic Messages API.
	{ID: "claude-opus-4-5", Name: "Claude Opus 4.5", Description: "Most capable Claude via Zen.", Protocol: opencodeMessages},
	{ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5", Description: "Balanced Claude coding model via Zen.", Protocol: opencodeMessages},
	{ID: "claude-haiku-4-5", Name: "Claude Haiku 4.5", Description: "Fast Claude model via Zen.", Protocol: opencodeMessages},
	{ID: "qwen3.7-max", Name: "Qwen3.7 Max", Description: "Qwen flagship via Zen (Anthropic protocol).", Protocol: opencodeMessages},
	// Chat Completions API.
	{ID: "glm-5.2", Name: "GLM 5.2", Description: "Zhipu flagship for agentic coding via Zen.", Protocol: opencodeChatCompletions},
	{ID: "kimi-k3", Name: "Kimi K3", Description: "Moonshot reasoning model via Zen.", Protocol: opencodeChatCompletions},
	{ID: "kimi-k2.7-code", Name: "Kimi K2.7 Code", Description: "Moonshot coding model via Zen.", Protocol: opencodeChatCompletions},
	{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", Description: "DeepSeek flagship via Zen.", Protocol: opencodeChatCompletions},
	{ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", Description: "Fast DeepSeek model via Zen.", Protocol: opencodeChatCompletions},
	{ID: "minimax-m3", Name: "MiniMax M3", Description: "MiniMax reasoning model via Zen (Chat Completions on Zen).", Protocol: opencodeChatCompletions},
	{ID: "big-pickle", Name: "Big Pickle", Description: "Free stealth model via Zen (limited time).", Protocol: opencodeChatCompletions},
}

// goModels is the curated OpenCode Go catalog from the published endpoint
// table. Note minimax-m3 diverges from Zen: it is Anthropic-protocol on
// Go but Chat Completions on Zen — which is why protocol selection is
// per (service, model), never per model alone.
var goModels = []opencodeModel{
	// Responses API.
	{ID: "grok-4.6", Name: "Grok 4.6", Description: "xAI reasoning model via Go.", Protocol: opencodeResponses},
	{ID: "gpt-5.6-luna", Name: "GPT 5.6 Luna", Description: "Efficient OpenAI model via Go.", Protocol: opencodeResponses},
	{ID: "muse-spark-1.3-contributor", Name: "Muse Spark 1.3 Contributor", Description: "Discounted Meta model via Go.", Protocol: opencodeResponses},
	{ID: "muse-spark-1.2-contributor", Name: "Muse Spark 1.2 Contributor", Description: "Discounted Meta model via Go.", Protocol: opencodeResponses},
	// Anthropic Messages API.
	{ID: "minimax-m3", Name: "MiniMax M3", Description: "MiniMax reasoning model via Go (Anthropic protocol on Go).", Protocol: opencodeMessages},
	{ID: "minimax-m2.7", Name: "MiniMax M2.7", Description: "MiniMax model via Go.", Protocol: opencodeMessages},
	{ID: "qwen3.8-max", Name: "Qwen3.8 Max", Description: "Qwen flagship via Go.", Protocol: opencodeMessages},
	{ID: "qwen3.7-max", Name: "Qwen3.7 Max", Description: "Qwen flagship via Go.", Protocol: opencodeMessages},
	{ID: "qwen3.7-plus", Name: "Qwen3.7 Plus", Description: "Efficient Qwen model via Go.", Protocol: opencodeMessages},
	// Chat Completions API.
	{ID: "glm-5.3", Name: "GLM-5.3", Description: "Zhipu flagship via Go.", Protocol: opencodeChatCompletions},
	{ID: "glm-5.2", Name: "GLM-5.2", Description: "Zhipu flagship for agentic coding via Go.", Protocol: opencodeChatCompletions},
	{ID: "kimi-k3", Name: "Kimi K3", Description: "Moonshot reasoning model via Go.", Protocol: opencodeChatCompletions},
	{ID: "kimi-k2.7-code", Name: "Kimi K2.7 Code", Description: "Moonshot coding model via Go.", Protocol: opencodeChatCompletions},
	{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", Description: "DeepSeek flagship via Go.", Protocol: opencodeChatCompletions},
	{ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", Description: "Fast DeepSeek model via Go.", Protocol: opencodeChatCompletions},
	{ID: "mimo-v2.5", Name: "MiMo-V2.5", Description: "Efficient Xiaomi model via Go.", Protocol: opencodeChatCompletions},
	{ID: "longcat-2.0", Name: "LongCat-2.0", Description: "Meituan coding model via Go.", Protocol: opencodeChatCompletions},
}

// opencodeTableForProvider returns the model table for an OpenCode
// service ID ("opencode-zen" or "opencode-go"), or nil for anything else.
func opencodeTableForProvider(providerID string) []opencodeModel {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "opencode-zen":
		return zenModels
	case "opencode-go":
		return goModels
	default:
		return nil
	}
}

// opencodeProtocolForModel resolves a model ID to its wire protocol using
// a service table. Matching is exact and case-sensitive: model IDs are
// opaque vendor strings, and fuzzy matching could route a request to the
// wrong protocol. Unknown IDs are an error, never a guess.
func opencodeProtocolForModel(table []opencodeModel, model string) (string, error) {
	for _, m := range table {
		if m.ID == model {
			return m.Protocol, nil
		}
	}
	known := make([]string, 0, len(table))
	for _, m := range table {
		known = append(known, m.ID)
	}
	sort.Strings(known)
	return "", fmt.Errorf("model %q is not in this OpenCode catalog (known: %s) - use a listed model or a direct-protocol custom provider instead", model, strings.Join(known, ", "))
}

// OpenCodeRouter serves one OpenCode gateway (Zen or Go). It resolves the
// active model to a wire protocol once at construction and delegates every
// operation to the matching generic adapter, so the rest of Forcefield
// treats it like any other single-transport provider.
type OpenCodeRouter struct {
	spec     Spec
	service  string
	protocol string
	inner    ModelProvider

	reasoning ReasoningConfig
}

// newOpenCodeRouter builds a router for service over table. An empty
// model resolves to the Chat Completions transport so capability probes
// and listings can construct the provider without a selection; any
// StreamChat with no model still fails locally before any request is
// sent. A non-empty unknown model is always an error, never a guess.
func newOpenCodeRouter(spec Spec, service string, table []opencodeModel) (*OpenCodeRouter, error) {
	protocol := opencodeChatCompletions
	if strings.TrimSpace(spec.Model) != "" {
		var err error
		protocol, err = opencodeProtocolForModel(table, spec.Model)
		if err != nil {
			return nil, err
		}
	}
	var inner ModelProvider
	switch protocol {
	case opencodeResponses:
		p := NewOpenAIResponses(spec)
		p.authHintEnv = "OPENCODE_API_KEY"
		inner = p
	case opencodeChatCompletions:
		p := NewOpenAICompatible(spec)
		p.authHintEnv = "OPENCODE_API_KEY"
		inner = p
	case opencodeMessages:
		p := NewAnthropicProvider(spec)
		p.authHintEnv = "OPENCODE_API_KEY"
		inner = p
	default:
		return nil, fmt.Errorf("model %q maps to unknown protocol %q", spec.Model, protocol)
	}
	return &OpenCodeRouter{spec: spec, service: service, protocol: protocol, inner: inner}, nil
}

// NewOpenCodeZen builds the OpenCode Zen gateway provider.
func NewOpenCodeZen(spec Spec) (*OpenCodeRouter, error) {
	return newOpenCodeRouter(spec, "OpenCode Zen", zenModels)
}

// NewOpenCodeGo builds the OpenCode Go gateway provider.
func NewOpenCodeGo(spec Spec) (*OpenCodeRouter, error) {
	return newOpenCodeRouter(spec, "OpenCode Go", goModels)
}

// Protocol reports the wire protocol resolved for the active model
// ("openai-responses", "openai-compatible", or "anthropic").
func (r *OpenCodeRouter) Protocol() string { return r.protocol }

// StreamChat delegates the turn to the resolved transport adapter. With
// no model configured it fails locally instead of sending a request the
// gateway could route unpredictably.
func (r *OpenCodeRouter) StreamChat(ctx context.Context, messages []Message, defs []tools.Definition) (<-chan StreamEvent, error) {
	if strings.TrimSpace(r.spec.Model) == "" {
		return nil, fmt.Errorf("%s: no model configured - pick a model with /model or set model.name in config.yaml", r.service)
	}
	return r.inner.StreamChat(ctx, messages, defs)
}

// Capabilities reports the union all three transports support: every
// routed model streams, calls tools (possibly in parallel), and can
// stream reasoning. Per-model reasoning controls still come from
// ModelReasoningCapabilities.
func (r *OpenCodeRouter) Capabilities() Capabilities {
	return Capabilities{
		Streaming:         true,
		ToolCalling:       true,
		Reasoning:         true,
		ParallelToolCalls: true,
	}
}

// SetReasoning forwards abstract reasoning settings to the resolved
// transport adapter.
func (r *OpenCodeRouter) SetReasoning(cfg ReasoningConfig) {
	r.reasoning = cfg
	if ra, ok := r.inner.(ReasoningAware); ok {
		ra.SetReasoning(cfg)
	}
}

// GetReasoning reports the last reasoning config set.
func (r *OpenCodeRouter) GetReasoning() ReasoningConfig { return r.reasoning }

// ListModels enumerates the gateway's models via GET /models, which both
// Zen and Go serve in the standard OpenAI list shape. It reuses the
// Chat Completions list implementation against the same spec.
func (r *OpenCodeRouter) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return NewOpenAICompatible(r.spec).ListModels(ctx)
}
