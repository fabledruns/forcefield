# CLAUDE.md

# Forcefield

Forcefield is a lightweight, local-first AI agent harness written in Go.

It is designed to feel like a terminal application rather than a web service or orchestration framework.

Core goals:

- local-first
- single binary
- zero telemetry
- fast startup
- predictable behavior
- minimal dependencies
- idiomatic Go

Always preserve these goals when making changes.

---

# Before Making Changes

Read the documentation in `/docs`.

Important references:

- docs/Index.md
- docs/Runtime.md
- docs/Providers.md
- docs/Tools.md
- docs/Skills.md
- docs/Session.md
- docs/TUI.md
- docs/Command.md
- docs/CLI.md
- docs/Config.md
- docs/Agent.md

The documentation describes the intended architecture.

If implementation and documentation disagree, prefer asking rather than silently changing architecture.

---

# Architecture

Forcefield is intentionally modular.

Each package owns one responsibility.

```
CLI
 ↓
TUI
 ↓
Runtime
 ├── Agent
 ├── Provider
 ├── Session
 ├── Tools
 ├── Skills
 └── Config
```

The runtime coordinates everything.

Other packages should not know about each other unless explicitly required.

---

# Runtime Rules

The runtime owns:

- conversation loop
- provider execution
- tool execution
- skill loading
- session updates

Providers stream **one model turn**.

The runtime owns the multi-turn agent loop.

Never move orchestration logic into providers.

---

# Provider Rules

Providers should only:

- translate Forcefield messages
- stream model output
- report tool calls

Providers should never:

- execute tools
- manage sessions
- load skills
- implement agent logic

---

# Tool Rules

Tools are deterministic local actions.

Keep tools:

- focused
- composable
- predictable

Return structured errors whenever possible instead of panicking.

Avoid hidden side effects.

---

# Skill Rules

Skills are loaded on demand.

Do not inject every skill into the system prompt.

The model receives only the skill catalog.

Full skill bodies should only be loaded through `load_skill`.

---

# Session Rules

Sessions are local.

Do not introduce cloud synchronization.

Conversation history must remain deterministic.

Avoid breaking session compatibility.

---

# TUI Rules

The TUI is presentation only.

Business logic belongs elsewhere.

Do not duplicate runtime behavior inside the UI.

---

# Command Rules

Slash commands should remain independent of Bubble Tea.

Commands operate through the command.Context interface.

Keep commands small.

---

# Configuration

Configuration is user-editable.

Do not silently rewrite configuration files.

Runtime model/provider switching is temporary unless explicitly implemented otherwise.

---

# Coding Style

Write idiomatic Go.

Prefer:

- small packages
- small functions
- explicit control flow
- composition
- early returns

Avoid:

- unnecessary interfaces
- unnecessary abstractions
- global state
- reflection unless absolutely required

Prefer the Go standard library.

---

# Performance

Performance matters.

Prefer:

- streaming over buffering
- lazy loading
- minimal allocations
- avoiding repeated filesystem scans

Do not trade significant complexity for tiny optimizations.

---

# Error Handling

Never panic for expected failures.

Wrap errors with context.

Example:

```go
return fmt.Errorf("loading provider: %w", err)
```

Error messages should help users fix the issue.

---

# Testing

Before finishing work, ensure:

```bash
go test ./...
```

and

```bash
go build ./...
```

both succeed.

New features should include tests whenever practical.

---

# Commit Philosophy

Prefer incremental changes.

Avoid unrelated refactors.

If improving existing code:

- preserve behavior
- reduce complexity
- improve readability

---

# When Unsure

Ask:

- Does this make Forcefield simpler?
- Does this reduce startup time?
- Does this improve readability?
- Does this preserve modularity?
- Would an experienced Go developer expect this implementation?

If not, reconsider the approach.