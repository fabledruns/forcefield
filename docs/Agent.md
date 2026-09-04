# Agent

Package: `internal/agent`

The `agent` package defines the agent identity that Forcefield sends to a model. An agent has a name, a base system prompt, a skill catalog, and optional project memory. The package builds the final system prompt from these parts plus a runtime operating contract.

## Purpose

The agent package does one job: it builds the system prompt for a model run. It does not talk to providers, load skills from disk, or execute tools.

## Types

### `Agent`

| Field          | Type     | Description                                      |
| -------------- | -------- | ------------------------------------------------ |
| `Name`         | `string` | The display name of the agent.                   |
| `SystemPrompt` | `string` | The base instructions for the agent.             |
| `SkillCatalog` | `string` | A formatted list of available skill catalog entries. |

## Functions

### `New`

```go
func New(name, systemPrompt, skillCatalog string) *Agent
```

Creates an `Agent`. The function trims space from the system prompt and the skill catalog.

### `BuildSystemPrompt`

```go
func (a *Agent) BuildSystemPrompt() string
```

Returns the full system prompt that the runtime sends to the model.

Behavior:

1. The function always starts with the base system prompt from configuration (agent identity).
2. It always appends the operating contract. That contract is versioned with the binary; user config cannot remove it.
3. If project memory is set, the function appends a Project Memory section.
4. If the skill catalog is not empty, the function appends a skill catalog section that tells the model which skills exist and how to load them with the `load_skill` tool.
5. The runtime may further append a Current Task State digest each turn. That is not part of this package.

## How the Runtime Uses the Agent

1. The runtime loads the configuration.
2. The runtime builds a skill catalog from the skill store.
3. The runtime creates an `Agent` with `agent.New`.
4. Before each model turn, the runtime calls `BuildSystemPrompt`.
5. The runtime places the result in a system message at the start of the message list.

## Specialised Agents

Forcefield supports multiple specialised agents (`coding, cyber, legal, docs, research, devops, general`). Each has its own `Definition` (name, description, system prompt, tool set, optional provider/model hints) and is held in an instance-scoped `Registry`. See [Agents](Agents.md) for the full registry, tool matrix, and selection.

## Design Notes

- The agent does not load skill bodies. It only holds catalog text.
- The model must call `load_skill` to read the full content of a skill.
- You can replace the base system prompt (identity) in the configuration file without changing agent code. The operating contract still applies.
- The operating contract is now shared across all specialised agents; domain identity lives in each `Definition.SystemPrompt`.
