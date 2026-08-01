# Agent

Package: `internal/agent`

The `agent` package defines the agent identity that Forcefield sends to a model. An agent has a name, a base system prompt, and a skill catalog. The package builds the final system prompt from these parts.

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

1. If the skill catalog is empty, the function returns only the base system prompt.
2. If the skill catalog is not empty, the function appends a skill catalog section.
3. The skill catalog section tells the model which skills exist and how to load them with the `load_skill` tool.

## How the Runtime Uses the Agent

1. The runtime loads the configuration.
2. The runtime builds a skill catalog from the skill store.
3. The runtime creates an `Agent` with `agent.New`.
4. Before each model turn, the runtime calls `BuildSystemPrompt`.
5. The runtime places the result in a system message at the start of the message list.

## Design Notes

- The agent does not load skill bodies. It only holds catalog text.
- The model must call `load_skill` to read the full content of a skill.
- You can replace the base system prompt in the configuration file without changing agent code.
