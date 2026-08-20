package providers

// ModelInfo describes one selectable model: the friendly name shown in
// the UI and the real model ID sent to the provider's API. The UI must
// only ever display Name; ID is what actually gets stored in config and
// sent over the wire.
type ModelInfo struct {
	Name        string
	ID          string
	Description string
}

// ProviderInfo describes one selectable provider and the models known
// to be available on it.
type ProviderInfo struct {
	Name        string
	ID          string
	Description string
	// Endpoint is the default endpoint used when switching to this
	// provider, so picking a provider doesn't require also knowing its
	// URL.
	Endpoint string
	Models   []ModelInfo
}

// Registry lists every provider Forcefield knows how to talk to, in the
// order they're presented in the /provider picker. Adding a provider or
// model means editing this list only — nothing else in the command or
// TUI layers hardcodes provider or model names.
var Registry = []ProviderInfo{
	{
		Name:        "Ollama",
		ID:          "ollama",
		Description: "Local models served by Ollama.",
		Endpoint:    "http://localhost:11434",
		Models: []ModelInfo{
			{Name: "Ornith 9B", ID: "ornith:9b", Description: "Forcefield's default local model."},
		},
	},
	{
		Name:        "LM Studio",
		ID:          "lmstudio",
		Description: "Local models served by LM Studio.",
		Endpoint:    "http://localhost:1234/v1",
		Models: []ModelInfo{
			{Name: "Local Model", ID: "local-model", Description: "Whatever model is currently loaded in LM Studio."},
		},
	},
	{
		Name:        "NVIDIA NIM",
		ID:          "nvidia",
		Description: "Hosted models served by NVIDIA NIM.",
		Endpoint:    "https://integrate.api.nvidia.com/v1",
		Models: []ModelInfo{
			{Name: "Nemotron 3 Ultra", ID: "nvidia/nemotron-3-ultra-550b-a55b", Description: "NVIDIA's largest Nemotron model."},
			{Name: "Inkling", ID: "thinkingmachines/inkling", Description: "Inkling is a multimodal (text + image) reasoning model from Thinking Machines."},
			{Name: "GLM 5.2", ID: "z-ai/glm-5.2", Description: "GLM-5.2 is a flagship LLM for agentic workflows, coding, and long-horizon reasoning tasks."},
			{Name: "Minimax M3", ID: "minimaxai/minimax-m3", Description: "MiniMax M3 Preview is a multimodal MoE vision-language model with strong reasoning, coding, and tool-calling capabilities."},
			{Name: "DeepSeek V4 Pro", ID: "deepseek-ai/deepseek-v4-pro", Description: "DeepSeek V4 scales to 1M-token context windows with efficient MoE architecture for coding tasks."},
		},
	},
}

// ByID looks up a provider by ID.
func ByID(id string) (ProviderInfo, bool) {
	for _, p := range Registry {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderInfo{}, false
}

// ModelByID looks up a model within a provider by its real model ID.
func (p ProviderInfo) ModelByID(id string) (ModelInfo, bool) {
	for _, m := range p.Models {
		if m.ID == id {
			return m, true
		}
	}
	return ModelInfo{}, false
}

// DisplayName returns a provider's friendly name, or its ID if unknown.
func DisplayName(providerID string) string {
	if p, ok := ByID(providerID); ok {
		return p.Name
	}
	return providerID
}

// ModelDisplayName returns a model's friendly name, or its ID if unknown.
func ModelDisplayName(providerID, modelID string) string {
	if p, ok := ByID(providerID); ok {
		if m, ok := p.ModelByID(modelID); ok {
			return m.Name
		}
	}
	return modelID
}
