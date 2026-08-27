# Providers

Package: `internal/providers`

The `providers` package defines the model provider contract, its adapters, the service catalog, and the factory registry that binds them together. A provider sends messages to a language model and streams the reply for one model turn.

## Purpose

Forcefield talks to models through one interface. The runtime owns the agent loop. Each provider only streams one model turn. Provider-specific protocol knowledge lives inside adapters; nothing in the runtime or TUI branches on which provider is active.

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

The channel emits `StreamEvent` values until the model turn ends or an error occurs. The channel always closes.

### `Message`

A single conversation message. This is Forcefield's internal representation - it is not tied to any vendor's wire format.

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

| Field         | Description                                      |
| ------------- | ------------------------------------------------ |
| `Content`     | Full text assembled from streamed chunks.        |
| `ToolCalls`   | Tool calls collected during the model turn.      |
| `StopReason`  | Normalized end-of-turn reason (`stop`, `tool_calls`, `length`). |
| `Usage`       | Token counts (`PromptTokens`, `CompletionTokens`, `TotalTokens`) when reported. |

### `StreamEvent`

One piece of a streaming model turn.

| Field        | Description                                      |
| ------------ | ------------------------------------------------ |
| `Text`       | Incremental text from the model.                 |
| `Thinking`   | Optional reasoning content from the model.       |
| `ToolCalls`  | Tool calls reported in this chunk.               |
| `Done`       | True when the provider marks the turn complete.  |
| `StopReason` | Set on the final event when reported.            |
| `Usage`      | Token counts when reported.                      |
| `Err`        | Non-nil if the stream failed.                    |

## Capabilities

Every adapter reports what it supports through `Capabilities()`:

```go
type Capabilities struct {
    Streaming         bool
    ToolCalling       bool
    Vision            bool
    StructuredOutput  bool
    Reasoning         bool
    ParallelToolCalls bool
    ContextWindow     int // tokens; 0 = unknown
}
```

Capabilities are explicit metadata: pickers render them ("local · tools · streaming"), and future runtime features can gate on them instead of asking which provider is configured. No current adapter claims vision because no Forcefield message can carry image content yet; the field exists so that support can be added honestly later.

Model discovery is optional. Adapters may implement:

```go
type ModelLister interface {
    ListModels(ctx context.Context) ([]ModelInfo, error)
}
```

## Model Discovery

Automatic model discovery lets Forcefield populate `/model` with what a provider currently serves, without editing configuration.

### Flow

```text
config providers entry → resolved Spec
        → factory registry builds that transport's adapter
        → ListModels()
        → Discovery service (single-flight, cache)
        → runtime ModelCatalog (ordering, de-duplication)
        → TUI model picker
```

The TUI contains no provider-specific logic: it consumes generic `providers.Model` values (`ID`, `Name`, `Provider`, `ContextWindow`) and a three-state listing (`fresh`, `stale`, `unsupported`).

### Supported discovery protocols

| Transport   | Endpoint                          | Notes                                                        |
| ----------- | --------------------------------- | ------------------------------------------------------------ |
| Ollama      | `GET /api/tags`                   | Locally installed models; unavailable server is a soft failure. |
| OpenAI-compatible | `GET {base_url}/models`     | One implementation for OpenAI, NVIDIA NIM, LM Studio, xAI, OpenRouter, Groq, Mistral, Together AI, and custom endpoints - auth and custom headers come from configuration. |
| Anthropic   | `GET /v1/models`                  | Follows cursor pagination (`has_more` / `after_id`) to the last page. |
| Gemini      | `GET /v1beta/models`              | Key rides the `x-goog-api-key` header, never the URL; IDs normalized (`models/` prefix stripped). |

Gemini additionally filters using `supportedGenerationMethods`: models explicitly reporting an empty method list or lacking `generateContent` are hidden. That is protocol-level evidence; nothing is guessed, and models with no methods field at all are kept.

### Caching

Successful listings live in an in-memory cache for the process lifetime:

- TTL is 10 minutes by default.
- Keys combine provider ID, transport type, base URL, and an irreversible SHA-256 fingerprint of the API key - so distinct accounts on one endpoint keep separate listings while raw keys never appear in keys, logs, or errors.
- Concurrent requests for the same key share a single fetch (single-flight).
- A failed refresh keeps the previous cached listing.

There is deliberately no disk persistence: discovery reflects live provider account state, and config/session storage must never hold it.

### Laziness and fallback

Discovery never runs at startup and `ModelCatalog` never touches the network. Fetching happens only when a model picker needs data or a refresh is requested. Until a fresh listing exists (or if discovery fails, is unsupported, or the machine is offline), the picker shows deterministic fallbacks: the active model first, then configured entry defaults, then catalog-known models, sorted and de-duplicated. A manually configured model always remains selectable even when absent from discovery - discovery enhances, never authorizes.

Failures surface as a concise status line in the picker (built from the typed error kinds, credential-redacted) while previously visible models stay listed.

## Transports

Forcefield ships four adapters, registered in a `FactoryRegistry` under these type names:

| Type                | Adapter             | Serves                                                              |
| ------------------- | ------------------- | ------------------------------------------------------------------- |
| `ollama`            | `OllamaProvider`    | Ollama's native `/api/chat` NDJSON protocol.                         |
| `openai-compatible` | `OpenAICompatible`  | Any OpenAI Chat Completions server (see below).                      |
| `anthropic`         | `AnthropicProvider` | Anthropic's native Messages API.                                     |
| `gemini`            | `GeminiProvider`    | Google's Generative Language API.                                    |

### OpenAI-compatible

The generic transport powers NVIDIA NIM, LM Studio, OpenAI, xAI, OpenRouter, Groq, Mistral, Together AI, and arbitrary self-hosted endpoints. It assumes only the documented protocol:

- `POST {base_url}/chat/completions` for turns (streaming and non-streaming).
- `GET {base_url}/models` for discovery.
- `Authorization: Bearer <key>` when an API key resolves; custom headers pass through unmodified.

It normalizes messy real-world behavior: reasoning deltas arrive as either `delta.reasoning_content` or `delta.reasoning`; tool calls stream as index-keyed argument fragments that are reassembled before delivery; streams that end without a finish reason still flush buffered tool calls; usage is parsed when present and ignored when not; malformed chunks surface as normalized protocol errors rather than crashes.

Service-specific request fields (such as NIM's `chat_template_kwargs`) ride an adapter-level extra-body map, not new code paths.

### Anthropic

Native Messages API client: `x-api-key` + `anthropic-version` headers, top-level `system` field, `tool_use`/`tool_result` content blocks with consecutive results merged into one user turn, `input_json_delta` fragment accumulation, `message_start`/`message_delta` usage parsing, and `stop_reason` mapping (`end_turn`→`stop`, `tool_use`→`tool_calls`, `max_tokens`→`length`).

### Gemini

Native Generative Language client: `x-goog-api-key` header (the key never appears in URLs), `systemInstruction`, user/model roles, typed parts with `functionCall`/`functionResponse`, `finishReason` mapping (`STOP`→`stop`, `MAX_TOKENS`→`length`), and `usageMetadata` parsing.

### Error normalization

Non-2xx responses become `*statusError` carrying a normalized kind; transport failures classify as connection/timeout/canceled; malformed bodies classify as protocol errors. `providers.Classify(err)` returns the kind (`ErrKindAuth`, `ErrKindRateLimit`, `ErrKindQuota`, `ErrKindServer`, `ErrKindTimeout`, `ErrKindProtocol`, ...). Every error that could echo credentials passes through redaction, so API keys never appear in messages shown to users.

Transient 429s retry with exponential backoff capped by policy, honoring `Retry-After`; quota/billing exhaustion is detected and never retried. One inference request per provider instance is allowed at a time by design.

## Service Catalog

`Catalog` describes every known service: display name, transport type, default base URL, authentication environment variable, local/cloud scope, and known models. The display registry used by pickers (`Registry`, `ByID`, `DisplayName`, `ModelDisplayName`) derives from it.

Adding a service whose API speaks a supported protocol means adding one catalog entry — no new code.

## Factory Registry

```go
registry := providers.DefaultFactories() // ollama, openai-compatible, anthropic, gemini
provider, err := registry.Create(providers.Spec{Type: "openai-compatible", BaseURL: ..., Model: ..., APIKey: ...})
```

`Register(typeID, factory)` adds transports; `Create(spec)` builds them after validating the spec's URL, scheme, and header names. The runtime requests providers exclusively through this path.

## How the Runtime Uses a Provider

1. Configuration resolves to a `Spec` (config entry + catalog defaults + environment key).
2. The registry builds the matching adapter.
3. The runtime builds the message list, including the system prompt.
4. The runtime calls `StreamChat` with current tool definitions.
5. The runtime assembles text, tool calls, usage, and stop reason from stream events.
6. If the model requests tools, the runtime executes them and starts another model turn.

Switching provider or model rebuilds step 1-2 only; the loop, sessions, and tools are untouched.

## Design Notes

- Providers do not execute tools. The runtime executes tools.
- Providers do not manage sessions. The session package stores history.
- Unknown provider types fail fast with a list of supported types.
- A missing but required API key does not stop startup; the first model turn fails with guidance naming the variable to set (`ff doctor` warns earlier).
