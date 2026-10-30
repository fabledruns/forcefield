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
- the configured provider is reachable and the configured model exists (Ollama), is loaded (LM Studio, llama.cpp), or the API key works (NVIDIA)
- session storage is readable; unreadable session files are named
- skills load from `~/.forcefield/skills`
- project memory parses
- the Bash execution backend (WSL on Windows) is usable
- the configured sandbox mode is actually deliverable; WSL mode that cannot run fails doctor with exit code 1
- MCP server configuration is valid, with last-known status from `.forcefield/mcp-status.json` (doctor never starts servers, so this does not prove reachability; see [MCP](MCP.md))

Doctor never prints secret values such as API keys. It exits non-zero when a `[FAIL]` item is found.

```bash
ff doctor
```

## Run evidence (v1.6)

`ff run` persists every headless run as a session (resumable via `--resume`) and writes a
structured JSONL orchestration event log beside the session JSON under
`.forcefield/sessions/` (`<session-id>.events.jsonl`). The log is append-only and
byte-bounded, and the writer scrubs every field for secrets. The schema is documented in
[Events](Events.md).

Exit codes reflect verification, not completion:

| Code | Meaning |
|---|---|
| 0 | verified completion (a plain answer that used no tools counts as verified) |
| 5 | unverified: the model finished without verification, the run was blocked (runtime-enforced stop), or a verified answer whose event log was unusable |
| 1 | errors: provider failure, session persistence failure, or cancellation (SIGINT/SIGTERM) |

Telemetry can never upgrade an outcome. When the event log cannot be opened, written, or
flushed, `ff run` says so on stderr and a verified completion is demoted to unverified
(exit 5); failed runs keep failing:

```text
ff run: orchestration event log could not be opened (<error>): <path>
ff run: orchestration event log write failed (<error>); session evidence may be incomplete
ff run: result marked unverified: orchestration event log unusable
```

The log carries identity, counters, and durations only — no task text, no arguments, no
response content. Common queries:

```bash
# final exit code of a run
jq -s '.[-1].exit_code' .forcefield/sessions/<session-id>.events.jsonl

# budget exhaustion (aggregate child turns, depth, spawn, task chars)
grep budget_exhausted .forcefield/sessions/<session-id>.events.jsonl

# delegation lifecycle (requested / accepted / rejected) and child lifecycle
grep -E '"(delegation|child)_' .forcefield/sessions/<session-id>.events.jsonl

# tool denials
grep tool_denied .forcefield/sessions/<session-id>.events.jsonl

# per-child turn counts (one number per child progress line)
grep child_progress .forcefield/sessions/<session-id>.events.jsonl | jq '.turns'
```

`ff doctor` does not probe the events path; an unusable events location surfaces as the
stderr lines above during `ff run`, not at startup.

`ff run --resume <session-id>` keeps the recovery contract's exit codes instead: 0
verified, 2 terminal, 3 retryable, 4 needs a human, 5 unverified.

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
