# Command

Package: `internal/command`

The `command` package implements the slash command system for interactive chat. It parses input, registers commands, looks up commands, and runs them. The package does not import Bubble Tea.

## Purpose

Slash commands control the chat session. Examples:

- `/help` — list available commands
- `/clear` — clear the chat transcript
- `/exit` — end the session
- `/model` — show or switch the active model
- `/provider` — show or switch the active provider
- `/sessions` — open the session picker
- `/status` — show the active model, session size, and tools
- `/tools` — list the tools available to the agent

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
| `OpenSessionPicker`  | Open the session selection UI.                   |
| `OpenProviderPicker` | Open the provider selection UI.                  |
| `OpenModelPicker`    | Open the model selection UI.                     |
| `SessionStats`       | Report session id, message count, and size.      |
| `Tools`              | List one line per available tool.                |

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

| Command      | Aliases | Usage              | Action                                      |
| ------------ | ------- | ------------------ | ------------------------------------------- |
| `help`       | `?`     | `/help`            | List registered commands.                   |
| `clear`      | —       | `/clear`           | Clear the chat transcript.                  |
| `exit`       | `quit`  | `/exit`            | End the chat session.                       |
| `model`      | —       | `/model [name]`    | Show or switch the active model.            |
| `provider`   | —       | `/provider [name]` | Show or switch the active provider.         |
| `sessions`   | `s`     | `/sessions`        | Open the saved session picker.              |
| `status`     | —       | `/status`          | Show provider, model, session size, tools.  |
| `tools`      | —       | `/tools`           | List the tools available to the agent.      |

## How to Add a Command

1. Create a type that implements `Command`.
2. Register an instance at TUI startup.
3. Do not change the parser, registry, or dispatch logic.

## Design Notes

- The package is independent of the TUI and the runtime.
- Parsing, lookup, suggestion, and execution are separate and testable.
- Built-in commands live in `command/builtin` so core dispatch stays small.
