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

### `ff run [task]`

Runs one prompt through the runtime and prints the final response.

```bash
ff run explain this repository
```

The command joins all task arguments into one prompt string.

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
