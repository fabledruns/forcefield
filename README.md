# Forcefield (`ff`)

## Overview

Forcefield is a local-first command line tool for running AI agents. It uses a local model provider, agent instructions, skills, tools, and sessions.
Forcefield runs as a single binary.

It does not require:

- A user account
- A cloud service
- Remote data processing
- Telemetry

Forcefield is under active development. Features and interfaces can change.

---

# Main Features

Forcefield provides:

- Local model execution through Ollama
- Interactive terminal interface
- Streaming responses
- Agent skills
- Agent tools
- Session storage
- Session recovery
- Model provider abstraction

---

# Requirements

Before you use Forcefield, make sure that you have:

- Ollama installed (or another supported provider)
- A local model installed

Example:

```bash
ollama pull ornith:9b
```

Go 1.22+ is only needed if you build from source.

---

# Installation

### Linux / macOS (shell)

```bash
curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.sh | sh
```

macOS supports both Apple Silicon (`arm64`) and Intel (`amd64`) — the installer detects the correct binary.

### Windows (PowerShell)

```powershell
irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.ps1 | iex
```

Installs to `~/.local/bin` (`$HOME\.local\bin` on Windows) and adds it to your user `PATH` if needed. No Administrator privileges required. Safe to run multiple times (upgrades in place).

> **Trust:** `curl | sh` / `irm | iex` downloads the installer from `main` over HTTPS and immediately executes it — convenient, but you are trusting `main` at that moment. For reproducibility or air-gapped review, pin to a tag (`https://raw.githubusercontent.com/fabledruns/forcefield/v1.0.0/scripts/install.sh`) or use the manual download below; either way the binary itself is still verified against `checksums.txt` from the GitHub Release (integrity, not independent authenticity beyond GitHub TLS).

### Manual installation

1. Download the binary for your OS/arch from [GitHub Releases](https://github.com/fabledruns/forcefield/releases).
2. Place it on your `PATH` as `ff` (`ff.exe` on Windows).
3. Ensure it is executable (`chmod +x ff` on Linux/macOS).

Artifacts are named `ff-<os>-<arch>` (`.exe` on Windows), e.g. `ff-linux-amd64`, `ff-darwin-arm64`, `ff-windows-amd64.exe`. Each release includes `checksums.txt`; the installers verify it automatically.

### Version pinning

Install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.sh | sh -s -- --version v1.0.0
FORCEFIELD_VERSION=v1.0.0 sh scripts/install.sh
```

Windows:

```powershell
$env:FORCEFIELD_VERSION="v1.0.0"; irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/install.ps1 | iex
# or when saved locally:
powershell -ExecutionPolicy Bypass -File scripts/install.ps1 -Version v1.0.0
```

### Upgrading

Run the same install command again. The installer detects an existing `ff` in the install directory, replaces it in place, and never touches `~/.forcefield` or project sessions.

### Uninstall

```bash
sh scripts/uninstall.sh
# or: curl -fsSL https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/uninstall.sh | sh
```

Windows:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/uninstall.ps1
# or: irm https://raw.githubusercontent.com/fabledruns/forcefield/main/scripts/uninstall.ps1 | iex
```

Removes only the binary (`~/.local/bin/ff`). Never removes `~/.forcefield`, sessions, memory, or config — those remain until you delete them manually.

### Supported platforms

| OS      | Arch  | Artifact               |
|---------|-------|------------------------|
| Linux   | amd64 | `ff-linux-amd64`       |
| Linux   | arm64 | `ff-linux-arm64`       |
| macOS   | amd64 | `ff-darwin-amd64`      |
| macOS   | arm64 | `ff-darwin-arm64`      |
| Windows | amd64 | `ff-windows-amd64.exe` |
| Windows | arm64 | `ff-windows-arm64.exe` |

All artifacts are statically linked (`CGO_ENABLED=0`) and built with `go build -trimpath -ldflags "-s -w"`.

### Troubleshooting PATH

If `ff` is not found after install, the installer likely added `~/.local/bin` to `~/.bashrc`, `~/.zshrc`, or `~/.profile` (Windows: user `PATH`). Restart your terminal or run:

```bash
export PATH="$HOME/.local/bin:$PATH"
ff --version
ff doctor
```

Check what the installer changed — it never duplicates entries and never overwrites existing config.

---

# Build from source

To build Forcefield, run:

```bash
go build -o ff .
```

The command creates the `ff` executable. On Windows:

```powershell
go build -o ff.exe .
```

---

# Start Forcefield

Run:

```bash
./ff
```

Forcefield starts the interactive terminal interface.

Example:

```text
> explain this repository
```

---

# System Operation

Forcefield processes a request in the following order:

```text
User Input
    |
    v
Command Handler
    |
    v
Agent Runtime
    |
    +-- Skills
    |
    +-- Memory
    |
    +-- Tools
    |
    v
Model Provider
    |
    v
Response
```

The runtime separates each function.

This allows each part to be changed without changing the complete system.

---

# Commands

Forcefield supports these commands:

```text
/help
Shows available commands.
```

```text
/sessions
Shows stored sessions.
```

```text
/status
Shows the active provider, model, session size, and tools.
```

```text
/tools
Lists the tools available to the agent.
```

```text
/skills
Lists and inspects the global skill catalog (/skills list, /skills show <id>).
```

```text
/memory
Manages agent memory (via the ff memory CLI subcommand).
```

---

# Diagnostics

Run `ff doctor` to check common local problems: invalid configuration,
unreachable providers, missing models, broken session files, and shell
backend issues. Doctor never prints secret values such as API keys.

```bash
ff doctor
```

---

# Configuration

Forcefield creates the configuration file during the first run.

Location:

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
    You are Forcefield, a local-first coding agent. Complete software tasks in real repositories: inspect, change, run, debug, and verify. Prefer a working, minimal result over advice or extra architecture.
```

The configuration file defines:

- Model provider
- Model endpoint
- Model name
- Agent instructions
- Optional shell execution sandbox (see `docs/Sandbox.md`)

---

# Shell Execution Boundary

Shell commands run through a configurable executor:

```text
native   no isolation (default; historical behavior)
wsl      commands run inside a WSL distribution with a pinned working
         directory, restricted environment, and optional enforced
         network isolation. Requires Windows; unavailable WSL is an
         error, never a silent fallback.
```

WSL mode does not confine filesystems (the distribution reaches all
Windows drives through /mnt); permission prompts and `ff doctor` state
exactly what is and is not isolated.

---

# Skills

Skills are global, filesystem-first Markdown files.

Skills provide additional instructions for the agent.

Location (global only):

```text
~/.forcefield/skills/
```

Supported layouts:

```text
~/.forcefield/skills/review.md              # file skill
~/.forcefield/skills/git-review/SKILL.md    # directory skill (supporting files alongside)
```

Example:

```md
# Go Development

Use the Go language standard.

Prefer simple designs.

Use clear error handling.
```

At startup Forcefield indexes skill metadata into a short catalog — the model sees only `id`, `name`, and `description`. The full skill body is loaded on demand via the `load_skill` tool or inspected with `/skills show <id>`. Supporting files are never executed automatically.

Slash commands:

```text
/skills              list available skills
/skills list         list available skills
/skills show <id>    display one skill's full instructions
```

---

# Tools

Tools allow the agent to perform actions.

Built-in tools include:

```text
read_file
write_file
list_files
shell
```

A tool can:

- Receive input from the agent
- Perform an operation
- Return a result

---

# Sessions

Forcefield saves chat sessions locally.

Session files are stored at:

```text
.forcefield/sessions/
```

Example:

```text
.forcefield/
└── sessions/
    ├── session-a.json
    └── session-b.json
```

Use `/sessions` to view saved sessions.

Use `/resume` to continue a previous session.

---

# Project Structure

```text
forcefield/
├── cmd/
│   └── ff/
│       └── main.go

├── internal/
│   ├── agent/
│   │   Agent runtime
│   │
│   ├── command/
│   │   Command handling
│   │
│   ├── config/
│   │   Configuration handling
│   │
│   ├── providers/
│   │   Model providers
│   │
│   ├── runtime/
│   │   Agent execution
│   │
│   ├── session/
│   │   Session storage
│   │
│   ├── skills/
│   │   Skill loading
│   │
│   ├── tools/
│   │   Tool system
│   │
│   └── tui/
│       Terminal interface
│
└── examples/
    └── skills/
```

---

# Design Rules

Forcefield follows these rules:

- Keep the runtime small.
- Keep components separate.
- Store user data locally.
- Allow replacement of models and tools.
- Avoid unnecessary system requirements.

---

# Future Development

Planned features:

- Improved session selection
- Tool permission control
- Improved memory system
- Additional model providers
- Agent profiles
- Plugin support
- Improved agent planning

---

# Purpose

Forcefield provides a simple runtime for local AI agents.

The model provides intelligence.

The tools provide actions.

The skills provide instructions.

The runtime connects these components.
```