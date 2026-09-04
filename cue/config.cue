// Package config holds the CUE schema for ~/.forcefield/config.yaml.
//
// Keep this file in sync with internal/config/config.go, which owns the
// authoritative parsing and validation. The rules below mirror what Load
// accepts at runtime, so `cue vet . <config.yaml> -d '#Config'` rejects
// exactly what Forcefield would reject.
//
// Validate a config file against this schema:
//
//	cue vet . path/to/config.yaml -d '#Config' -c
package config

// Provider lists the values accepted for a providers entry's type:
// either a wire protocol Forcefield ships adapters for, or a known
// service whose defaults (endpoint, auth variable) are built in.
#Provider: "ollama" | "lmstudio" | "nvidia" | "openai" | "anthropic" | "gemini" | "xai" | "openrouter" | "groq" | "mistral" | "together" | "openai-compatible"

// Permission values for permissions.default and every permissions.tools
// entry. "" is accepted everywhere and means "unset behaves like ask",
// matching validatePermissionValue.
#Permission: "allow" | "deny" | "ask" | ""

// SandboxMode selects the shell execution boundary.
// "" means native (historical behavior, no isolation).
// See internal/sandbox and docs/Sandbox.md for exact guarantees.
#SandboxMode: "native" | "wsl" | ""

// NetworkPolicy is the WSL sandbox network request.
// "" means disabled (fail closed when isolation cannot be established).
#NetworkPolicy: "disabled" | "host" | ""

// nonEmpty constrains strings the runtime requires to be present.
#nonEmpty: string & != ""
#httpURL: string & =~ "^https?://.+"

// AgentName lists the built-in specialised agents recognised by the registry.
#AgentName: "coding" | "cyber" | "legal" | "docs" | "research" | "devops" | "general"

// AgentConfig is the per-agent override block under agents:. Every field is
// optional; only non-empty values replace the built-in definition.
#AgentConfig: {
	description?:   string
	system_prompt?: string
	tools?: [...string]
	provider?: string
	model?:    string
}

// ProviderEntry is one section under providers:. Every field is optional;
// omitted fields fall back to the service's catalog defaults. Secrets are
// never stored here - api_key_env names an environment variable or .env
// key instead, so a saved config.yaml can never contain credentials.
#ProviderEntry: {
	type?:       #Provider
	base_url?:   #httpURL
	api_key_env?: string
	model?:      string
	headers?:    [string]: string
	models?:     [...#nonEmpty]
}

// Config is the top-level shape of config.yaml. model.provider and
// model.name are strictly required; endpoint is optional when the active
// provider has catalog defaults; every other section is tolerated when
// absent, mirroring config.Load.
#Config: {
	model!: {
		provider!: #nonEmpty // e.g. ollama, openai, or a configured providers key
		endpoint?: #httpURL  // e.g. http://localhost:11434; optional with catalog defaults
		name!:     #nonEmpty // e.g. ornith:9b
	}

	providers?: [string]: #ProviderEntry

	agent?: {
		name?:          string
		system_prompt?: string

		// Long-horizon run limits. Zero/omitted values fall back to
		// runtime.DefaultLimits; negative values are meaningless and are
		// rejected here even though the runtime merely ignores them.
		max_iterations?:           int & > 0
		max_tool_calls?:           int & > 0
		max_consecutive_failures?: int & > 0
	}

	agents?: [#AgentName]: #AgentConfig

	permissions?: {
		default?: #Permission
		tools?: [string]: #Permission
	}

	sandbox?: {
		mode?: #SandboxMode
		wsl?: {
			distribution?: string // "" or omitted = system default distribution
			network?:      #NetworkPolicy
		}
	}
}
