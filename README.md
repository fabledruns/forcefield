<p align="center">
  <img src="assets/hero.svg" alt="FORCEFIELD — A local-first runtime for AI agents" width="100%">
</p>

<p align="center">
  <a href="https://go.dev/"><img src="https://img.shields.io/badge/go-1.26.4+-0C0D12?style=flat-square&logo=go&logoColor=white" alt="Go Version"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-ED2663?style=flat-square" alt="License"></a>
  <a href="#supported-platforms"><img src="https://img.shields.io/badge/platform-linux%20%7C%20macos%20%7C%20windows-0C0D12?style=flat-square" alt="Supported Platforms"></a>
  <a href="#supported-platforms"><img src="https://img.shields.io/badge/arch-amd64%20%7C%20arm64-0C0D12?style=flat-square" alt="Supported Architectures"></a>
  <a href="#installation"><img src="https://img.shields.io/badge/binary-standalone-0C0D12?style=flat-square" alt="Single Binary"></a>
</p>

<p align="center">
  <a href="https://dodolaunch.com/p/superprime?ref=badge" target="_blank" rel="noreferrer">
    <img
      src="https://dodolaunch.com/badge/superprime?theme=light&type=launched"
      alt="Forcefield on DodoLaunch"
      height="54"
    />
  </a>
  &nbsp;&nbsp;
  <a href="https://peerlist.io/jehoshuam/project/forcefield" target="_blank" rel="noreferrer">
    <img
      src="https://peerlist.io/api/v1/projects/embed/PRJH6A7OMP7DM9JK9CODMPQQKQQAJB?showUpvote=true&theme=light"
      alt="Forcefield on Peerlist"
      height="54"
    />
  </a>
</p>

---

## OVERVIEW

Forcefield is a local-first runtime for executing AI agents.

It provides the complete host execution harness around a language model: deterministic tool dispatch, on-demand skill indexing, project-scoped memory, atomic session recovery, 3-state permission gating, shell execution, and provider transport abstraction. It runs as a single static binary.

Forcefield does not require a local model. It works with local inference engines (Ollama, LM Studio, llama.cpp) and remote API providers (Anthropic, OpenAI, Google Gemini, AWS Bedrock, Azure AI, NVIDIA NIM, Groq, Mistral, Together, OpenRouter, and OpenCode). Running models locally is optional.

| Architectural Invariant | Specification |
| :--- | :--- |
| **Local-First Boundary** | Zero user accounts, zero central cloud, zero telemetry, and zero outbound network traffic outside configured provider endpoints. |
| **Single Binary** | Statically linked Go binary (`CGO_ENABLED=0`) with zero external runtime dependencies. |
| **Provider Independence** | The runtime owns the multi-turn agent loop; providers stream single turns. Switching providers requires no harness modifications. |
| **State Integrity** | Atomic writes via temporary files, `fsync`, and rename. Interrupted or truncated turns fail closed rather than reporting false success. |

Forcefield is under active development. Runtime interfaces and schemas may evolve.

---

## CORE CAPABILITIES

* **Deterministic Runtime Loop**: The runtime coordinates the multi-turn conversation loop. The model requests operations via structured tool calls; Forcefield validates permissions, runs the tools, and delivers results back to the model.
* **Three-State Permission Gating**: Pre-execution authorization with `allow`, `ask`, and `deny` rules evaluated before any host action executes.
* **Credential Redaction**: Automated detection and masking of API keys and credentials in tool arguments, outputs, error messages, persisted session files, and diagnostics (`ff doctor` never prints secret values).
* **Atomic State and Crash Recovery**: Project-local session histories written via temp-file, `fsync`, and atomic rename. Interrupted turns record explicit state for clean recovery upon reopening.
* **Dual Execution Backends**: Configurable native host execution or sandboxed Windows WSL execution with pinned working directories and sanitized environments.
* **Specialised Agent Personas**: Pre-configured capability profiles (`coding`, `cyber`, `research`, `legal`, `docs`, `devops`, `general`) with isolated tool allowlists.
* **On-Demand Skills**: Discovers and indexes Markdown skills into a minimal catalog. Full skill bodies are loaded on demand via `load_skill` without bloating base system prompt context.
* **Bounded Output and Context Guard**: Tool outputs are individually capped (shell output capped at 2 MiB, 30s default timeout). The runtime context guard enforces a 6,000-byte head/tail result elision before history reaches the provider, alongside a 100-message sliding window and 1,000-message session limit.
* **Bounded Subagent Delegation**: Optional hierarchical subagent execution (`delegate`) with runtime-enforced limits on call depth, concurrency, spawn count, and aggregate turns.

---

## INSTALLATION

### LINUX / MACOS

```bash
curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.sh | sh
```

The script detects OS (`linux`, `darwin`) and architecture (`amd64`, `arm64`), downloads the matching release binary, verifies its integrity against `checksums.txt`, and installs `ff` to `~/.local/bin`.

### WINDOWS

```powershell
irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.ps1 | iex
```

Installs `ff.exe` to `$HOME\.local\bin` and ensures the directory is present on the user `PATH`. Neither installer requires Administrator or `sudo` privileges.

### VERSION PINNING

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.sh | sh -s -- --version v1.0.0

# Or via environment variable
FORCEFIELD_VERSION=v1.0.0 sh scripts/install.sh
```

```powershell
# Windows PowerShell
$env:FORCEFIELD_VERSION="v1.0.0"; irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.ps1 | iex

# Or from a cloned repository
powershell -ExecutionPolicy Bypass -File scripts/install.ps1 -Version v1.0.0
```

### UPGRADES

Re-run the installation command at any time.

* If the targeted version is already installed, the installer reports it and exits without downloading.
* To force reinstallation of the active version:
  * Linux / macOS: `sh scripts/install.sh --force` (or `FORCEFIELD_FORCE=1`)
  * Windows: `powershell -ExecutionPolicy Bypass -File scripts/install.ps1 -Force`
* Downgrades to older releases are refused unless forced.
* Upgrades replace the binary in place and never modify configuration (`~/.forcefield`) or session data.

### UNINSTALLATION

Uninstallation removes the Forcefield binary only. User configuration, memory, and project sessions remain untouched.

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/uninstall.sh | sh
```

```powershell
# Windows PowerShell
irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/uninstall.ps1 | iex

# To also remove ~/.local/bin from user PATH:
powershell -ExecutionPolicy Bypass -File scripts/uninstall.ps1 -RemovePath
```

### TRUST AND VERIFICATION

* Installation commands fetch release assets over HTTPS directly from the GitHub repository.
* Downloaded binaries are verified against the published `checksums.txt` SHA-256 manifest.
* For air-gapped or audited environments, download the release archive manually, inspect the install script, and verify checksums independently.

### SUPPORTED PLATFORMS

Release binaries are statically compiled with `CGO_ENABLED=0` using `go build -trimpath -ldflags "-s -w"`.

| Operating System | Architecture | Release Artifact |
| :--- | :--- | :--- |
| Linux | amd64 | `ff-linux-amd64` |
| Linux | arm64 | `ff-linux-arm64` |
| macOS | amd64 | `ff-darwin-amd64` |
| macOS | arm64 | `ff-darwin-arm64` |
| Windows | amd64 | `ff-windows-amd64.exe` |
| Windows | arm64 | `ff-windows-arm64.exe` |

### MANUAL INSTALLATION

1. Download the release binary matching your platform from GitHub Releases.
2. Rename the binary to `ff` (`ff.exe` on Windows).
3. Move the binary into a directory on your `PATH` (e.g. `~/.local/bin`).
4. On Linux and macOS, grant execution permissions: `chmod +x ff`.

### PATH CONFIGURATION

The installer scripts operate non-interactively without user prompts. When stdout is piped or `NO_COLOR` is set, ANSI colors and decoration are suppressed. Set `FORCEFIELD_ASCII=1` to force ASCII logging on standard terminals.

If `ff` is not recognized after installation, restart your terminal shell or verify:

```bash
export PATH="$HOME/.local/bin:$PATH"
ff --version
ff doctor
```

---

## QUICK START

### REQUIREMENTS

Forcefield requires connectivity to a supported model provider. Ollama or local runtimes are only needed if you choose local execution.

* **Local Inference**: Install Ollama, LM Studio, or llama.cpp, and load a model:
  ```bash
  ollama pull ornith:9b
  ```
* **Cloud Inference**: Set the corresponding API key for your provider:
  ```bash
  export ANTHROPIC_API_KEY="your-api-key"
  # or: export OPENAI_API_KEY="your-api-key"
  # or: export GEMINI_API_KEY="your-api-key"
  ```
* **Source Builds**: Go 1.26.4+ is required only when compiling from source.

### EXECUTION MODES

```bash
# Start interactive terminal interface
ff

# Execute a single-turn or multi-turn task headlessly
ff run "inspect this codebase, locate test failures, and propose fixes"

# Launch conversational mode
ff chat

# Launch with a specialised agent profile
ff --agent coding
ff chat --agent cyber
ff run --agent legal "review license compatibility in this repository"

# Resume an existing session in the current project
ff --resume <session-id>

# Run runtime diagnostic checks
ff doctor
```

### INTERACTIVE COMMANDS

Inside the interactive chat interface, use slash commands to inspect and manage runtime state:

```text
/help              Display command reference and keybindings
/status            Display active provider, model, context budget, and registered tools
/sessions          Browse and switch project-local conversation sessions
/tools             List available tools and their operational parameters
/skills            Display installed skills catalog
/skills list       List all available skill identifiers
/skills show <id>  Inspect full instructions for a specific skill
/new               Start a clean session in the current workspace
/clear             Clear the viewport transcript
```

Sessions are stored project-locally under `.forcefield/sessions/`. The `--resume` flag and `/sessions` command only surface sessions initialized within the current working directory.

---

## CONFIGURATION

Configuration is stored at:

```text
~/.forcefield/config.yaml
```

### EXAMPLE CONFIGURATION

```yaml
# Model provider configuration
model:
  provider: ollama
  endpoint: http://localhost:11434
  name: ornith:9b

# Default agent persona and instructions
agent:
  name: default
  system_prompt: |
    You are Forcefield, a local-first coding agent.
    Complete software tasks in real repositories:
    inspect, change, run, debug, and verify.

# Shell execution backend: native (default) or wsl
sandbox:
  executor: native

# Tool permissions: allow, ask, or deny
permissions:
  tools:
    shell: ask
    write_file: ask
    read_file: allow

# Hierarchical orchestration (disabled by default)
orchestration:
  enabled: false
  max_depth: 2
  max_concurrency: 3
```

### EXECUTION BACKENDS

Shell execution is governed by the configured sandbox executor:

* `native` (default): Runs processes directly on the host operating system with standard user permissions.
* `wsl` (Windows only): Dispatches shell commands inside a designated Windows Subsystem for Linux (WSL) distribution with a pinned working directory and sanitized environment variables.
  * WSL mode requires an active WSL installation. Forcefield will not silently fall back to native execution if WSL is unreachable.
  * **Filesystem Boundary Notice**: WSL execution does not provide complete filesystem confinement. Host Windows drives remain accessible to WSL processes through `/mnt/`.

For complete sandbox policies, network rules, and process supervision details, see [Sandbox Documentation](docs/Sandbox.md).

### SUBAGENT DELEGATION LIMITS

Multi-agent delegation is disabled by default. When enabled (`orchestration.enabled: true`), the runtime exposes the `delegate` tool to allow parent agents to spawn child runs for discrete subtasks.

To ensure deterministic safety and prevent unbounded recursion, the runtime enforces hard constraints at the invocation site:
* Maximum delegation call depth (`max_depth`).
* Maximum simultaneous child agents (`max_concurrency`).
* Aggregate turn limits, task-size boundaries, and per-run spawn caps.

See [Configuration Reference](docs/Config.md), [Events Specification](docs/Events.md), and [Orchestration Testing](docs/OrchestrationTesting.md).

---

## BUILT-IN TOOLS

The runtime provides a built-in suite of deterministic local tools:

| Tool | Category | Operational Bounds & Safeguards |
| :--- | :--- | :--- |
| `read_file` | Filesystem | Reads text files; refuses files larger than 5 MiB. |
| `write_file` | Filesystem | Writes or overwrites workspace files with path validation. |
| `list_files` | Filesystem | Lists directory trees; capped at 500 entries per call. |
| `pwd` | Shell | Returns current working directory. |
| `shell` | Shell | Runs command under configured executor. Output capped at 2 MiB (lines capped at 256 KiB); 30s default timeout (configurable up to 300s). |
| `shell_job` | Shell | Background process execution; poll, list, or cancel by job ID. Max 4 concurrent jobs, 1 MiB buffer, 300s lifetime. |
| `search_files` | Search | Bounded recursive pattern search. Skips lockfiles, binaries, and build directories (`node_modules`, `.git`, `dist`, `target`, `vendor`, `.next`, `__pycache__`). |
| `search_code` | Search | Fast regex search using `ripgrep` (`rg` CLI argv, never shell). Falls back to built-in search if `rg` is not on `PATH`. |
| `find_files` | Search | Path globbing and substring matching; returns up to 50 paths. |
| `git` | Version Control | Read-only inspection (`status`, `diff`, `log`). Disables external textconv filters and `core.fsmonitor` hooks. Max 256 KiB output. |
| `secret_scan` | Security | Scans target file or text for credentials, tokens, and private keys. Max 50 findings. |
| `load_skill` | Runtime | Loads the Markdown body of an assigned skill from `~/.forcefield/skills/`. |
| `memory` | Runtime | Manages persistent project memory (`~/.forcefield/memory/`). Prompts capped at 200 entries or 8 KiB. |
| `delegate` | Orchestration | Spawns a bounded child agent for subtasks. Active only when orchestration is enabled. |

For tool development and parameter schemas, see [Tools Documentation](docs/Tools.md).

---

## SUPPORTED PROVIDERS

Forcefield abstracts provider wire protocols behind a unified streaming interface (`providers.ModelProvider`). Adapters negotiate features (streaming, tool calls, reasoning tokens, and context limits) dynamically through explicit capability metadata.

| Provider | Scope | Default Endpoint | Authentication | Default / Sample Models |
| :--- | :--- | :--- | :--- | :--- |
| Ollama | Local | `http://localhost:11434` | None | `ornith:9b` |
| llama.cpp | Local | `http://localhost:9931/v1` | None | Dynamic (`llama-server --alias`) |
| LM Studio | Local | `http://localhost:1234/v1` | None | `local-model` (active loaded model) |
| Anthropic | Cloud | `https://api.anthropic.com` | `ANTHROPIC_API_KEY` | `claude-sonnet-4-5`, `claude-haiku-4-5` |
| OpenAI | Cloud | `https://api.openai.com/v1` | `OPENAI_API_KEY` | `gpt-4o`, `gpt-4o-mini` |
| Google Gemini | Cloud | `https://generativelanguage.googleapis.com` | `GEMINI_API_KEY` | `gemini-2.5-flash`, `gemini-2.5-pro` |
| Google Vertex AI | Cloud | Deployment-scoped | Application Default Credentials (ADC) | `gemini-2.5-flash`, `gemini-2.5-pro` |
| Amazon Bedrock | Cloud | AWS Region-scoped | Standard AWS Credential Chain | Configured via `providers.<id>.models` |
| Azure AI | Cloud | Resource-scoped | `AZURE_OPENAI_API_KEY` | Deployment-scoped model names |
| NVIDIA NIM | Cloud | `https://integrate.api.nvidia.com/v1` | `NVIDIA_API_KEY` | `nvidia/nemotron-3-ultra-550b-a55b` |
| Groq | Cloud | `https://api.groq.com/openai/v1` | `GROQ_API_KEY` | `llama-3.3-70b-versatile` |
| Mistral AI | Cloud | `https://api.mistral.ai/v1` | `MISTRAL_API_KEY` | `mistral-large-latest`, `mistral-small-latest` |
| Together AI | Cloud | `https://api.together.xyz/v1` | `TOGETHER_API_KEY` | `meta-llama/Llama-3.3-70B-Instruct-Turbo` |
| OpenRouter | Cloud | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` | `openrouter/auto` |
| OpenCode | Cloud | `https://opencode.ai/zen/v1` | `OPENCODE_API_KEY` | Zen and Go subscription models |

For provider configuration details, see [Providers Reference](docs/Providers.md).

---

## DOCUMENTATION

Architecture guides and reference manuals are organized under [`docs/`](docs/Index.md):

### GUIDES

* [Getting Started](docs/GettingStarted.md): Setup, provider configuration, and running initial sessions.
* [CLI Reference](docs/CLI.md): Command-line flags and argument reference for `ff`, `ff run`, and `ff chat`.
* [Configuration Reference](docs/Config.md): Full YAML specification for models, providers, agents, permissions, and tools.
* [Command Reference](docs/Command.md): Interactive slash command specifications and TUI shortcuts.
* [Terminal Interface (TUI)](docs/TUI.md): Bubble Tea architecture, telemetry canvas, and state machines.
* [Events and Telemetry Log](docs/Events.md): JSONL run-evidence log schema, correlation, and file semantics.
* [Hardening and Audit Record](docs/Audit.md): Forensic audit findings, boundary tests, and security design decisions.
* [Crash Recovery and Supervision](docs/Recovery.md): Process-tree supervisory contracts and shutdown guarantees.
* [Performance and Benchmarks](docs/Benchmarks.md): HPOV benchmark suite methodology, latency, and resource metrics.
* [Orchestration Testing](docs/OrchestrationTesting.md): Adversarial test inventory covering multi-agent limits.

### ARCHITECTURE

* [Runtime Coordination](docs/Runtime.md): Event flow, multi-turn loop lifecycle, context guards, and cancellation mechanics.
* [Model Providers](docs/Providers.md): Interface contracts, capability negotiation, and transport adapters.
* [Tools Framework](docs/Tools.md): Registration, execution policies, output elision, and error classification.
* [Sandbox and Shell Execution](docs/Sandbox.md): Host isolation boundaries, WSL configuration, and environment hygiene.
* [Session and Crash Recovery](docs/Session.md): Atomic persistence, history compaction, and recovery invariants.
* [Skills Architecture](docs/Skills.md): Discovery mechanisms, on-demand catalog indexing, and storage layouts.
* [Specialised Agents](docs/Agents.md): Built-in personas, capability models, and tool-filtering isolation.
* [Model Context Protocol (MCP)](docs/MCP.md): Integration and lifecycle management for local MCP tool servers.

---

## DEVELOPMENT

### BUILD FROM SOURCE

```bash
# Clone repository
git clone https://github.com/fabledruns/forcefield.git
cd forcefield

# Compile binary
go build -o ff .

# Windows PowerShell:
# go build -o ff.exe .

# Verify build
./ff --version
```

### VERIFICATION

```bash
# Execute test suite
go test ./...

# Run static analysis
go vet ./...

# Format source files
gofmt -w .
```

### REPOSITORY STRUCTURE

```text
forcefield/
├── cmd/
│   └── ff/                  # Binary entrypoint
├── internal/
│   ├── agent/               # Agent definitions, system prompts, and persona registry
│   ├── command/             # Interactive slash command parsers and handlers
│   ├── config/              # YAML configuration loading, merging, and validation
│   ├── orchevents/          # JSONL orchestration event stream and telemetry
│   ├── permissions/         # Three-state tool authorization engine
│   ├── providers/           # Universal LLM transport adapters and service catalog
│   ├── runtime/             # Multi-turn agent loop coordinator and context manager
│   ├── sandbox/             # Native and WSL shell execution backends
│   ├── session/             # Atomic JSON session persistence and compaction
│   ├── skills/              # Skill catalog discovery and on-demand loader
│   ├── tools/               # Deterministic local tool implementations and limits
│   └── tui/                 # Bubble Tea terminal interface, canvas, and styles
├── docs/                    # Architectural documentation and guides
├── examples/
│   └── skills/              # Reference skill definitions
└── scripts/                 # Platform installers and verification suites
```

---

## LICENSE

Forcefield is licensed under the [Apache License 2.0](LICENSE).

A project of superprime™.

Some parts of the TUI are inspired by the Crush project by Charm™.
