package config

#Provider: "ollama" | "lmstudio" | "nvidia" | "openai" | "anthropic"

#Permission: "allow" | "deny" | "ask"

#Config: {
	model: {
		provider: #Provider
		endpoint: string
		name:     string
	}

	agent: {
		name:          string
		system_prompt: string
	}

	permissions: {
		default: #Permission

		tools?: [string]: #Permission
	}
}
