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

// Provider lists the model providers the runtime can construct. Anything
// else fails in runtime.newProvider ("unsupported model provider").
#Provider: "ollama" | "lmstudio" | "nvidia"

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

// Config is the top-level shape of config.yaml. Only model.* is strictly
// required: every other section is tolerated when absent, mirroring
// config.Load.
#Config: {
	model!: {
		provider!: #Provider
		endpoint!: #nonEmpty // e.g. http://localhost:11434
		name!:     #nonEmpty // e.g. ornith:9b
	}

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
