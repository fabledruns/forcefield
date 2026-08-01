# Session

Package: `internal/session`

The `session` package manages chat sessions, message history, local persistence, and conversion of session messages into provider message format.

## Purpose

A session is one conversation. Forcefield stores sessions as local JSON files so you can list them later and continue a previous chat.

## Types

### `Message`

| Field     | Type        | Description                          |
| --------- | ----------- | ------------------------------------ |
| `Role`    | `string`    | Message role, for example `user`.    |
| `Content` | `string`    | Message text.                        |
| `Time`    | `time.Time` | Time when the message was added.     |

### `Session`

| Field       | Type        | Description                              |
| ----------- | ----------- | ---------------------------------------- |
| `ID`        | `string`    | Unique session ID.                       |
| `CreatedAt` | `time.Time` | Session creation time.                   |
| `UpdatedAt` | `time.Time` | Last update time.                        |
| `Messages`  | `[]Message` | Ordered message history.                 |

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

### `Load`

```go
func Load(id string) (*Session, error)
```

Reads one session file by ID.

### `List`

```go
func List() ([]Session, error)
```

Reads all session JSON files from the sessions directory. If the directory does not exist, the function returns an empty list.

### `ProviderMessages`

```go
func (s *Session) ProviderMessages() []providers.Message
```

Converts session messages into provider messages for a model call. This conversion keeps only role and content for the provider path used by chat history.

## How the CLI Uses Sessions

| Command / flag           | Behavior                                      |
| ------------------------ | --------------------------------------------- |
| `ff` or `ff chat`        | Starts a new session.                         |
| `ff --resume <id>`       | Loads an existing session by ID.              |
| `/sessions` in the TUI   | Lists saved sessions and opens a picker.      |

## Design Notes

- Session data stays on the local machine.
- Each session file is independent JSON.
- The package does not talk to the model. It only stores and converts history.
