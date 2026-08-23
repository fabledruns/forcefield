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

- Go version 1.22 or later
- Ollama installed
- A local model installed

Example:

```bash
ollama pull ornith:9b
```

---

# Build

To build Forcefield, run:

```bash
go build -o ff ./cmd/ff
```

The command creates the `ff` executable.

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

Skills are Markdown files.

Skills provide additional instructions for the agent.

Location:

```text
~/.forcefield/skills/
```

Example:

```md
# Go Development

Use the Go language standard.

Prefer simple designs.

Use clear error handling.
```

Forcefield loads all Markdown skill files during agent startup.

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