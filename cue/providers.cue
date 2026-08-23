package config

// Provider metadata mirroring internal/providers/registry.go. Keep the
// entries in sync with that registry: it drives /provider and /model
// pickers, while this file documents and cross-checks the same facts.

#ModelInfo: {
	name:        string // friendly display name shown in the UI
	id:          string // real model ID sent to the provider API
	description: string
}

#ProviderInfo: {
	name:             string    // friendly provider name shown in the UI
	id:               #Provider // matches config model.provider values
	description:      string
	endpoint:         string // default endpoint adopted when switching providers
	requires_api_key: bool
	models: [...#ModelInfo]
}

providers: {
	ollama: #ProviderInfo & {
		name:             "Ollama"
		id:               "ollama"
		description:      "Local models served by Ollama."
		endpoint:         "http://localhost:11434"
		requires_api_key: false
		models: [{
			name:        "Ornith 9B"
			id:          "ornith:9b"
			description: "Forcefield's default local model."
		}]
	}

	lmstudio: #ProviderInfo & {
		name:             "LM Studio"
		id:               "lmstudio"
		description:      "Local models served by LM Studio."
		endpoint:         "http://localhost:1234/v1"
		requires_api_key: false
		models: [{
			name:        "Local Model"
			id:          "local-model"
			description: "Whatever model is currently loaded in LM Studio."
		}]
	}

	nvidia: #ProviderInfo & {
		name:             "NVIDIA NIM"
		id:               "nvidia"
		description:      "Hosted models served by NVIDIA NIM."
		endpoint:         "https://integrate.api.nvidia.com/v1"
		requires_api_key: true
		models: [
			{
				name:        "Nemotron 3 Ultra"
				id:          "nvidia/nemotron-3-ultra-550b-a55b"
				description: "NVIDIA's largest Nemotron model."
			},
			{
				name:        "Inkling"
				id:          "thinkingmachines/inkling"
				description: "Inkling is a multimodal (text + image) reasoning model from Thinking Machines."
			},
			{
				name:        "GLM 5.2"
				id:          "z-ai/glm-5.2"
				description: "GLM-5.2 is a flagship LLM for agentic workflows, coding, and long-horizon reasoning tasks."
			},
			{
				name:        "Minimax M3"
				id:          "minimaxai/minimax-m3"
				description: "MiniMax M3 Preview is a multimodal MoE vision-language model with strong reasoning, coding, and tool-calling capabilities."
			},
			{
				name:        "DeepSeek V4 Pro"
				id:          "deepseek-ai/deepseek-v4-pro"
				description: "DeepSeek V4 scales to 1M-token context windows with efficient MoE architecture for coding tasks."
			},
		]
	}
}
