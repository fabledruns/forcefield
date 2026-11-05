# TUI

Package: `internal/tui`

The `tui` package provides the interactive terminal interface for Forcefield. It is built with Bubble Tea.

## Purpose

The TUI is a presentation layer. It shows the conversation, accepts user input, runs slash commands, and streams runtime events into the transcript. It does not own agent logic, provider logic, or tool logic.

## Start Path

```go
func Start(sess *session.Session) error
```

1. Apply dynamic terminal tab branding (`Forcefield`) and arm restoration cleanup.
2. Load configuration for header labels.
3. Create a Bubble Tea program with the chat model.
4. Enable the alternate screen and mouse cell motion.
5. Run until the user exits.
6. Restore previous terminal title on exit (normal quit, error, signal, cancellation).

The CLI starts the TUI from:

- `ff` with no subcommand
- `ff chat`
- `ff --resume <session-id>`

## Dynamic Terminal Tab Branding

Forcefield dynamically brands the active terminal tab or window title:

- **Startup**: Sets the active tab title to `Forcefield` via standard OSC 2 sequences (`\x1b]2;Forcefield\x07`) and Windows Console APIs (`SetConsoleTitleW`). On xterm-compatible terminals, it also saves the current title to the title stack via `\x1b[22;0t`.
- **Shutdown & Restoration**: When Forcefield exits—normally (e.g. `/quit`, `Ctrl+C`), on runtime errors, on cancellation/interrupts (`SIGINT`/`SIGTERM`), or during panic unwinding—it restores the original terminal title captured prior to launch. Terminals supporting the xterm title stack pop the previous title (`\x1b[23;0t`), and Windows hosts restore the captured console title.
- **Terminal Compatibility Detection**: Escape sequences are emitted only after verifying that output is connected to an interactive terminal (`IsTerminal`). Sequences are withheld when redirected to files/pipes or running under dumb terminals (`TERM=dumb`). Windows Terminal is detected via the `WT_SESSION` environment variable.
- **Zero TUI Interference**: Branding initialization precedes Bubble Tea's alternate screen transition (`tea.WithAltScreen()`), and restoration executes after Bubble Tea has fully restored the terminal. No title query sequences (which could leak into stdin input) are used, ensuring zero interference with Bubble Tea's input reader, alternate screen, or rendering.
- **No Persistent Modification**: Forcefield never touches user shell profiles (e.g. `$PROFILE`), registry keys, or Windows Terminal `settings.json`.

### Tab Icon Integration & Platform Boundaries

- **Dynamic In-Band Switching Limitation**: Windows Terminal does **not** support dynamically altering an active tab's icon graphic from within a running process via in-band OSC escape sequences (Microsoft Terminal Issue #1868: *"Suggestion: ability for applications to set their tab icon"*, an open feature request). Standard xterm OSC 0 and OSC 1 sequences only set textual icon labels (the window title when minimized), not graphical icon images. Candidate sequences such as `OSC I`, `OSC 10000`, and vendor extensions are not parsed as tab icons by Windows Terminal's Virtual Terminal engine (`OutputStateMachineEngine.cpp`).
- **Official JSON Fragment Extension**: Windows Terminal supports native tab icons for applications through its **JSON Fragment Extensions** system (`%LOCALAPPDATA%\Microsoft\Windows Terminal\Fragments\Forcefield\forcefield.json`).
- **Profile Icon Packaging**: Forcefield packages and ensures this extension with `assets/logo.png`. When launching Forcefield in Windows Terminal via the tab dropdown (`+ -> Forcefield`) or via `wt -p Forcefield`, Windows Terminal displays `logo.png` directly on the tab header.
- **Settings Safety**: Like all Forcefield components, this extension is self-contained under the `Fragments` directory and does not edit, rewrite, or corrupt the user's `settings.json` or PowerShell profile.

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
| System output     | Command results with headings, key/value, command, path, status, and diff hierarchy plus inline code/number/reference styling; long blocks collapse. |
| Agents panel      | Live delegate-children block above the input box while children run. |
| Banner / styles   | Visual presentation helpers.                              |

The agents panel appears only while at least one delegate child is queued
or running, and it renders purely from observed runtime events (never a
predicted or invented state): one bounded row per child, labeled
`subagent-N`, with a state glyph, the child agent, the run state, and the
turn count. Turn counts show the observed progress with a `+` suffix when
the child's progress budget hid turns (`2+ turns` = at least two observed).
Narrow terminals drop the task column and switch to ASCII glyphs. When a
child reaches a terminal state its row leaves the panel and collapses into
a single transcript entry (`subagent-1 completed: status=partial,
turns=7`), so the transcript never floods. The panel resets on `/clear`,
`/new`, and session switches, and always describes the active session.

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

Typing `/` opens the command palette above the input: typing filters
commands, <kbd>↑</kbd>/<kbd>↓</kbd> move the highlight (wrapping), and
<kbd>Enter</kbd> or <kbd>Tab</kbd> confirms it into the input. A second
<kbd>Enter</kbd> runs it. <kbd>Esc</kbd> clears the input and closes the
palette.

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
| Click a palette row                | Complete the input with that command.          |
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
- read or switch model, provider, and specialised agent
- open the session picker

The header shows `provider · model · session(agent)` where the agent label reflects `runtime.CurrentAgent()`. `/agent [name]` switches agents: the active stream is cancelled first so the switch never happens mid-turn, the header updates only on success, and `session.Agent` is persisted. Switching sessions via `/sessions` also restores the target session's stored agent.

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

## Concurrency and teardown

- Permission asks run on the scheduler goroutine and block only that
  tool call; the UI goroutine keeps rendering. At most one prompt is
  pending, and it renders inline (transcript line + footer UI) rather
  than taking over the screen.
- Stopping a stream cancels the generation, retires it, and repairs
  stranded `tool_calls` via the shared session recovery; teardown
  paths (`/clear`, agent/session switch, quit) mark open turns
  `cancelled`.
- Transcript rendering is cached and incremental; viewport follow
  preserves scrolled-up position during streaming.

## Design Notes

- The TUI is thin. Business logic stays in runtime, command, session, and tools.
- Markdown rendering improves readability of assistant replies.
- The interface targets a local terminal workflow, not a web UI.
