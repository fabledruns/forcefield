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
| Provider picker   | Modal list of providers from `/provider`, with capabilities and availability per row. |
| Model picker      | Modal list of models for the active provider.             |
| Banner / styles   | Visual presentation helpers.                              |

### Provider and Model Pickers

The provider picker lists every configured or known provider. Each row shows the service name plus a detail line describing scope, capabilities, and availability, e.g.:

```text
› Ollama
    local · tools · streaming · reasoning
  OpenAI
    cloud · tools · streaming · api key missing
```

Availability is checked without network I/O: a cloud provider whose API key cannot be resolved is marked unavailable instead of failing silently later. Selecting a provider switches immediately - no restart - and opens the model picker when more than one model is known. The pickers are presentation only; they read summaries from the runtime and switch through it.

#### Automatic Model Discovery

When the model picker opens for a provider whose listing is not cached (or has expired), Forcefield fetches the provider's models in the background:

```text
OPENAI

● custom-model          ← your configured model always leads
  gpt-5.6
  gpt-5.6-mini
↻ Refresh models
Fetching models…       ← non-blocking status while the request runs
```

- The picker never freezes: navigation and Esc work during the fetch.
- When results arrive, rows are replaced by the discovered list, sorted deterministically with your active model first.
- If discovery fails (no API key, unreachable endpoint, timeout, malformed response), the previously visible models stay listed and a concise warning line explains what happened.
- Successful listings are cached in memory for ~10 minutes, so reopening `/model` is instant until then.
- `r` or the "Refresh models" row forces a fresh request that bypasses the cache and keeps your highlighted selection when it still exists.

Discovery is lazy: nothing is fetched at startup or for providers you never open. See [Providers](Providers.md) for protocol details per service.

## Input Handling

When the user submits a line:

1. The TUI first tries slash-command dispatch.
2. If the line is a command, the command runs through the command context.
3. If the line is not a command, the TUI treats it as a chat prompt.
4. The prompt is added to the session and sent through the runtime stream.
5. Streamed text, tool activity, and the final response update the transcript.
6. The session is saved locally.

## Mouse Support

Mouse events route through one centralized interaction layer (`mouse.go`).
Rendering registers hit regions; incoming events resolve against them, in
a fixed precedence: permission prompt → open modal → wheel scrolling →
transcript blocks (tool/thinking) → footer (suggestions, input box). Events
that land on nothing fall through untouched.

| Interaction                        | Action                                        |
| ---------------------------------- | --------------------------------------------- |
| Wheel up/down                      | Scroll transcript (3 lines per notch).         |
| Click a `…` tool block             | Toggle that block's detail view.               |
| Click a Thinking header            | Toggle that reasoning block.                   |
| Click a row in a picker modal      | Choose it (same as keyboard Enter).            |
| Wheel over a picker modal          | Move its selection cursor.                     |
| Click the input box                | Focus the editor.                              |
| Click `/command` suggestion        | Complete the input with it.                    |
| Click an answer label while a permission prompt is open | Same as pressing that key (y/n/a/d). |
| <kbd>F2</kbd>                      | Toggle mouse capture off/on.                    |

Text selection: with mouse capture on (default), native terminal
selection needs Shift+drag on most terminals; press **F2** to hand mouse
events back to the terminal entirely for ordinary click-drag selection.
Keyboard behavior is identical in both modes. Hover feedback is subtle
(underlined block headers, highlighted permission labels) and only
refreshes on events Forcefield receives; no all-motion tracking is used.

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
