# Config

Package: `internal/config`

The `config` package loads, validates, and manages Forcefield's global YAML configuration file.

## Purpose

Configuration tells Forcefield which model provider to use, which model to call, and which base agent instructions to apply. Forcefield stores this data on the local machine.

## File Location

```text
~/.forcefield/config.yaml
```

On first run, `Load` creates the Forcefield home directory and writes a default configuration file if the file does not exist.

## Configuration Shape

```yaml
model:
  provider: ollama
  endpoint: http://localhost:11434
  name: ornith:9b

providers:
  ollama:
    type: ollama

  openai:
    type: openai
    api_key_env: OPENAI_API_KEY

  anthropic:
    type: anthropic

  gemini:
    type: gemini

  openrouter:
    type: openai-compatible
    base_url: https://openrouter.ai/api/v1
    api_key_env: OPENROUTER_API_KEY
    headers:
      HTTP-Referer: https://myproject.example

agent:
  name: default
  system_prompt: |
    You are Forcefield, a local-first coding agent. Complete software tasks in real repositories: inspect, change, run, debug, and verify. Prefer a working, minimal result over advice or extra architecture.
```

### `model`

The active selection used for every request.

| Field      | Required | Description                                              |
| ---------- | -------- | -------------------------------------------------------- |
| `provider` | Yes      | Active provider ID: a `providers:` key or a known service id. |
| `endpoint` | No       | Base URL override; optional when the provider has catalog defaults (e.g. Ollama's `http://localhost:11434`). |
| `name`     | Yes      | Active model ID sent to the API, e.g. `ornith:9b`.       |

### `providers`

Each key defines one selectable provider. Every field is optional; omitted values fall back to that service's built-in defaults.

| Field         | Description                                                                    |
| ------------- | ------------------------------------------------------------------------------ |
| `type`        | Wire protocol (`ollama`, `openai-compatible`, `anthropic`, `gemini`) or a known service id (`openai`, `xai`, `nvidia`, `lmstudio`, ...). A custom id can alias a service to inherit its defaults. |
| `base_url`    | API root. Overrides the service default. Required when the type has no default (e.g. a self-hosted OpenAI-compatible server). |
| `api_key_env` | Environment variable (or `.env` file key) holding the API key. Defaults to the service's standard variable. |
| `model`       | Optional default model recorded for this provider.                             |
| `headers`     | Extra HTTP headers sent with every request (e.g. OpenRouter's `HTTP-Referer`).  |
| `models`      | Model IDs offered by providers that cannot enumerate their own models.          |

Supported services and their protocols are documented in [Providers](Providers.md).

### Secrets

API keys never live in config.yaml - there is no field that could store them. Keys resolve per provider from:

1. The process environment (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, `XAI_API_KEY`, `NVIDIA_API_KEY`, ...)
2. `.env` in the current project directory
3. `~/.forcefield/.env`

A key found this way stays inside Forcefield: it is never written back into the process environment and never persisted, so commands run by the shell tool never inherit it. Values are also redacted from every error message in case a provider echoes credentials back.

### `agent`

| Field           | Required | Description                                      |
| --------------- | -------- | ------------------------------------------------ |
| `name`          | No       | Display name of the default agent.               |
| `system_prompt` | No       | Agent identity. The operating contract is always appended by the agent package. |

## Functions

### `Dir`

```go
func Dir() (string, error)
```

Returns the Forcefield home directory (`~/.forcefield`) and creates it if needed.

### `Path`

```go
func Path() (string, error)
```

Returns the full path to `config.yaml`.

### `Load`

```go
func Load() (*Config, error)
```

Loads the configuration file.

Behavior:

1. Resolve the config path.
2. If the file does not exist, write the default template.
3. Read and parse the YAML file.
4. Validate required model fields.
5. Return a `Config` value or an error.

## Validation

`Load` fails early with a message naming the offending field:

- `model.provider` must be set and must resolve to a configured provider.
- `model.name` must be set.
- `model.endpoint`, when present, must be an absolute `http(s)` URL. It is optional when the active provider has catalog defaults.
- Every `providers.*` entry must use a supported type, a valid `base_url`, well-formed header names, and non-empty model IDs.

Permission values (`permissions.default` and every `permissions.tools.*`) must be `allow`, `deny`, or `ask`.

## API Keys

Each provider names its key source through `api_key_env`; when unset, the service's standard variable applies (`OPENAI_API_KEY` for OpenAI, `ANTHROPIC_API_KEY` for Anthropic, `GEMINI_API_KEY` for Gemini, `NVIDIA_API_KEY` for NVIDIA NIM, and so on).

If the variable is unset, resolution also checks two `.env` files, in order:

1. `.env` in the current project directory
2. `~/.forcefield/.env`

A `.env` file with malformed non-comment lines is rejected with an error naming the file and line; it is never partially applied.

A missing required key does not stop startup: Forcefield starts, and the first model turn fails with guidance naming the variable to set. `ff doctor` reports where a key was found without printing its value, and skips its reachability probe until the key exists.

## CUE Schema

The `cue/` directory holds a CUE schema mirroring this file's accepted shape (providers, agent limits, permissions, sandbox). Validate any config against it when the [CUE CLI](https://cuelang.org) is installed:

```bash
cd cue
cue vet . ~/.forcefield/config.yaml -d '#Config' -c
```

`make cue-check` runs the same checks against bundled valid/invalid fixtures, and the `Test` workflow enforces them in CI. Keep `cue/config.cue`, `cue/providers.cue`, and `cue/tools.cue` in sync with `internal/config`, `internal/providers/registry.go`, and the tool registrations.

## Saving

`Save` writes config.yaml atomically (temporary file plus rename), so an interrupted write can never leave a truncated configuration behind. The API key is never written to disk.

## Design Notes

- Configuration is local. Forcefield does not send config data to a remote service.
- The default file gives a first-time user a working starting point.
- Runtime model/provider switches persist by writing `model.name`, `model.provider`, and `model.endpoint` atomically. Provider entries under `providers:` are always user-owned: Forcefield never adds, edits, or removes them.
