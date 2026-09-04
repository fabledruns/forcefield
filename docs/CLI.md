# CLI

Package: `cmd`

The CLI is the user entry point for Forcefield. The binary name is `ff`.

## Purpose

The CLI starts interactive chat, resumes a session, or runs a one-shot prompt. It uses the Cobra command framework.

## Commands

### `ff`

Starts the interactive terminal interface with a new session.

```bash
ff
```

### `ff --resume <session-id>`

Starts the interactive terminal interface with an existing session.

```bash
ff --resume a1b2c3d4-e5f6-7890-abcd-ef1234567890
```

### `ff chat`

Starts the same interactive chat as bare `ff`.

```bash
ff chat
```

### `ff --agent <name>` / `ff chat --agent <name>`

Selects a specialised agent (`coding, cyber, legal, docs, research, devops, general`).

```bash
ff --agent coding
ff chat --agent cyber
ff --resume <id> --agent legal
```

Precedence: `CLI --agent` > resumed session agent > configured default > `general`. When the flag overrides a resumed session's stored agent, the session is updated to the flagged agent.

### `ff run [task]`

Runs one prompt through the runtime and prints the final response.

```bash
ff run explain this repository
ff run --agent legal "summarise this clause"
```

The command joins all task arguments into one prompt string.

### `ff doctor`

Checks the local pieces Forcefield depends on and reports problems with actionable messages:

- config.yaml exists, parses, and validates
- the configured provider is reachable and the configured model exists (Ollama), is loaded (LM Studio), or the API key works (NVIDIA)
- session storage is readable; unreadable session files are named
- skills load from `~/.forcefield/skills`
- project memory parses
- the Bash execution backend (WSL on Windows) is usable
- the configured sandbox mode is actually deliverable; WSL mode that cannot run fails doctor with exit code 1

Doctor never prints secret values such as API keys. It exits non-zero when a `[FAIL]` item is found.

```bash
ff doctor
```

## Request Paths

### Interactive path

```text
ff / ff chat
    │
    ▼
session.New or session.Load
    │
    ▼
tui.Start
    │
    ▼
runtime.StreamChat
```

### One-shot path

```text
ff run [task]
    │
    ▼
runtime.Run
    │
    ▼
print response content
```

## Design Notes

- Bare `ff` opens chat so the common path needs no subcommand.
- `ff run` is useful for scripts and quick checks.
- Session resume is a root flag, not a separate subcommand, in the current prototype.
