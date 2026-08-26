package config

// Provider metadata mirroring the built-in catalog in
// internal/providers/catalog.go. Keep the entries in sync: the catalog
// drives /provider and /model pickers and supplies defaults for providers
// entries, while this file documents and cross-checks the same facts.

#ModelInfo: {
	name:        string // friendly display name shown in the UI
	id:          string // real model ID sent to the provider API
	description?: string
}

#ProviderInfo: {
	name:             string    // friendly provider name shown in the UI
	id:               string    // provider key used in config (model.provider or providers key)
	type:             #Provider // wire protocol serving this service
	description:      string
	endpoint:         #httpURL // default endpoint adopted when switching providers
	auth_env_var?:    string   // environment variable holding the API key; absent = unauthenticated
	scope:            "local" | "cloud"
	requires_api_key: bool
	models: [...#ModelInfo]
}

providers: {
	ollama: #ProviderInfo & {
		name:        "Ollama"
		id:          "ollama"
		type:        "ollama"
		description: "Local models served by Ollama."
		endpoint:    "http://localhost:11434"
		scope:       "local"
		requires_api_key: false
		models: [{
			name:        "Ornith 9B"
			id:          "ornith:9b"
			description: "Forcefield's default local model."
		}]
	}

	lmstudio: #ProviderInfo & {
		name:        "LM Studio"
		id:          "lmstudio"
		type:        "openai-compatible"
		description: "Local models served by LM Studio."
		endpoint:    "http://localhost:1234/v1"
		scope:       "local"
		requires_api_key: false
		models: [{
			name:        "Local Model"
			id:          "local-model"
			description: "Whatever model is currently loaded in LM Studio."
		}]
	}

	nvidia: #ProviderInfo & {
		name:        "NVIDIA NIM"
		id:          "nvidia"
		type:        "openai-compatible"
		description: "Hosted models served by NVIDIA NIM."
		endpoint:    "https://integrate.api.nvidia.com/v1"
		auth_env_var: "NVIDIA_API_KEY"
		scope:       "cloud"
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

	openai: #ProviderInfo & {
		name:        "OpenAI"
		id:          "openai"
		type:        "openai-compatible"
		description: "OpenAI's hosted models."
		endpoint:    "https://api.openai.com/v1"
		auth_env_var: "OPENAI_API_KEY"
		scope:       "cloud"
		requires_api_key: true
		models: [
			{name: "GPT-4o mini", id: "gpt-4o-mini"},
			{name: "GPT-4o", id: "gpt-4o"},
		]
	}

	anthropic: #ProviderInfo & {
		name:        "Anthropic"
		id:          "anthropic"
		type:        "anthropic"
		description: "Anthropic's Claude models."
		endpoint:    "https://api.anthropic.com"
		auth_env_var: "ANTHROPIC_API_KEY"
		scope:       "cloud"
		requires_api_key: true
		models: [
			{name: "Claude Sonnet 4.5", id: "claude-sonnet-4-5"},
			{name: "Claude Haiku 4.5", id: "claude-haiku-4-5"},
		]
	}

	gemini: #ProviderInfo & {
		name:        "Google Gemini"
		id:          "gemini"
		type:        "gemini"
		description: "Google's Gemini models."
		endpoint:    "https://generativelanguage.googleapis.com"
		auth_env_var: "GEMINI_API_KEY"
		scope:       "cloud"
		requires_api_key: true
		models: [
			{name: "Gemini 2.5 Flash", id: "gemini-2.5-flash"},
			{name: "Gemini 2.5 Pro", id: "gemini-2.5-pro"},
		]
	}

	xai: #ProviderInfo & {
		name:        "xAI"
		id:          "xai"
		type:        "openai-compatible"
		description: "xAI's Grok models."
		endpoint:    "https://api.x.ai/v1"
		auth_env_var: "XAI_API_KEY"
		scope:       "cloud"
		requires_api_key: true
		models: [
			{name: "Grok 3", id: "grok-3"},
			{name: "Grok 3 Mini", id: "grok-3-mini"},
		]
	}

	openrouter: #ProviderInfo & {
		name:        "OpenRouter"
		id:          "openrouter"
		type:        "openai-compatible"
		description: "One API across many hosted models."
		endpoint:    "https://openrouter.ai/api/v1"
		auth_env_var: "OPENROUTER_API_KEY"
		scope:       "cloud"
		requires_api_key: true
		models: [{
			name:        "Auto Router"
			id:          "openrouter/auto"
			description: "Lets OpenRouter pick the best available model."
		}]
	}

	groq: #ProviderInfo & {
		name:        "Groq"
		id:          "groq"
		type:        "openai-compatible"
		description: "Extremely fast hosted inference."
		endpoint:    "https://api.groq.com/openai/v1"
		auth_env_var: "GROQ_API_KEY"
		scope:       "cloud"
		requires_api_key: true
		models: [{name: "Llama 3.3 70B", id: "llama-3.3-70b-versatile"}]
	}

	mistral: #ProviderInfo & {
		name:        "Mistral"
		id:          "mistral"
		type:        "openai-compatible"
		description: "Mistral AI's hosted models."
		endpoint:    "https://api.mistral.ai/v1"
		auth_env_var: "MISTRAL_API_KEY"
		scope:       "cloud"
		requires_api_key: true
		models: [
			{name: "Mistral Large", id: "mistral-large-latest"},
			{name: "Mistral Small", id: "mistral-small-latest"},
		]
	}

	together: #ProviderInfo & {
		name:        "Together AI"
		id:          "together"
		type:        "openai-compatible"
		description: "Together AI's hosted open models."
		endpoint:    "https://api.together.xyz/v1"
		auth_env_var: "TOGETHER_API_KEY"
		scope:       "cloud"
		requires_api_key: true
		models: [{name: "Llama 3.3 70B Turbo", id: "meta-llama/Llama-3.3-70B-Instruct-Turbo"}]
	}
}
