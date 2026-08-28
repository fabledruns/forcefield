# Runtime

Package: `internal/runtime`

The `runtime` package is the Forcefield harness. It coordinates configuration, agents, providers, tools, skills, and sessions to process a user request and return a response.

## Purpose

The runtime contains no product logic of its own beyond ordering steps. It connects the other packages and runs the agent loop until the model finishes or an error occurs.

## Construction

```go
func New() (*Runtime, error)
```

`New` builds a ready runtime:

1. Load configuration.
2. Resolve the Forcefield home directory.
3. Build the skill store.
4. Create the agent with the base prompt and skill catalog.
5. Create the model provider.
6. Create the tool manager with built-in tools.
7. Register the `load_skill` tool.

## Public Methods

| Method              | Description                                                      |
| ------------------- | ---------------------------------------------------------------- |
| `CurrentModel`      | Returns the active model name.                                   |
| `CurrentProvider`   | Returns the active provider name.                                |
| `SetModel`          | Switches the model for the next request.                         |
| `SetProvider`       | Switches the provider for the next request.                      |
| `ProviderSummaries` | Describes every selectable provider with capabilities and availability. |
| `StreamChat`        | Runs the agent loop and emits structured events as they happen.  |
| `Stream`            | Compatibility alias for `StreamChat`.                            |
| `Run`               | Runs the agent loop and returns the final response.              |
| `RunContext`        | Same as `Run`, with caller-controlled cancellation.              |

## Provider Selection

The runtime never constructs providers directly and never branches on which provider is active. Configuration resolves to a `Spec`; the providers factory registry builds the matching adapter; the loop runs against `providers.ModelProvider`. Switching provider or model takes effect on the next request without a restart, and streaming, tool calling, sessions, and cancellation behave identically across transports.

If the active provider requires an API key that is missing, startup still succeeds; the model turn fails with an error naming the variable to set.

## Events

`StreamChat` emits `Event` values on a channel.

| Event type        | Meaning                                              |
| ----------------- | ---------------------------------------------------- |
| `EventThinking`   | The model is thinking or a turn has started.         |
| `EventText`       | Incremental assistant text.                          |
| `EventToolStart`  | A tool call is about to run.                         |
| `EventToolFinish` | A tool call finished with a result or error.         |
| `EventDone`       | The run completed with a final response.             |
| `EventError`      | The run stopped because of an error.                 |

## Agent Loop

The runtime uses one loop for both streaming and non-streaming callers.

```text
build messages (system + history)
        │
        ▼
 stream one model turn
        │
        ├── text / thinking events
        │
        ├── no tool calls ──► EventDone
        │
        └── tool calls
                │
                ▼
        for each tool call
                │
                ├── EventToolStart
                ├── execute tool
                ├── EventToolFinish
                └── append tool result message
                │
                ▼
        start next model turn
```

Detailed steps:

1. Build the message list with the agent system prompt and conversation history.
2. Emit thinking, then stream one provider turn.
3. Collect text and tool calls from provider stream events.
4. If there are no tool calls, emit `EventDone` and stop.
5. If there are tool calls, append the assistant message.
6. Execute each tool through the tool manager.
7. Append each tool result as a tool message.
8. Repeat from step 2.

## Skill Loading

The runtime registers a special tool named `load_skill`.

- The agent system prompt includes only the skill catalog.
- When the model needs full skill instructions, it calls `load_skill` with a skill ID.
- The tool reads the skill body from the in-memory skill store.
- No full scan of the skills directory happens during tool execution.

## Entry Points

| Entry point              | Use                                              |
| ------------------------ | ------------------------------------------------ |
| `ff` or `ff chat`        | Interactive TUI, which streams runtime events.   |
| `ff run [task]`          | One-shot prompt through `runtime.Run`.           |
| `runtime.Run(messages)`  | Convenience helper that creates a runtime and runs once. |

## Design Notes

- The runtime owns the multi-turn tool loop. Providers stream only one turn.
- Model and provider switches (`SetModel`/`SetProvider`) take effect on the next request and are in-memory only (temporary) — they do not write `config.yaml` unless `SaveConfig` is called explicitly. See `docs/Config.md`.
- Cancellation through context stops emission and ends the run cleanly when possible.
