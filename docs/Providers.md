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
    MaxOutputTokens   int // tokens per turn; 0 = unknown
}
```

Capabilities are explicit metadata: pickers render them ("local · tools · streaming"), and the runtime negotiates behavior from them instead of branching on provider names. `ResolveCapabilities(provider, model)` merges instance-reported transport features with the known-model limit table; providers too old to report keep historical behavior. The runtime currently negotiates three decisions: tool definitions are withheld only when tool calling is explicitly unreported-as-absent, tool batches run sequentially without reported parallel support, and the context budget prefers reported window/reserve over table values. No current adapter claims vision because no Forcefield message can carry image content yet; the field exists so that support can be added honestly later.

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
| OpenAI-compatible | `GET {base_url}/models`     | One implementation for OpenAI, NVIDIA NIM, LM Studio, llama.cpp (llama-server), xAI, OpenRouter, Groq, Mistral, Together AI, and custom endpoints - auth and custom headers come from configuration. |
| OpenAI Responses | `GET {base_url}/models`      | Same OpenAI list shape; used by OpenCode Zen/Go discovery. |
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

Forcefield ships five adapters, registered in a `FactoryRegistry` under these type names:

| Type                | Adapter             | Serves                                                              |
| ------------------- | ------------------- | ------------------------------------------------------------------- |
| `ollama`            | `OllamaProvider`    | Ollama's native `/api/chat` NDJSON protocol.                         |
| `openai-compatible` | `OpenAICompatible`  | Any OpenAI Chat Completions server (see below).                      |
| `openai-responses`  | `OpenAIResponses`   | Any OpenAI Responses API server (`POST {base}/responses`, stateless). |
| `anthropic`         | `AnthropicProvider` | Anthropic's native Messages API.                                     |
| `gemini`            | `GeminiProvider`    | Google's Generative Language API.                                    |

Two more types exist as multi-protocol routers rather than transports (see below): `opencode-zen` and `opencode-go`.

### OpenAI Responses

The generic Responses transport powers OpenCode Zen/Go models served over `POST {base_url}/responses`, and any self-hosted Responses endpoint. Requests are stateless (`store: false`, full history every turn); function tools, parallel calls, usage, and reasoning summaries all map onto the shared `StreamEvent` vocabulary. Reasoning effort maps to `reasoning: {effort, summary: auto}`.

### Multi-protocol gateways (OpenCode)

OpenCode Zen (`https://opencode.ai/zen/v1`) and OpenCode Go (`https://opencode.ai/zen/go/v1`) each serve models over three protocols, chosen per model from OpenCode's published endpoint tables — never guessed:

| Protocol | Zen path | Go path | Example models |
| -------- | -------- | ------- | -------------- |
| `openai-responses` | `/v1/responses` | `/go/v1/responses` | GPT 5.x, Grok 4.x, Muse Spark |
| `openai-compatible` | `/v1/chat/completions` | `/go/v1/chat/completions` | GLM, Kimi, DeepSeek, MiniMax (Zen only) |
| `anthropic` | `/v1/messages` | `/go/v1/messages` | Claude, Qwen, MiniMax (Go only) |

`OpenCodeRouter` resolves the active model against a per-service table at construction and delegates the whole turn to the matching generic adapter, so unknown models fail fast with a list of known IDs instead of sending a request down the wrong protocol. Both services authenticate with `OPENCODE_API_KEY` (Bearer; `x-api-key` on the Messages path, matching Anthropic's convention). Model discovery uses each service's documented `GET /v1/models` in OpenAI list shape.

Known limitations: Zen's per-model Gemini endpoints (`/v1/models/<id>`) need admin-native Gemini protocol support and are excluded from the catalog; wire model IDs are the bare IDs from OpenCode's tables (the `opencode/` prefix in OpenCode's own config is provider namespacing, not part of the API model name) — verify against a live key if requests 404.

### OpenAI-compatible

The generic transport powers NVIDIA NIM, LM Studio, llama.cpp (llama-server), OpenAI, xAI, OpenRouter, Groq, Mistral, Together AI, and arbitrary self-hosted endpoints. It assumes only the documented protocol:

- `POST {base_url}/chat/completions` for turns (streaming and non-streaming).
- `GET {base_url}/models` for discovery.
- `Authorization: Bearer <key>` when an API key resolves; custom headers pass through unmodified.

Custom endpoints can tune this without new code:

- `api_path` overrides the chat path (default `/chat/completions`) for servers with genuine path differences. Discovery stays `GET {base_url}/models`.
- `auth_mode`: `""` (auto: bearer when a key resolves, else none), `bearer`, `none`, or `header` (custom header via `auth_header`). `auth_scheme` optionally overrides the prefix (`Bearer` by default for bearer mode, raw key for header mode).
- `timeout_seconds` overrides the response-header timeout (default 120s).
- `capabilities` forces flags (`streaming`, `tool_calling`, `reasoning`, `structured_output`, `vision`, `parallel_tool_calls`) for endpoints that do not implement every OpenAI feature. Streaming disabled fails fast with `ErrKindUnsupportedCapability`; tool definitions are withheld by the runtime when tool calling is explicitly disabled.

Redirects are same-host only (max 3, no cross-host forwarding, no https→http downgrade except loopback) so credentials never follow a redirect to an unintended host. Header values containing control characters are rejected at validation and skipped defensively at request time. Resolved API keys are validated before use: a value containing CR, LF, or other control characters fails closed (naming the variable, never the value) instead of being placed in a header. Cloud metadata endpoints (`169.254.169.254`, `metadata.google.internal`, ...) are refused at configuration, construction, and probe time unless `allow_metadata_endpoint: true` is set explicitly for that provider.

It normalizes messy real-world behavior: reasoning deltas arrive as either `delta.reasoning_content` or `delta.reasoning`; tool calls stream as index-keyed argument fragments that are reassembled before delivery; streams that end without a finish reason still flush buffered tool calls; usage is parsed when present and ignored when not; malformed chunks surface as normalized protocol errors rather than crashes.

Service-specific request fields (such as NIM's `chat_template_kwargs`) ride an adapter-level extra-body map, not new code paths.

### Vertex AI

Native Google Cloud Vertex AI support (`type: vertex`) serves Gemini models inside your own project. It speaks the same GenerateContent protocol as Gemini (contents, typed parts, `functionCall`/`functionResponse`, usage metadata, finish reasons), but against regional endpoints with OAuth credentials:

- Endpoint: `https://{location}-aiplatform.googleapis.com` derived from `location` unless `base_url` overrides it; requests go to `/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:streamGenerateContent` (`?alt=sse`) or `:generateContent`.
- Auth: Application Default Credentials only — `GOOGLE_APPLICATION_CREDENTIALS`, the `gcloud auth application-default login` well-known file, then the GCE metadata server, resolved in that order per Google's documented chain. `api_key_env` and `auth_mode`/`auth_header`/`auth_scheme`/`api_path` are rejected for vertex entries so a misconfiguration fails fast instead of being silently ignored. `credentials_file` optionally pins one service-account key to a single provider.
- Models: `gemini-2.5-flash`, `gemini-2.5-pro` (publisher `google`); full resource names (`projects/.../models/...`, e.g. tuned endpoints) are used verbatim. There is no models-listing API in this integration: `providers.<id>.models` entries and the catalog defaults populate the picker. Availability depends on the project, region, and model access — verify against a live project.
- Quota/rate-limit, timeout, retry, redaction, and redirect handling reuse the shared transport. `401/403` hints point at ADC setup; `404` names project/location/model. ADC/token failures never retry and never include credential material.

```yaml
providers:
  vertex:
    type: vertex
    project: my-gcp-project
    location: us-central1
    models:
      - gemini-2.5-flash
```

### Bedrock

Native Amazon Bedrock support (`type: bedrock`) uses the Converse API through the maintained AWS SDK for Go v2 — one integration for chat, streaming, tools, usage, and stop reasons across models:

- Operations: `Converse` (one-shot) and `ConverseStream` (turns), requiring `bedrock:InvokeModel` / `bedrock:InvokeModelWithResponseStream` IAM permissions respectively.
- Auth: the standard AWS credential chain only — environment variables, shared `~/.aws/credentials` + `config` profiles (including SSO), then EC2/ECS/Lambda metadata. Long-lived keys are never stored in Forcefield config: `api_key_env` is rejected for bedrock entries. `profile` pins one shared-config profile; otherwise `AWS_PROFILE` and the rest of the chain apply untouched.
- Region: `region` (or `AWS_REGION`/`AWS_DEFAULT_REGION`) selects the endpoint; `base_url` optionally overrides it (e.g. VPC endpoints).
- Models: base model IDs, inference-profile IDs/ARNs, or provisioned-throughput ARNs pass through verbatim. IDs and availability vary by region, account entitlement, and date, so the catalog ships no fallback list: set `providers.<id>.models` explicitly. Not every model supports Converse or every feature — an unsupported model fails fast naming the model and region.
- Mapping: `end_turn`→`stop`, `tool_use`→`tool_calls`, `max_tokens`/`model_context_window_exceeded`→`length`; `inputTokens`/`outputTokens`/`totalTokens` usage; toolUse input-JSON fragments reassembled per content block.
- Retries are Forcefield-owned: the SDK retryer is disabled (`MaxAttempts(1)` equivalent) and the shared bounded policy retries transient throttling/server/timeout/connection failures with backoff. Quota exhaustion (`ServiceQuotaExceededException` or quota wording) never retries. No blind retries after a billable stream starts.
- Errors: `AccessDeniedException` (credentials **and** model-access grants), auth-family signature/token errors, `ResourceNotFoundException` (model ID/region), `ValidationException`, throttling vs quota — each with actionable hints and no credential material in messages.

```yaml
providers:
  bedrock:
    type: bedrock
    region: us-east-1
    models:
      # Use model or inference-profile IDs entitled in your account and
      # region (Bedrock console → Model access), e.g.:
      - anthropic.claude-sonnet-4-5-20250929-v1:0
```

### Azure AI

Native Azure OpenAI / AI Foundry support (`type: azure`) speaks the deployment-scoped Chat Completions API — the same OpenAI wire protocol, with three Azure-owned differences the adapter bakes in:

- URL: `POST {base_url}/openai/deployments/{deployment}/chat/completions?api-version={ver}` where `base_url` is the resource endpoint (e.g. `https://myres.openai.azure.com`) and the deployment name **is** the configured model ID. Because deployment names are unique per resource, the catalog ships no fallback models; set `providers.<id>.models` to your deployment names.
- Auth: an API key travels in Azure's fixed `api-key` header (`api_key_env`, the default), while Microsoft Entra ID tokens travel as `auth_mode: bearer` with `api_key_env` naming the variable that holds a token (e.g. from `az account get-access-token --resource https://cognitiveservices.azure.com`; scope `https://cognitiveservices.azure.com/.default`). `auth_header`/`auth_scheme`/`api_path` are rejected so the fixed header and derived path cannot be quietly overridden. Entra tokens are used as provided — refresh them outside Forcefield (the token lifetime is about an hour).
- Versioning: every data-plane call pins `api-version` (`api_version`, default `2024-06-01`). The charset is validated so no extra query parameters can be smuggled in.
- Mapping: payloads, SSE streaming, tool-call reassembly, usage, retries, redaction, and redirects are the shared OpenAI transport's; Azure-only extras (`prompt_filter_results` chunks, `content_filter` finish reasons) parse cleanly. `401/403` hints distinguish key auth from Entra scope/role problems; `404` names the deployment-not-found semantics; `400` distinguishes content-filter blocks from api-version mismatches; `429` points at deployment quota.
- The newer OpenAI-style v1 API (`{base_url}/openai/v1/chat/completions` with the deployment in the body's `model` field) needs no adapter: configure a plain `openai-compatible` entry with that base URL.

```yaml
providers:
  azure:
    type: azure
    base_url: https://myres.openai.azure.com
    api_key_env: AZURE_OPENAI_API_KEY
    # api_version: 2024-06-01   # optional; this is the default
    models:
      # Deployment names from Azure portal → Deployments, e.g.:
      - gpt-4o-deploy
```

### Anthropic

Native Messages API client: `x-api-key` + `anthropic-version` headers, top-level `system` field, `tool_use`/`tool_result` content blocks with consecutive results merged into one user turn, `input_json_delta` fragment accumulation, `message_start`/`message_delta` usage parsing, and `stop_reason` mapping (`end_turn`→`stop`, `tool_use`→`tool_calls`, `max_tokens`→`length`).

### Gemini

Native Generative Language client: `x-goog-api-key` header (the key never appears in URLs), `systemInstruction`, user/model roles, typed parts with `functionCall`/`functionResponse`, `finishReason` mapping (`STOP`→`stop`, `MAX_TOKENS`→`length`), and `usageMetadata` parsing.

### Error normalization

Non-2xx responses become `*statusError` carrying a normalized kind; transport failures classify as connection/timeout/canceled; malformed bodies classify as protocol errors. `providers.Classify(err)` returns the kind (`ErrKindAuth`, `ErrKindRateLimit`, `ErrKindQuota`, `ErrKindServer`, `ErrKindTimeout`, `ErrKindProtocol`, ...). Every error that could echo credentials passes through redaction, so API keys never appear in messages shown to users.

Transient 429s retry with exponential backoff capped by policy, honoring `Retry-After`; quota/billing exhaustion is detected and never retried. One inference request per provider instance is allowed at a time by design.

Retry work model (upper bounds, not exact counts): the transport allows up to 4 attempts per model turn (1 + 3 retries) and the runtime allows up to 3 turn attempts (1 + 2 retries), so one model turn can issue up to 4 × 3 requests against a persistently failing endpoint; under `ff supervise` that multiplies again by (1 + restarts) runs per episode.

## Service Catalog

`Catalog` describes every known service: display name, transport type, default base URL, authentication environment variable, local/cloud scope, and known models. The display registry used by pickers (`Registry`, `ByID`, `DisplayName`, `ModelDisplayName`) derives from it.

Adding a service whose API speaks a supported protocol means adding one catalog entry — no new code. Multi-protocol gateways instead add one router type plus a per-service model→protocol table (see OpenCode above).

## Factory Registry

```go
registry := providers.DefaultFactories() // ollama, openai-compatible, openai-responses, anthropic, gemini, vertex, bedrock, azure, opencode-zen, opencode-go
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
