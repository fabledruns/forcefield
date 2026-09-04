# Session

Package: `internal/session`

The `session` package manages chat sessions, message history, local persistence, and conversion of session messages into provider message format.

## Purpose

A session is one conversation. Forcefield stores sessions as local JSON files so you can list them later and continue a previous chat.

## Types

### `Message`

| Field        | Type                  | Description                                                                 |
| ------------ | --------------------- | --------------------------------------------------------------------------- |
| `Role`       | `string`              | Message role, for example `user`, `assistant`, or `tool`.                  |
| `Content`    | `string`              | Message text (or tool result).                                             |
| `Time`       | `time.Time`           | Time when the message was added.                                           |
| `ToolCalls`  | `[]providers.ToolCall`| Assistant tool calls in this turn (`omitempty`, backward compatible).      |
| `ToolCallID` | `string`              | Tool result linkage (`omitempty`).                                         |
| `Name`       | `string`              | Tool name for `tool` role (`omitempty`).                                   |

Old session files containing only `role`/`content`/`time` continue to load because the new fields are `omitempty`.

### `Session`

| Field       | Type        | Description                              |
| ----------- | ----------- | ---------------------------------------- |
| `ID`        | `string`    | Unique session ID.                       |
| `CreatedAt` | `time.Time` | Session creation time.                   |
| `UpdatedAt` | `time.Time` | Last update time.                        |
| `Agent`     | `string`    | Active specialised agent (`omitempty`).  |
| `Messages`  | `[]Message` | Ordered message history.                 |

Old session files without `agent` continue to load; callers treat `""` as `general`.

## Storage Location

Sessions are stored under the current working directory:

```text
.forcefield/sessions/<session-id>.json
```

Example:

```text
.forcefield/
└── sessions/
    ├── a1b2c3d4-....json
    └── e5f6g7h8-....json
```

## Functions

### `New`

```go
func New() *Session
```

Creates a new session with a unique ID, current timestamps, and an empty message list.

### `AddMessage`

```go
func (s *Session) AddMessage(role, content string)
```

Appends a message and updates `UpdatedAt`.

### `Save`

```go
func (s *Session) Save() error
```

Writes the session to disk as indented JSON. The function creates the sessions directory if needed and updates `UpdatedAt`.

The write is atomic: data goes to a temporary file in the same directory, is flushed, and is then renamed over the real file. If Forcefield is killed during a save, readers see either the previous complete file or the new complete file — never a truncated one.

### `Load`

```go
func Load(id string) (*Session, error)
```

Reads one session file by ID. IDs containing path separators or dot segments are rejected. A corrupted session file is reported with an error that names the file; the file itself is never modified or deleted.

### `List`

```go
func List() ([]Session, error)
```

Reads all session JSON files from the sessions directory. One unreadable or malformed file does not fail the listing: healthy sessions are returned and broken files are skipped. Use `ListCorrupt` to get the skipped files as well. If the directory does not exist, the function returns an empty list.

### `ListCorrupt`

```go
func ListCorrupt() ([]Session, []Corruption, error)
```

Like `List`, but also returns every file that could not be read or parsed, so callers can surface exactly what was skipped instead of silently losing sessions.

### `ProviderMessages`

```go
func (s *Session) ProviderMessages() []providers.Message
```

Converts session messages into provider messages for a model call, preserving `ToolCalls`, `ToolCallID`, and `Name` when present so `/resume` can replay tool history with fidelity. Old files without those fields still convert correctly.

### `AddProviderMessage` / `AddAssistantToolCalls` / `AddToolResult` / `AppendToolCallToLastAssistant`

Helpers that persist tool-call state with fidelity:

- `AddProviderMessage(providers.Message)` — stores any provider message verbatim.
- `AddAssistantToolCalls(content string, calls []providers.ToolCall)` — stores an assistant turn with tool calls.
- `AddToolResult(toolCallID, name, content string)` — stores a `tool` result linked to a call.
- `AppendToolCallToLastAssistant(call providers.ToolCall, content string)` — appends to the last assistant batch (used by the TUI to coalesce concurrent tool calls per turn).

## How the CLI Uses Sessions

| Command / flag           | Behavior                                      |
| ------------------------ | --------------------------------------------- |
| `ff` or `ff chat`        | Starts a new session.                         |
| `ff --resume <id>`       | Loads an existing session by ID.              |
| `ff --agent <name>`      | Overrides the session's stored agent.         |
| `/sessions` in the TUI   | Lists saved sessions and opens a picker.      |
| `/agent [name]` in TUI   | Lists agents or switches (persisted to session). |

Switching sessions restores the target session's stored agent (fallback to `general` with a notice if unknown).

## Design Notes

- Session data stays on the local machine.
- Each session file is independent JSON.
- Saves are atomic, so a crash cannot corrupt the session store.
- Corrupted files are reported and skipped, never silently deleted or rewritten.
- The package does not talk to the model. It only stores and converts history.
