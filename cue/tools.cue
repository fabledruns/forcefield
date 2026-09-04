package config

// Tool inventory mirroring what Forcefield actually registers:
//   - filesystem + shell tools: internal/tools/builtin/builtin.go
//   - runtime-registered tools: internal/runtime/runtime.go (New)
//
// default_permission records each tool's entry in the shipped config
// template (internal/config/config.go): "ask" marks tools that need
// interactive approval by default, "allow" marks read-only/local ones.
// Keep all three sides in sync when adding a tool.

#Tool: {
	name:               string & != ""
	description:        string
	default_permission: #Permission
}

tools: [
	{
		name:               "read_file"
		description:        "Read the contents of a text file at the given path."
		default_permission: "allow"
	},
	{
		name:               "list_files"
		description:        "List the entries of a directory."
		default_permission: "allow"
	},
	{
		name:               "pwd"
		description:        "Return the current working directory of the Forcefield process."
		default_permission: "allow"
	},
	{
		name:               "search_files"
		description:        "Search file contents under a directory for a literal string or regex."
		default_permission: "allow"
	},
	{
		name:               "secret_scan"
		description:        "Defensively scan one file or inline text for hardcoded secrets (local-only)."
		default_permission: "allow"
	},
	{
		name:               "write_file"
		description:        "Create or overwrite a file with the given content."
		default_permission: "ask"
	},
	{
		name:               "shell"
		description:        "Execute a shell command through the sandbox executor and return its output and exit code."
		default_permission: "ask"
	},
	{
		name:               "load_skill"
		description:        "Load a skill's full instructions on demand from the skill store."
		default_permission: "allow"
	},
	{
		name:               "update_task_state"
		description:        "Update the agent's structured plan/blockers/discoveries for the current task."
		default_permission: "allow"
	},
	{
		name:               "add_project_memory"
		description:        "Persist a durable fact about the current project."
		default_permission: "ask"
	},
]
