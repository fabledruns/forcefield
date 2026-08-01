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

| Field     | Type     | Description                                      |
| --------- | -------- | ------------------------------------------------ |
| `Content` | `string` | Output text returned to the model.               |
| `IsError` | `bool`   | True when the tool completed with a soft error.  |

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

`Execute` treats a nil argument map as empty. Tools with no arguments can run without special handling by callers.

## Built-in Tools

Built-in tools are registered through `tools/builtin`.

| Tool          | Package                 | Description                                      |
| ------------- | ----------------------- | ------------------------------------------------ |
| `read_file`   | `tools/filesystem`      | Read a text file. Maximum size is 5 MiB.         |
| `write_file`  | `tools/filesystem`      | Write content to a file.                         |
| `list_files`  | `tools/filesystem`      | List files in a directory.                       |
| `pwd`         | `tools/shell`           | Return the current working directory.            |
| `load_skill`  | `runtime`               | Load a skill body by ID from the skill store.    |

`load_skill` is registered by the runtime, not by the generic builtin package, because it needs the skill store.

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
