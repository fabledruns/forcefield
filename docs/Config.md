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

agent:
  name: default
  system_prompt: |
    You are Forcefield, a local-first coding agent. Complete software tasks in real repositories: inspect, change, run, debug, and verify. Prefer a working, minimal result over advice or extra architecture.
```

### `model`

| Field      | Required | Description                                              |
| ---------- | -------- | -------------------------------------------------------- |
| `provider` | Yes      | Provider name. This prototype supports `ollama`.         |
| `endpoint` | Yes      | Base URL of the provider, for example `http://localhost:11434`. |
| `name`     | Yes      | Model name, for example `ornith:9b`.                     |

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

`Load` fails early if required model fields are missing:

- `model.provider` must not be empty.
- `model.endpoint` must not be empty.
- `model.name` must not be empty.

Permission values (`permissions.default` and every `permissions.tools.*`) must be `allow`, `deny`, or `ask`.

## API Keys

The NVIDIA provider reads its key from the `NVIDIA_API_KEY` environment variable. If the variable is unset, `Load` also checks two `.env` files, in order:

1. `.env` in the current project directory
2. `~/.forcefield/.env`

A key found this way stays inside Forcefield: it is never written back into the process environment and never persisted to config.yaml, so commands run by the shell tool never inherit it. A `.env` file with malformed non-comment lines is rejected with an error naming the file and line; it is never partially applied.

`ff doctor` reports where a key was found without printing its value.

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
- Runtime model and provider switches update the in-memory runtime. They do not rewrite `config.yaml` in the current prototype.
