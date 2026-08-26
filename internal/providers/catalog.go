package providers

import (
	"sort"
	"strings"
)

// Preset describes one known provider service: how it is displayed, which
// wire protocol serves it, where it lives by default, and how it
// authenticates. Presets are defaults only - every field they provide can
// be overridden per configured provider in config.yaml.
//
// Services whose APIs genuinely speak the OpenAI Chat Completions protocol
// share the "openai-compatible" transport; they get their own preset so
// users write `type: openai` rather than repeating URLs and auth details,
// not because they have separate code paths.
type Preset struct {
	ID          string
	Name        string
	Description string
	// Type is the transport protocol serving this preset.
	Type       string
	BaseURL    string
	AuthEnvVar string // environment variable holding the API key; "" means unauthenticated
	Auth       AuthRequirement
	Scope      Scope
	Models     []ModelInfo
}

// Catalog lists every known provider service in display order.
var Catalog = []Preset{
	{
		ID:          "ollama",
		Name:        "Ollama",
		Description: "Local models served by Ollama.",
		Type:        "ollama",
		BaseURL:     "http://localhost:11434",
		Auth:        AuthNone,
		Scope:       ScopeLocal,
		Models: []ModelInfo{
			{Name: "Ornith 9B", ID: "ornith:9b", Description: "Forcefield's default local model."},
		},
	},
	{
		ID:          "lmstudio",
		Name:        "LM Studio",
		Description: "Local models served by LM Studio.",
		Type:        "openai-compatible",
		BaseURL:     "http://localhost:1234/v1",
		Auth:        AuthNone,
		Scope:       ScopeLocal,
		Models: []ModelInfo{
			{Name: "Local Model", ID: "local-model", Description: "Whatever model is currently loaded in LM Studio."},
		},
	},
	{
		ID:          "nvidia",
		Name:        "NVIDIA NIM",
		Description: "Hosted models served by NVIDIA NIM.",
		Type:        "openai-compatible",
		BaseURL:     "https://integrate.api.nvidia.com/v1",
		AuthEnvVar:  "NVIDIA_API_KEY",
		Auth:        AuthRequired,
		Scope:       ScopeCloud,
		Models: []ModelInfo{
			{Name: "Nemotron 3 Ultra", ID: "nvidia/nemotron-3-ultra-550b-a55b", Description: "NVIDIA's largest Nemotron model."},
			{Name: "Inkling", ID: "thinkingmachines/inkling", Description: "Inkling is a multimodal (text + image) reasoning model from Thinking Machines."},
			{Name: "GLM 5.2", ID: "z-ai/glm-5.2", Description: "GLM-5.2 is a flagship LLM for agentic workflows, coding, and long-horizon reasoning tasks."},
			{Name: "Minimax M3", ID: "minimaxai/minimax-m3", Description: "MiniMax M3 Preview is a multimodal MoE vision-language model with strong reasoning, coding, and tool-calling capabilities."},
			{Name: "DeepSeek V4 Pro", ID: "deepseek-ai/deepseek-v4-pro", Description: "DeepSeek V4 scales to 1M-token context windows with efficient MoE architecture for coding tasks."},
		},
	},
	{
		ID:          "openai",
		Name:        "OpenAI",
		Description: "OpenAI's hosted models.",
		Type:        "openai-compatible",
		BaseURL:     "https://api.openai.com/v1",
		AuthEnvVar:  "OPENAI_API_KEY",
		Auth:        AuthRequired,
		Scope:       ScopeCloud,
		Models: []ModelInfo{
			{Name: "GPT-4o mini", ID: "gpt-4o-mini"},
			{Name: "GPT-4o", ID: "gpt-4o"},
		},
	},
	{
		ID:          "anthropic",
		Name:        "Anthropic",
		Description: "Anthropic's Claude models.",
		Type:        "anthropic",
		BaseURL:     "https://api.anthropic.com",
		AuthEnvVar:  "ANTHROPIC_API_KEY",
		Auth:        AuthRequired,
		Scope:       ScopeCloud,
		Models: []ModelInfo{
			{Name: "Claude Sonnet 4.5", ID: "claude-sonnet-4-5"},
			{Name: "Claude Haiku 4.5", ID: "claude-haiku-4-5"},
		},
	},
	{
		ID:          "gemini",
		Name:        "Google Gemini",
		Description: "Google's Gemini models.",
		Type:        "gemini",
		BaseURL:     "https://generativelanguage.googleapis.com",
		AuthEnvVar:  "GEMINI_API_KEY",
		Auth:        AuthRequired,
		Scope:       ScopeCloud,
		Models: []ModelInfo{
			{Name: "Gemini 2.5 Flash", ID: "gemini-2.5-flash"},
			{Name: "Gemini 2.5 Pro", ID: "gemini-2.5-pro"},
		},
	},
	{
		ID:          "xai",
		Name:        "xAI",
		Description: "xAI's Grok models.",
		Type:        "openai-compatible",
		BaseURL:     "https://api.x.ai/v1",
		AuthEnvVar:  "XAI_API_KEY",
		Auth:        AuthRequired,
		Scope:       ScopeCloud,
		Models: []ModelInfo{
			{Name: "Grok 3", ID: "grok-3"},
			{Name: "Grok 3 Mini", ID: "grok-3-mini"},
		},
	},
	{
		ID:          "openrouter",
		Name:        "OpenRouter",
		Description: "One API across many hosted models.",
		Type:        "openai-compatible",
		BaseURL:     "https://openrouter.ai/api/v1",
		AuthEnvVar:  "OPENROUTER_API_KEY",
		Auth:        AuthRequired,
		Scope:       ScopeCloud,
		Models: []ModelInfo{
			{Name: "Auto Router", ID: "openrouter/auto", Description: "Lets OpenRouter pick the best available model."},
		},
	},
	{
		ID:          "groq",
		Name:        "Groq",
		Description: "Extremely fast hosted inference.",
		Type:        "openai-compatible",
		BaseURL:     "https://api.groq.com/openai/v1",
		AuthEnvVar:  "GROQ_API_KEY",
		Auth:        AuthRequired,
		Scope:       ScopeCloud,
		Models: []ModelInfo{
			{Name: "Llama 3.3 70B", ID: "llama-3.3-70b-versatile"},
		},
	},
	{
		ID:          "mistral",
		Name:        "Mistral",
		Description: "Mistral AI's hosted models.",
		Type:        "openai-compatible",
		BaseURL:     "https://api.mistral.ai/v1",
		AuthEnvVar:  "MISTRAL_API_KEY",
		Auth:        AuthRequired,
		Scope:       ScopeCloud,
		Models: []ModelInfo{
			{Name: "Mistral Large", ID: "mistral-large-latest"},
			{Name: "Mistral Small", ID: "mistral-small-latest"},
		},
	},
	{
		ID:          "together",
		Name:        "Together AI",
		Description: "Together AI's hosted open models.",
		Type:        "openai-compatible",
		BaseURL:     "https://api.together.xyz/v1",
		AuthEnvVar:  "TOGETHER_API_KEY",
		Auth:        AuthRequired,
		Scope:       ScopeCloud,
		Models: []ModelInfo{
			{Name: "Llama 3.3 70B Turbo", ID: "meta-llama/Llama-3.3-70B-Instruct-Turbo"},
		},
	},
}

// PresetByID looks up a catalog entry by provider ID.
func PresetByID(id string) (Preset, bool) {
	for _, p := range Catalog {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// ProtocolTypes are the wire protocols Forcefield ships adapters for.
func ProtocolTypes() []string {
	return DefaultFactories().Types()
}

// IsKnownType reports whether t names either a wire protocol or a catalog
// service - both are accepted as a providers entry's type.
func IsKnownType(t string) bool {
	t = strings.ToLower(strings.TrimSpace(t))
	if DefaultFactories().HasType(t) {
		return true
	}
	_, ok := PresetByID(t)
	return ok
}

// KnownTypes lists every valid value for a providers entry type, sorted.
func KnownTypes() []string {
	out := ProtocolTypes()
	for _, p := range Catalog {
		if !contains(out, p.ID) {
			out = append(out, p.ID)
		}
	}
	sort.Strings(out)
	return out
}

// CapabilitiesFor reports what a given transport type supports by asking
// its registered adapter. Unknown types report nothing.
func CapabilitiesFor(protocolType string) Capabilities {
	spec := Spec{
		ID:      "capability-probe",
		Type:    protocolType,
		BaseURL: "http://localhost:9",
		Model:   "probe",
	}
	p, err := DefaultFactories().Create(spec)
	if err != nil {
		return Capabilities{}
	}
	if cp, ok := p.(CapabilitiesProvider); ok {
		return cp.Capabilities()
	}
	return Capabilities{}
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
