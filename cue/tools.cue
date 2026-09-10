package config

// Tool inventory mirroring what Forcefield actually registers:
//   - filesystem + shell tools: internal/tools/builtin/builtin.go
//   - runtime-registered tools: internal/runtime/runtime.go (New)
//
// default_permission records each tool's effective default: either its
// entry in the shipped config template (internal/config/config.go) or,
// for tools with no template entry (load_skill, update_task_state),
// the template's permissions.default, which is "ask". All three sides
// (template, permission resolution, this file) must agree that those
// two resolve to "ask": they stay fail-closed by default because
// load_skill injects untrusted skill Markdown into model context and
// update_task_state mutates task state. Do not flip them to "allow"
// here without also adding explicit template entries and a security
// justification. Keep all three sides in sync when adding a tool.

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
		name:               "find_files"
		description:        "Find files and directories under a directory by filename glob or substring."
		default_permission: "allow"
	},
	{
		name:               "git"
		description:        "Inspect a git repository (read-only): status, diffs, log, changed files."
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
		name:               "shell_job"
		description:        "Run a shell command in the background and poll, list, or cancel it."
		default_permission: "ask"
	},
	{
		name:               "load_skill"
		description:        "Load a skill's full instructions on demand from the skill store."
		default_permission: "ask"
	},
	{
		name:               "update_task_state"
		description:        "Update the agent's structured plan/blockers/discoveries for the current task."
		default_permission: "ask"
	},
	{
		name:               "add_project_memory"
		description:        "Persist a durable fact about the current project."
		default_permission: "ask"
	},
]
