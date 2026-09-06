# Forcefield (`ff`)

Forcefield is a local-first command line tool for running AI agents.

It provides the runtime around a model: tools, skills, sessions, memory, permissions, shell execution, and provider communication. Forcefield runs as a single binary.

It does not require:

* A user account
* A cloud service
* Remote data processing
* Telemetry

Forcefield is under active development. Features and interfaces can change.

## Features

* Local model execution through Ollama and LM Studio
* Support for remote model providers
* Interactive terminal interface
* Streaming responses
* Agent skills
* Agent tools
* Session storage and recovery
* Model provider abstraction
* Tool permissions
* Context management
* Project search
* Shell execution
* Secret redaction
* Agent memory

## Requirements

For local models, install Ollama or another supported provider and have a model available.

Example:

```bash
ollama pull ornith:9b
```

Go 1.22+ is only required when building from source.

## Installation

### Linux / macOS

```bash
curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.sh | sh
```

The installer detects `amd64` or `arm64` and installs `ff` to `~/.local/bin`.

### Windows

```powershell
irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.ps1 | iex
```

The Windows installer installs `ff.exe` to `$HOME\.local\bin` and adds that directory to the user `PATH` when required.

The installers require no Administrator privileges and can be run again to upgrade an existing installation.

### Trust and verification

The `curl | sh` and `irm | iex` commands download the installer from the `main` branch over HTTPS and execute it immediately. This means you are trusting the installer contents at that point in time.

For reproducible installation, pin the installer to a release tag or download the installer first and inspect it.

Release binaries include `checksums.txt`. The installers verify the downloaded binary against those checksums.

Checksum verification provides integrity against the published checksum file. It does not provide independent authenticity beyond the GitHub release and HTTPS trust chain.

### Manual installation

1. Download the appropriate binary from GitHub Releases.
2. Rename it to `ff` (`ff.exe` on Windows) if necessary.
3. Place it somewhere on your `PATH`.
4. On Linux/macOS, make it executable:

```bash
chmod +x ff
```

Release artifacts use these names:

```text
ff-linux-amd64
ff-linux-arm64
ff-darwin-amd64
ff-darwin-arm64
ff-windows-amd64.exe
ff-windows-arm64.exe
```

### Version pinning

Linux/macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.sh | sh -s -- --version v1.0.0
```

Or, from a checked-out repository:

```bash
FORCEFIELD_VERSION=v1.0.0 sh scripts/install.sh
```

Windows:

```powershell
$env:FORCEFIELD_VERSION="v1.0.0"; irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.ps1 | iex
```

Or from a checked-out repository:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install.ps1 -Version v1.0.0
```

### Upgrading

Run the installation command again.

The installer replaces the existing binary in place. It does not modify `~/.forcefield` or project sessions.

### Uninstalling

Linux/macOS:

```bash
sh scripts/uninstall.sh
```

Or:

```bash
curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/uninstall.sh | sh
```

Windows:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/uninstall.ps1
```

Or:

```powershell
irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/uninstall.ps1 | iex
```

Uninstallation removes the Forcefield binary only. Configuration, sessions, memory, and other files under `~/.forcefield` are left untouched.

### Supported platforms

| OS      | Architecture | Artifact               |
| ------- | ------------ | ---------------------- |
| Linux   | amd64        | `ff-linux-amd64`       |
| Linux   | arm64        | `ff-linux-arm64`       |
| macOS   | amd64        | `ff-darwin-amd64`      |
| macOS   | arm64        | `ff-darwin-arm64`      |
| Windows | amd64        | `ff-windows-amd64.exe` |
| Windows | arm64        | `ff-windows-arm64.exe` |

Release artifacts are statically linked with `CGO_ENABLED=0` and built using `go build -trimpath -ldflags "-s -w"`.

### PATH troubleshooting

If `ff` is not found after installation, restart the terminal so the updated `PATH` is loaded.

You can also check:

```bash
export PATH="$HOME/.local/bin:$PATH"
ff --version
ff doctor
```

The installer does not duplicate existing `PATH` entries or overwrite existing shell configuration.

## Build from source

Clone the repository and run:

```bash
go build -o ff .
```

Windows:

```powershell
go build -o ff.exe .
```

Run the resulting binary:

```bash
./ff
```

## Usage

Start the interactive terminal:

```bash
ff
```

Then enter a request:

```text
> explain this repository
```

Run a task directly:

```bash
ff run "inspect this repository and explain its structure"
```

Run diagnostics:

```bash
ff doctor
```

## Commands

Forcefield provides both CLI commands and interactive slash commands.

CLI commands include:

```text
ff init
ff run
ff chat
ff tools
ff sessions
ff memory
ff doctor
```

Inside an interactive session:

```text
/help
/sessions
/status
/tools
/skills
/skills list
/skills show <id>
/resume
```

`/status` shows the active provider, model, session information, and available tools.

## Configuration

Forcefield stores its configuration at:

```text
~/.forcefield/config.yaml
```

Example:

```yaml
model:
  provider: ollama
  endpoint: http://localhost:11434
  name: ornith:9b

agent:
  name: default
  system_prompt: |
    You are Forcefield, a local-first coding agent.
    Complete software tasks in real repositories:
    inspect, change, run, debug, and verify.
```

Configuration controls the model provider, endpoint, model, and agent instructions.

Additional runtime settings are available for context management, permissions, workspace boundaries, and shell execution.

## How it works

Forcefield owns the agent loop:

```text
User
 │
 ▼
Command / TUI
 │
 ▼
Agent Runtime
 ├── Context
 ├── Permissions
 ├── Sessions
 ├── Skills
 ├── Memory
 └── Tools
       │
       ▼
   Model Provider
       │
       ▼
    Response
```

The model proposes operations through tool calls. Forcefield applies permission rules, executes tools, records their results, and continues the conversation.

Providers handle communication with individual model APIs. The runtime remains independent of the provider being used.

## Tools

Built-in tools include:

```text
read_file
write_file
list_files
search_files
find_files
shell
secret_scan
load_skill
memory
```

Tools receive structured input, perform an operation, and return a result to the runtime.

Tool execution is subject to permission rules and runtime limits.

## Shell execution

Shell commands use a configurable executor.

```text
native
```

Runs commands directly on the host. This is the default and preserves the historical Forcefield behavior.

```text
wsl
```

On Windows, runs commands inside a WSL distribution with a pinned working directory, restricted environment, and optional network isolation.

WSL mode requires an available WSL distribution. Forcefield does not silently fall back to native execution.

WSL does not provide filesystem confinement. A WSL process can access Windows drives through `/mnt`.

For the full shell and sandbox configuration, see `docs/Sandbox.md`.

## Skills

Skills are Markdown files stored globally under:

```text
~/.forcefield/skills/
```

Supported layouts:

```text
~/.forcefield/skills/review.md
~/.forcefield/skills/git-review/SKILL.md
```

Forcefield indexes skill metadata into a small catalog. The full skill body is loaded when needed.

Supporting files are not executed automatically.

Example:

```md
# Go Development

Use the Go language standard.

Prefer simple designs.

Use clear error handling.
```

Manage skills with:

```text
/skills
/skills list
/skills show <id>
```

## Sessions and memory

Sessions are stored locally under:

```text
.forcefield/sessions/
```

Use `/sessions` to view stored sessions and `/resume` to continue an existing session.

Persistent agent memory is stored at:

```text
~/.forcefield/memory.md
```

Session state is written atomically. Interrupted tool execution is recorded so the runtime can identify incomplete work when a session is reopened.

## Project search

`search_files` and `find_files` provide bounded project search.

Generated and dependency directories such as these are excluded from searches:

```text
.git
node_modules
dist
build
target
vendor
.next
__pycache__
```

Search operations also limit the number of files, file sizes, matches, and execution time.

## Permissions and redaction

Tool permissions use three states:

```text
allow
ask
deny
```

Rules are evaluated before a tool executes.

Forcefield also redacts recognized credentials from runtime output and persisted state. Redaction covers areas such as tool results, shell output, provider errors, session data, tool arguments, memory, and diagnostics.

`ff doctor` does not print secret values such as API keys.

## Project structure

```text
forcefield/
├── cmd/
│   └── ff/
│       └── main.go
├── internal/
│   ├── agent/
│   ├── command/
│   ├── config/
│   ├── providers/
│   ├── runtime/
│   ├── session/
│   ├── skills/
│   ├── tools/
│   └── tui/
├── examples/
│   └── skills/
└── scripts/
```

Package responsibilities are separated by runtime, provider, session, tool, skill, configuration, and terminal-interface concerns.

## Development

Run the test suite:

```bash
go test ./...
```

Build:

```bash
go build ./...
```

Run static analysis:

```bash
go vet ./...
```

Format the repository:

```bash
gofmt -w .
```

## License

Apache License 2.0.
