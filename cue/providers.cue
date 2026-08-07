package config

#ProviderInfo: {
	name:             string
	endpoint:         string
	requires_api_key: bool
}

providers: {
	ollama: #ProviderInfo & {
		name:             "Ollama"
		endpoint:         "http://localhost:11434"
		requires_api_key: false
	}

	lmstudio: #ProviderInfo & {
		name:             "LM Studio"
		endpoint:         "http://localhost:1234/v1"
		requires_api_key: false
	}

	nvidia: #ProviderInfo & {
		name:             "NVIDIA NIM"
		endpoint:         "https://integrate.api.nvidia.com/v1"
		requires_api_key: true
	}
}
