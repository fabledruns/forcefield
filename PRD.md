# Forcefield

## Repository Product Requirements Document

**Project:** Forcefield
**Organization:** Marlex Software
**Repository:** Forcefield
**Product Type:** Local-first AI agent harness
**Status:** Active development
**Document Version:** 1.0
**Last Updated:** August 16, 2026

---

## 1. Purpose

This document defines the current product direction, existing capabilities, technical expectations, and planned evolution of the Forcefield repository.

Forcefield is already an operational developer-focused AI agent harness. This PRD is therefore not a greenfield specification. It describes the system as it exists today and establishes what the repository should become as development continues.

The goal is to prevent Forcefield from turning into a pile of disconnected AI features.

Every new feature should strengthen the core idea:

> **Forcefield is the controlled runtime between an AI model and the developer's machine.**

---

## 2. Current Product

Forcefield is a Go-based, local-first agent harness designed to run AI agents through a developer-oriented CLI/TUI.

It provides an abstraction layer between models and the local environment.

The current system includes:

* Agent execution
* Streaming model responses
* Multiple model providers
* Local model support
* Tool execution
* Shell access
* Permission/approval flows
* Sessions
* Persistent memory
* Skills
* YAML configuration
* CLI commands
* Interactive TUI
* Concurrent tool execution
* Provider abstraction

The project is intentionally closer to tools such as Claude Code, OpenCode, and Gemini CLI than to a traditional chatbot.

---

# 3. Current Repository State

## 3.1 Language and Runtime

Forcefield is implemented primarily in Go.

The repository should continue prioritizing:

* Simple Go architecture
* Explicit interfaces
* Small packages
* Native concurrency
* Testability
* Minimal external dependencies
* Clear separation between runtime and UI

The agent runtime must not become tightly coupled to the TUI.

---

## 3.2 CLI

The current CLI is based around Cobra.

Existing command concepts include:

```text
ff init
ff run
ff chat
ff tools
ff sessions
ff memory
```

The CLI is the primary entry point for automation and non-interactive usage.

Future commands should follow the same philosophy:

* Short
* Predictable
* Scriptable
* Discoverable

Commands should not exist merely because a feature exists internally.

---

# 4. Existing Agent Runtime

The agent runtime is the core of Forcefield.

It is responsible for coordinating:

```text
User
 ↓
Agent
 ↓
Model
 ↓
Tool Request
 ↓
Permission System
 ↓
Tool Execution
 ↓
Tool Result
 ↓
Model
 ↓
Final Response
```

The runtime must support multi-step execution rather than treating every model response as a standalone chat message.

The runtime should remain independent from the presentation layer.

Both the CLI and TUI should consume the same runtime APIs.

---

# 5. Model Provider System

Forcefield currently supports multiple model backends.

Existing provider integrations include:

* Ollama
* LM Studio
* NVIDIA NIM

The provider layer should allow the agent runtime to remain provider-agnostic.

A model provider should be responsible for:

* Connection configuration
* Authentication
* Model selection
* Request construction
* Streaming
* Tool-call handling
* Provider-specific errors

The agent runtime should not contain provider-specific logic.

---

## 5.1 Local-First Model Discovery

Forcefield should detect locally available model servers where practical.

Examples include:

```text
Ollama
LM Studio
```

The user should be able to discover available models without manually configuring every endpoint.

Provider discovery should fail gracefully.

A missing provider should never prevent Forcefield from starting.

---

# 6. Tool System

Tools allow the agent to interact with the user's environment.

The existing system includes tools such as:

* Shell execution
* File operations
* Repository interaction

The tool system should remain modular.

Every tool should expose:

```text
Name
Description
Input schema
Execution
Result
Error
Permission requirements
```

Tools should not directly control the agent loop.

The runtime decides when a tool is called.

---

# 7. Permission and Approval System

Permission handling is one of Forcefield's defining features.

The agent must not have unrestricted access to the user's environment by default.

Operations should be classified according to their risk.

Examples:

```text
Read source file
→ Low risk

Run tests
→ Low risk

Create source file
→ Medium risk

Modify configuration
→ Medium risk

Execute destructive shell command
→ High risk
```

The system should support at minimum:

```text
Allow
Deny
Ask
```

Future versions should support persistent permission policies.

Example:

```yaml
permissions:
  file_read: allow
  file_write: ask
  shell: ask
```

The permission layer must exist below the agent's decision-making layer.

An LLM must never be able to bypass it by generating a different tool request.

---

# 8. Shell Execution

Shell execution is one of the most powerful and dangerous Forcefield capabilities.

The shell tool should:

* Execute commands through a controlled interface
* Capture stdout
* Capture stderr
* Return exit codes
* Support timeouts
* Support cancellation
* Report failures clearly
* Pass through the permission system

Long-running commands must not permanently block the agent runtime.

The system should eventually support process management and cancellation.

---

# 9. Sessions

Forcefield currently has a session system.

A session contains information such as:

```go
type Session struct {
    ID        string
    CreatedAt time.Time
    UpdatedAt time.Time
    Messages  []Message
}
```

Sessions are persisted locally.

The system must support:

* Creating sessions
* Saving sessions
* Loading sessions
* Resuming sessions
* Switching sessions
* Handling interrupted sessions

The session layer should remain independent of the UI.

A TUI restart must not destroy the underlying session.

---

# 10. Session UX

The session system should eventually support workflows such as:

```bash
ff sessions
ff sessions new
ff sessions resume <id>
```

Inside the TUI, users should be able to switch between sessions without restarting Forcefield.

Session metadata should eventually include:

* Human-readable title
* Model/provider
* Creation time
* Last activity
* Message count
* Current status

A session should be treated as a persistent workspace, not merely a saved chat transcript.

---

# 11. Memory

Forcefield includes persistent memory.

The current design uses a local memory store:

```text
~/.forcefield/memory.md
```

Memory exists separately from session history.

Session history answers:

> "What happened in this conversation?"

Memory answers:

> "What should the agent remember across conversations?"

The distinction must remain explicit.

Future memory improvements may include:

* Structured memories
* Memory categories
* Search
* Relevance ranking
* User-approved memories
* Memory deletion
* Memory expiration
* Better context selection

Memory should not become an uncontrolled dumping ground for model-generated text.

---

# 12. Skills

Forcefield includes a skills system.

Skills represent reusable agent capabilities or workflows.

A skill may provide:

* Instructions
* Tool requirements
* Workflow rules
* Supporting context
* Examples

The runtime should be able to discover available skills and expose relevant ones to the agent.

The skill system should eventually allow developers to create custom skills without modifying Forcefield's Go source.

Potential structure:

```text
skills/
├── debugging/
├── git-review/
├── testing/
└── documentation/
```

Skills should remain composable rather than becoming hardcoded agent personalities.

---

# 13. TUI

The TUI is the primary interactive interface.

The current implementation uses:

* Bubble Tea
* Lip Gloss
* Markdown rendering
* Syntax highlighting

The TUI should expose the runtime rather than duplicate it.

Important UI states include:

```text
Idle
Thinking
Streaming
Running tool
Waiting for approval
Tool completed
Error
Cancelled
```

The user should always know which state Forcefield is currently in.

---

## 13.1 TUI Requirements

The interface should clearly display:

* Current session
* Current provider
* Current model
* Agent response
* Tool activity
* Permission requests
* Errors
* Input state
* Execution state

The interface should prioritize terminal usability over visual decoration.

---

# 14. Input Handling

The input system needs to support normal terminal behavior.

Required behavior includes:

* Multi-line input
* Shift+Enter for newline
* Enter for submission
* Paste handling
* Long prompts
* Empty input handling
* Input focus
* Clear visual distinction between active and inactive states

Keyboard shortcuts should be documented and consistent.

---

# 15. Concurrent Tool Execution

Forcefield supports parallel tool calls where the model requests independent operations.

The runtime should execute safe independent operations concurrently where possible.

Example:

```text
Model
 ├── Read file A
 ├── Read file B
 └── Search repository
        ↓
   Parallel execution
        ↓
   Combined results
        ↓
       Model
```

Concurrency must not bypass permissions.

Each tool call still requires its own policy evaluation.

---

# 16. Configuration

Forcefield uses a local configuration file:

```text
~/.forcefield/config.yaml
```

Configuration should control:

* Provider
* Model
* Provider endpoints
* Tool policies
* Memory
* Sessions
* UI preferences

Configuration loading should have sensible defaults.

A broken configuration should result in a useful error rather than a panic.

---

# 17. Error Handling

Forcefield must treat errors as normal runtime events.

The system should gracefully handle:

* Provider unavailable
* Model unavailable
* Malformed model response
* Tool failure
* Permission denial
* Invalid tool arguments
* Network timeout
* Interrupted process
* Corrupted session
* Invalid configuration

Errors should preserve useful context.

Bad:

```text
Error
```

Better:

```text
Tool execution failed

Command:
go test ./...

Exit code:
1

stderr:
...
```

The TUI should never panic because an external model behaves unexpectedly.

---

# 18. Current Technical Priorities

The immediate engineering priorities are:

### P0

* Stabilize the agent runtime
* Eliminate session resume crashes
* Harden tool execution
* Improve permission handling
* Improve TUI input behavior
* Improve provider error handling
* Increase test coverage

### P1

* Improve skill discovery
* Improve memory management
* Improve session UX
* Improve model/provider discovery
* Improve tool output rendering
* Add better cancellation
* Improve context handling

### P2

* Advanced agent workflows
* Sandboxed execution
* MCP support
* Multi-agent execution
* Remote execution
* Vision capabilities
* Agent observability

---

# 19. Repository Quality Requirements

Every significant feature should include:

* Unit tests
* Error handling
* Documentation
* CLI/TUI integration where applicable
* Configuration support where applicable

Core runtime packages should be testable without launching the TUI.

Tests should prioritize deterministic behavior.

External model calls should not be required for the majority of the test suite.

---

# 20. Testing Strategy

Forcefield should use multiple testing levels.

### Unit Tests

Test:

* Agent logic
* Provider adapters
* Tool execution
* Permission policies
* Sessions
* Memory
* Skills
* Configuration

### Integration Tests

Test:

```text
Runtime
+
Provider
+
Tools
+
Permission system
```

### End-to-End Tests

Test complete workflows such as:

```text
Start Forcefield
↓
Create session
↓
Prompt agent
↓
Agent calls tool
↓
Approve operation
↓
Tool executes
↓
Agent receives result
↓
Agent responds
↓
Session saved
↓
Session resumed
```

The E2E suite should specifically protect against regressions in session handling and the agent loop.

---

# 21. Security Requirements

Security-sensitive operations must remain centralized.

No tool should be able to execute around the permission system.

No provider should receive local file contents unless the agent explicitly sends them as part of a model request.

Secrets should not be written into:

* Session logs
* Memory
* Tool output
* Debug logs

Telemetry, if introduced, must be opt-in.

---

# 22. Developer Experience

A new developer should be able to clone the repository and understand the architecture without reverse-engineering the entire codebase.

The repository should provide:

```text
README
CONTRIBUTING
Architecture documentation
Development setup
Testing instructions
Provider documentation
Tool documentation
Skill documentation
```

The codebase should favor obvious behavior over clever abstractions.

---

# 23. Product Boundaries

Forcefield should not become:

* A generic chatbot
* An IDE replacement
* A task manager
* A calendar
* A productivity suite
* A social platform
* A random collection of AI utilities

Features should be rejected or reconsidered if they do not improve the agent-runtime experience.

The repository needs a strong center of gravity.

---

# 24. Future Architecture

The long-term architecture should support:

```text
                         ┌───────────────┐
                         │    CLI / TUI  │
                         └───────┬───────┘
                                 │
                                 ▼
                       ┌──────────────────┐
                       │  Agent Runtime   │
                       └───────┬──────────┘
                               │
          ┌────────────────────┼────────────────────┐
          │                    │                    │
          ▼                    ▼                    ▼
     Providers              Tools              Context
          │                    │                    │
          │              ┌─────▼─────┐        ┌─────┴─────┐
          │              │Permission │        │ Sessions  │
          │              └─────┬─────┘        │ Memory    │
          │                    │              │ Skills    │
          │                    │              └───────────┘
          ▼                    ▼
       Models              Local System
```

The runtime remains the center.

UI, models, tools, and storage remain replaceable components around it.

---

# 25. Roadmap

## Phase 1: Stabilization

Focus on making the existing system reliable.

* Fix runtime crashes
* Fix session resume
* Harden tools
* Improve permission handling
* Fix TUI input
* Improve provider failures
* Expand tests

## Phase 2: Agent UX

Make Forcefield pleasant to use every day.

* Better sessions
* Better memory
* Better skills
* Better tool output
* Better model discovery
* Better cancellation
* Better context management

## Phase 3: Agent Runtime Expansion

Increase what the runtime can safely accomplish.

* Sandboxing
* MCP
* Git-aware workflows
* Better filesystem tooling
* Background tasks
* Execution traces

## Phase 4: Advanced Agents

Explore more complex workflows.

* Multi-agent execution
* Planner/executor patterns
* Specialized agents
* Remote agents
* Vision
* Long-running autonomous tasks with explicit controls

---

# 26. Definition of Done

Forcefield is considered mature when a developer can:

1. Install Forcefield.
2. Detect or configure a model provider.
3. Start an agent session.
4. Give the agent a real development task.
5. Allow the agent to inspect the repository.
6. Approve necessary modifications or commands.
7. Let the agent execute multiple tool calls.
8. Observe what the agent is doing.
9. Recover from errors.
10. Save the session.
11. Resume the session later.
12. Use persistent memory and skills when appropriate.

The experience should feel like operating a capable software agent through a controlled developer runtime, not opening another AI chat window.

---

# 27. Product North Star

Forcefield should make the following workflow feel normal:

```text
Human intent
     ↓
     AI
     ↓
Forcefield runtime
     ↓
Controlled execution
     ↓
Real software changes
     ↓
Human verification
```

The model provides intelligence.

Forcefield provides execution, context, control, and boundaries.

That separation is the foundation of the product.
