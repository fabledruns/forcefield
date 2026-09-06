# Tools

Package: `internal/tools`

The `tools` package defines the tool framework. Tools are actions that the model can request during a run. The package covers tool registration, lookup, execution, and built-in tool implementations.

## Purpose

The model provides text. Tools provide actions. When the model requests a tool, the runtime executes it and returns the result to the model.

## Core Abstractions

### `Tool`

| Method          | Description                                      |
| --------------- | ------------------------------------------------ |
| `Name()`        | Unique tool name.                                |
| `Description()` | Human-readable description for the model.        |
| `InputSchema()` | JSON-schema-like argument description.           |
| `Execute()`     | Runs the tool with a context and argument map.   |

### `Result`

| Field        | Type              | Description                                      |
| ------------ | ----------------- | ------------------------------------------------ |
| `Content`    | `string`          | Output text returned to the model.               |
| `IsError`    | `bool`            | True when the tool completed with a soft error.  |
| `ExitCode`   | `int`             | Process exit code, for tools that run commands.  |
| `Stdout`     | `string`          | Captured stdout, kept separate from stderr.      |
| `Stderr`     | `string`          | Captured stderr, kept separate from stdout.      |
| `DurationMs` | `int64`           | Execution time in milliseconds.                  |
| `Tool`       | `string`          | Tool name that produced the result.              |
| `Command`    | `string`          | Command that ran, for shell-like tools.          |
| `Metadata`   | `map[string]any`  | Structured truncation info when output was cut.  |

A soft error (`IsError: true`) is a normal result that tells the model the action failed. A hard error from `Execute` stops the runtime tool path with an execution error.

### `Definition`

A static description of a tool for the provider. It includes name, description, and input schema. It does not expose the implementation.

## Manager and Registry

### `Registry`

Stores tools by name and keeps registration order.

| Method         | Description                                      |
| -------------- | ------------------------------------------------ |
| `Register`     | Adds a tool. Fails on empty or duplicate names.  |
| `Lookup`       | Finds a tool by name.                            |
| `All`          | Returns registered tools in order.               |
| `Definitions`  | Returns provider-facing definitions.             |

### `Manager`

The entry point used by the rest of Forcefield.

| Method         | Description                                      |
| -------------- | ------------------------------------------------ |
| `Register`     | Registers a tool.                                |
| `List`         | Lists registered tools.                          |
| `Definitions`  | Returns definitions for the current model turn.  |
| `Execute`      | Looks up a tool by name and runs it.             |
| `Filtered`     | Returns a new manager exposing only named tools (same instances). |

`Execute` treats a nil argument map as empty. Tools with no arguments can run without special handling by callers.

`Filtered` rejects unknown or duplicate names. The runtime builds one full manager at startup, then derives a per-agent filtered view so the model only sees the active agent's tools and the scheduler fails closed (`tool not found`) on anything else. See [Agents](Agents.md).

## Built-in Tools

Built-in tools are registered through `tools/builtin`.

| Tool          | Package                 | Description                                      |
| ------------- | ----------------------- | ------------------------------------------------ |
| `read_file`   | `tools/filesystem`      | Read a text file. Files over 5 MiB are refused with a note. |
| `write_file`  | `tools/filesystem`      | Write content to a file.                         |
| `list_files`  | `tools/filesystem`      | List files in a directory. Max 500 entries per listing. |
| `pwd`         | `tools/shell`           | Return the current working directory.            |
| `shell`       | `tools/shell`           | Run a Bash command. Combined stdout+stderr capped at 2 MiB; 30s default timeout, per-call up to 300s. |
| `shell_job`   | `tools/shell`           | Run a Bash command in the background; poll, list, or cancel by job id. Max 4 running jobs, 1 MiB output, 300s lifetime, 10 min idle expiry. |
| `search_files`| `tools/search`          | Search file contents under a directory. Skips excluded dirs, lockfiles, sensitive and binary files; max 100 matches. |
| `find_files`  | `tools/search`          | Find files/dirs by glob or substring. Sorted workspace-relative paths; max 50 results. |
| `git`         | `tools/git`             | Inspect a git repository (read-only): status, diffs, log, changed files. Max 256 KiB output. |
| `secret_scan` | `tools/security`        | Defensively scan one file/text for hardcoded secrets (local-only, redacted output). Max 50 findings, 1 MiB input. |
| `load_skill`  | `runtime`               | Load a skill body by ID, scoped to the active agent's skill set. |

`load_skill` is registered by the runtime, not by the generic builtin package, because it needs the skill store. It refuses IDs outside the active agent's assignment with a soft error.

## Output Limits

Every tool's output is bounded in two stages so large output can never
consume the whole model context:

1. **Tool level** (`internal/tools/limits.go`): each tool caps its own
   output — combined bytes for shell, file size for reads, entry/match/
   finding counts for listings — and marks cut output with a truncation
   note the model can see, plus structured `Metadata` (`truncated`,
   sizes, limits). Shell separates stdout/stderr, always reports the
   exit code, and truncates before the runtime ever sees the bytes.
2. **Runtime context guard**: the runtime additionally caps each tool
   result at 6000 characters before sending history to the provider
   (see [Runtime](Runtime.md)).

Timeouts: every tool execution runs under a timeout (30s default; shell
`timeout_seconds` per call). The scheduler clamps everything to a 300s
hard ceiling.

Limits are configurable per tool without touching code:

```yaml
tools:
  shell:
    max_bytes: 1048576     # combined stdout+stderr cap
    timeout_seconds: 60
  search_files:
    max_lines: 40          # reported matches
```

Omitted fields resolve to the tool defaults above; unknown tool names
are rejected at load.

## Execution Flow

1. The provider streams a model turn with tool definitions.
2. The model requests one or more tool calls.
3. The runtime emits `EventToolStart`.
4. The tool manager looks up the tool and calls `Execute`.
5. The runtime emits `EventToolFinish` with the result.
6. The runtime appends a tool-result message and continues the agent loop.

## How to Add a Tool

1. Implement the `Tool` interface.
2. Register the tool on the manager during startup.
3. Keep the tool focused on one action.
4. Return soft errors in `Result` when the model should continue and adapt.

## Design Notes

- Tools are local. They run on the machine that runs Forcefield.
- The registry rejects duplicate names so tool identity stays clear.
- Argument helpers in `args.go` extract typed values from the argument map.
