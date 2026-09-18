# Command

Package: `internal/command`

The `command` package implements the slash command system for interactive chat. It parses input, registers commands, looks up commands, and runs them. The package does not import Bubble Tea.

## Purpose

Slash commands control the chat session. Examples:

- `/help` — list available commands
- `/clear` — clear the chat transcript
- `/exit` — end the session
- `/agent` — list or switch the active specialised agent
- `/model` — show or switch the active model
- `/provider` — show or switch the active provider
- `/sessions` — open the session picker
- `/status` — show the active agent, model, session size, tools, and skills
- `/tools` — list the tools available to the agent
- `/usage` — show session and context usage estimates
- `/context` — show what the next model turn will send
- `/diff` — show the workspace's unstaged diff
- `/git` — show git status for the workspace
- `/compact` — show automatic compaction state
- `/jobs` — list background shell jobs
- `/cancel` — cancel the current run
- `/plan` — produce an implementation plan without modifying anything
- `/build` — execute the accepted plan

## Core Interfaces

### `Command`

Each slash command implements this interface:

| Method        | Description                                      |
| ------------- | ------------------------------------------------ |
| `Name()`      | Primary command name, for example `help`.        |
| `Aliases()`   | Optional alternate names, for example `?`.       |
| `Description()` | Short text for `/help`.                        |
| `Usage()`     | Usage string, for example `/model [name]`.       |
| `Execute()`   | Runs the command with a context and arguments.   |

### `Context`

Commands act on the session through a small interface. The TUI is the production implementation. Tests can supply a fake.

| Method               | Description                                      |
| -------------------- | ------------------------------------------------ |
| `Println`            | Write a message to the user.                     |
| `Clear`              | Clear the visible transcript.                    |
| `Quit`               | End the interactive session.                     |
| `Model` / `SetModel` | Read or change the active model.                 |
| `Provider` / `SetProvider` | Read or change the active provider.        |
| `Agent` / `SetAgent` | Read or change the active specialised agent.     |
| `Agents`             | List summaries for all known agents.             |
| `OpenSessionPicker`  | Open the session selection UI.                   |
| `OpenProviderPicker` | Open the provider selection UI.                  |
| `OpenModelPicker`    | Open the model selection UI.                     |
| `SessionStats`       | Report session id, message count, size, save errors, and plan status. |
| `ContextInfo`        | Report estimated context consumption for `/usage`, `/context`, and `/compact`. |
| `Git`                | Run a read-only git inspection for `/diff` and `/git`. |
| `Jobs`               | Snapshot background shell jobs for `/jobs`.      |
| `CancelRun`          | Cancel the current run for `/cancel`.            |
| `StartPlan`          | Begin a read-only planning turn for `/plan`.     |
| `StartBuild`         | Execute the accepted plan for `/build`.          |
| `Tools`              | List one line per available tool.                |
| `Skills`             | List global skills in catalog order.             |
| `LoadSkill`          | Load one global skill's Markdown body by id.     |

## Main Parts

| Part       | File           | Description                                              |
| ---------- | -------------- | -------------------------------------------------------- |
| Parser     | `parser.go`    | Detects slash commands and splits name and arguments.    |
| Registry   | `registry.go`  | Stores commands by name and alias.                       |
| Dispatch   | `dispatch.go`  | Looks up a command and runs it.                          |
| Suggest    | `suggest.go`   | Suggests similar names for unknown commands.             |
| Builtin    | `builtin/`     | Built-in command implementations.                        |

## Parse and Dispatch Flow

1. The user submits a line in the TUI.
2. `Parse` checks for a leading `/`.
3. If the line is not a command, the TUI treats it as a chat message.
4. If the line is a command, `Dispatch` looks up the name in the registry.
5. If the name is unknown, dispatch returns an error with suggestions.
6. If the name is known, the command runs against the context.

## Built-in Commands

| Command      | Aliases | Usage                    | Action                                      |
| ------------ | ------- | ------------------------ | ------------------------------------------- |
| `help`       | `?`     | `/help`                  | List registered commands.                   |
| `clear`      | —       | `/clear`                 | Clear the chat transcript.                  |
| `exit`       | `quit`  | `/exit`                  | End the chat session.                       |
| `model`      | —       | `/model [name]`          | Show or switch the active model.            |
| `provider`   | —       | `/provider [name]`       | Show or switch the active provider.         |
| `agent`      | —       | `/agent [name]`          | List agents or switch the active agent.     |
| `sessions`   | `s`     | `/sessions`              | Open the saved session picker.              |
| `status`     | —       | `/status`                | Show agent, provider, model, session size, tools, skills, plan. |
| `tools`      | —       | `/tools`                 | List the tools available to the agent.      |
| `skills`     | `skill` | `/skills [list|show <id>]` | List and inspect global skills.           |
| `usage`      | —       | `/usage`                 | Show session size, estimated tokens, budget, and fit. |
| `context`    | —       | `/context`               | Show what the next model turn will send.    |
| `diff`       | —       | `/diff [path]`           | Show the unstaged diff, optionally scoped to a path. |
| `git`        | —       | `/git`                   | Show git status for the workspace.          |
| `compact`    | —       | `/compact`               | Show automatic compaction state (report only). |
| `jobs`       | —       | `/jobs`                  | List background shell jobs (read-only).     |
| `cancel`     | —       | `/cancel`                | Cancel the current run (same as Ctrl+C).    |
| `plan`       | —       | `/plan <task>`           | Produce an implementation plan without modifying anything. |
| `build`      | —       | `/build`                 | Execute the accepted plan.                  |

## Plan and Build

`/plan <task>` starts a read-only planning turn through the normal
runtime loop: the system prompt gains a planning overlay and the tool
set narrows to inspection tools (`read_file`, `list_files`, search,
`git`, `secret_scan`, `load_skill`). The finished text is stored as the
session's draft plan (`session.Plan`) with a workspace tree signature
and message count. `/build` warns about drift since planning (workspace
or conversation) but always proceeds, executing the plan through the
normal agent loop with the full tool set. A cancelled, errored, or
blocked build leaves the plan `partial`; finishing marks it `done`.
`/status` shows the plan line when a plan exists.

## How to Add a Command

1. Create a type that implements `Command`.
2. Register an instance at TUI startup.
3. Do not change the parser, registry, or dispatch logic.

## Design Notes

- The package is independent of the TUI and the runtime.
- Parsing, lookup, suggestion, and execution are separate and testable.
- Built-in commands live in `command/builtin` so core dispatch stays small.
