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
#Provider: "ollama" | "lmstudio" | "nvidia" | "openai" | "anthropic" | "gemini" | "xai" | "openrouter" | "groq" | "mistral" | "together" | "opencode-zen" | "opencode-go" | "openai-compatible" | "openai-responses"

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

// ToolName lists the tool names a tools: override block may address.
#ToolName: "read_file" | "write_file" | "list_files" | "pwd" | "shell" | "shell_job" | "search_files" | "find_files" | "git" | "secret_scan" | "load_skill" | "update_task_state" | "add_project_memory"

// AgentConfig is the per-agent override block under agents:. Scalars are
// optional (non-empty replaces). Lists are optional (omitted keeps the
// built-in; explicit replaces, and explicit empty means "none").
// Runtime settings scope the shared run bounds to one agent; positive
// values win over the global agent.* block.
#AgentConfig: {
	description?:   string
	system_prompt?: string
	tools?: [...string]
	skills?: [...string]
	constraints?: [...string]
	provider?: string
	model?:    string

	max_iterations?:           int & > 0
	max_tool_calls?:           int & > 0
	max_consecutive_failures?: int & > 0
	context_window?:           int & > 0
	context_reserve?:          int & > 0
	max_context_messages?:     int & > 0
	context_summary?:          bool
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

		// Context-budget overrides. Omitted values resolve from the
		// provider capability table (known models) or fall back to
		// message-count windowing (unknown models).
		context_window?:       int & > 0
		context_reserve?:      int & > 0
		max_context_messages?: int & > 0
		context_summary?:      bool
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

	// Explicit project root and enforcement mode. Empty root resolves
	// to the Git top-level (else cwd); empty/omitted mode is permissive.
	workspace?: {
		root?: string // "" or omitted = auto-resolve
		mode?: "permissive" | "strict" | ""
	}

	// Local-only execution trace. Disabled unless explicitly enabled;
	// traces never leave the machine.
	tracing?: {
		enabled?: bool
		dir?:     string // "" or omitted = .forcefield/traces
	}

	// Per-tool output/execution overrides. Keys are tool names; every
	// field is optional and omitted values resolve to the tool default.
	// timeout_seconds is capped at 300 (the scheduler's hard ceiling).
	tools?: [#ToolName]: {
		max_bytes?:       int & > 0
		max_lines?:       int & > 0
		timeout_seconds?: number & >= 0 & <= 300
	}
}
