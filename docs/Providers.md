# Providers

Package: `internal/providers`

The `providers` package defines the model provider interface and its implementations. A provider sends messages to a language model and streams the reply.

## Purpose

Forcefield talks to models through one interface. The runtime owns the agent loop. Each provider only streams one model turn. This design lets you add a second provider later without changing agent or runtime code.

## Core Types

### `ModelProvider`

```go
type ModelProvider interface {
    StreamChat(
        ctx context.Context,
        messages []Message,
        tools []tools.Definition,
    ) (<-chan StreamEvent, error)
}
```

| Parameter  | Description                                              |
| ---------- | -------------------------------------------------------- |
| `ctx`      | Cancellation and deadline control.                       |
| `messages` | Conversation history, including system and tool messages.|
| `tools`    | Tool definitions available for this model turn.          |

The channel emits `StreamEvent` values until the model turn ends or an error occurs.

### `Message`

A single conversation message.

| Field        | Description                                      |
| ------------ | ------------------------------------------------ |
| `Role`       | `system`, `user`, `assistant`, or `tool`.        |
| `Content`    | Text content of the message.                     |
| `ToolCalls`  | Tool calls requested by an assistant message.    |
| `ToolCallID` | ID that links a tool result to a tool call.      |
| `Name`       | Tool name for tool-result messages.              |

### `ToolCall`

| Field       | Description                          |
| ----------- | ------------------------------------ |
| `ID`        | Unique ID for the tool call.         |
| `Name`      | Name of the tool to run.             |
| `Arguments` | Argument map for the tool.           |

### `Response`

| Field       | Description                                      |
| ----------- | ------------------------------------------------ |
| `Content`   | Full text assembled from streamed chunks.        |
| `ToolCalls` | Tool calls collected during the model turn.      |

### `StreamEvent`

One piece of a streaming model turn.

| Field       | Description                                      |
| ----------- | ------------------------------------------------ |
| `Text`      | Incremental text from the model.                 |
| `Thinking`  | Optional thinking content from the model.        |
| `ToolCalls` | Tool calls reported in this chunk.               |
| `Done`      | True when the provider marks the turn complete.  |
| `Err`       | Non-nil if the stream failed.                    |

## Supported Implementation

### Ollama

Type: `OllamaProvider`

Ollama is the only provider in this prototype.

| Field      | Description                                      |
| ---------- | ------------------------------------------------ |
| `Endpoint` | Ollama base URL, for example `http://localhost:11434`. |
| `Model`    | Model name, for example `ornith:9b`.             |

`StreamChat` posts to the Ollama `/api/chat` endpoint with streaming enabled. It converts Forcefield messages and tool definitions into the Ollama request shape, then maps each response chunk to a `StreamEvent`.

## How the Runtime Uses a Provider

1. The runtime selects a provider from configuration.
2. The runtime builds the message list, including the system prompt.
3. The runtime calls `StreamChat` with current tool definitions.
4. The runtime assembles text and tool calls from stream events.
5. If the model requests tools, the runtime executes them and starts another model turn.

## Design Notes

- Providers do not execute tools. The runtime executes tools.
- Providers do not manage sessions. The session package stores history.
- Unsupported provider names fail fast with a clear error.
