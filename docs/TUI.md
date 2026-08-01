# TUI

Package: `internal/tui`

The `tui` package provides the interactive terminal interface for Forcefield. It is built with Bubble Tea.

## Purpose

The TUI is a presentation layer. It shows the conversation, accepts user input, runs slash commands, and streams runtime events into the transcript. It does not own agent logic, provider logic, or tool logic.

## Start Path

```go
func Start(sess *session.Session) error
```

1. Load configuration for header labels.
2. Create a Bubble Tea program with the chat model.
3. Enable the alternate screen and mouse cell motion.
4. Run until the user exits.

The CLI starts the TUI from:

- `ff` with no subcommand
- `ff chat`
- `ff --resume <session-id>`

## Main UI Parts

| Part              | Role                                                      |
| ----------------- | --------------------------------------------------------- |
| Header            | Shows agent, provider, and model labels.                  |
| Transcript        | Scrollable conversation history.                          |
| Input box         | Text input for prompts and slash commands.                |
| Spinner / status  | Shows that a model run is in progress.                    |
| Session picker    | Modal list of saved sessions from `/sessions`.            |
| Banner / styles   | Visual presentation helpers.                              |

## Input Handling

When the user submits a line:

1. The TUI first tries slash-command dispatch.
2. If the line is a command, the command runs through the command context.
3. If the line is not a command, the TUI treats it as a chat prompt.
4. The prompt is added to the session and sent through the runtime stream.
5. Streamed text, tool activity, and the final response update the transcript.
6. The session is saved locally.

## Command Integration

The TUI implements `command.Context`.

Through that interface, commands can:

- print status messages
- clear the transcript
- quit the program
- read or switch model and provider
- open the session picker

Built-in commands are registered when the chat model is created.

## Streaming

The TUI consumes `runtime.Event` values:

| Event             | TUI behavior                                      |
| ----------------- | ------------------------------------------------- |
| `EventThinking`   | Show waiting or thinking status.                  |
| `EventText`       | Append assistant text to the live buffer.         |
| `EventToolStart`  | Show that a tool started.                         |
| `EventToolFinish` | Show tool completion or tool error detail.        |
| `EventDone`       | Finalize the assistant message and save session.  |
| `EventError`      | Show the error and stop waiting.                  |

## Session Picker

The `/sessions` command loads saved sessions and opens a picker modal.

- Sessions are sorted by last update time.
- Each entry shows a short title from the first user message.
- Selecting a session switches the active conversation in the TUI.

## Design Notes

- The TUI is thin. Business logic stays in runtime, command, session, and tools.
- Markdown rendering improves readability of assistant replies.
- The interface targets a local terminal workflow, not a web UI.
