# Events

`docs/Events.md` documents the stable JSONL run-evidence log emitted by Forcefield
headless runs. The stream schema is contract version 1 (`orchevents.Version`,
`internal/orchevents/events.go`). Every assertion below is verified against the
named source files; this description tracks observed behavior as of the current
tree.

Draft authored by a delegated docs child (wave-2c); field tables, exit codes,
writer semantics, and examples corrected by parent-side source review where the
draft contradicted the emitters.

## One log per headless run

Each headless run writes exactly one log file:

```
.forcefield/sessions/<session-id>.events.jsonl
```

The path is computed by `internal/session/manager.go` (`Session.EventLogPath`) and
places the log beside the session JSON inside the same session directory. `ff run`
opens it via `orchevents.NewWriter` (`cmd/run.go`) and installs the writer as the
runtime's orchestration event sink.

Two properties follow from that placement:

- The log is evidence, not configuration. Nothing reads it to decide how a run
  behaves; it is an append-only record of what happened.
- `ff --resume` loads sessions independently, so a resumed run gets its own fresh
  log (the writer generates a fresh run id when none is supplied,
  `orchevents/writer.go`).

## Line schema

Every line is one JSON object with the fields defined by `orchevents.Event`
(`internal/orchevents/events.go`). Zero-valued and empty fields are omitted
(`omitempty`), which is why real lines vary: a `tool_failed` with no measured
duration carries only `call_id` and `tool`.

Base fields stamped by the writer on every line (`internal/orchevents/writer.go`):

- `seq` — 1-based, gap-free per file, assigned by the writer.
- `ts` — RFC3339Nano timestamp in UTC.
- `run` — run id; the writer's own id, so child events also carry it (meaning: all
  events of one process run), plus `child_run` for child-attribution.

Free-text fields (`error`, `reason`, `status`) are scrubbed defensively via
`redact.Scrub` before persistence; invalid events (unknown type, child event
without a `chain`) are dropped by the writer rather than recorded.

### Event table

| Event | Fields |
| --- | --- |
| `run_started` | `agent`, `task_chars` |
| `run_completed` | `status`, `outcome` (`done`), `duration_ms`, `exit_code` |
| `run_failed` | `status`, `outcome` (`error`), `duration_ms`, `exit_code`, `error` |
| `run_cancelled` | `status`, `outcome` (`cancelled`), `duration_ms`, `exit_code`, `error` |
| `run_blocked` | `status`, `outcome` (`blocked`), `duration_ms`, `exit_code`, `error` |
| `delegation_requested` | `agent` (target), `chain`, `call_id`, `task_chars` |
| `delegation_accepted` | `agent`, `chain`, `call_id`, `task_chars` |
| `delegation_rejected` | `agent`, `chain`, `call_id`, `reason`; budget-driven rejections add `budget`, `budget_limit`, `budget_value` |
| `agent_spawned` | `agent`, `chain`, `depth`, `child_run`, `call_id` |
| `child_progress` | `agent`, `chain`, `depth`, `child_run`, `call_id`, `turns` |
| `child_completed` | `agent`, `chain`, `depth`, `child_run`, `call_id`, `outcome`, `turns`, `tool_calls`, `failed`, `denied`, `duration_ms`, `error` (when the child failed or was cancelled) |
| `tool_started` | `call_id`, `tool` |
| `tool_completed` | `call_id`, `tool`, `duration_ms`, `attempt`; adds `error` when the result itself was an error outcome |
| `tool_failed` | `call_id`, `tool`, `duration_ms`, `attempt`, `error` |
| `tool_denied` | `call_id`, `tool`, `duration_ms`, `attempt`, `error` (no `reason` field is emitted) |
| `tool_cancelled` | `call_id`, `tool`, `duration_ms`, `attempt` |
| `budget_exhausted` | `budget`, `budget_limit`, `budget_value`, `reason` |

Sources: `internal/orchevents/events.go` for the field set; `cmd/run.go`
(`mapRunEvent`, `mapToolTerminal`, terminal classification) for tool and final-event
fields; `internal/runtime/delegation_tool.go` for delegation and child fields.
All four tool terminal events share one shape — `tool_completed` with `IsError`
promotion to `tool_failed` included — and a shell-shaped exit code rides
`meta.exit_code` (`cmd/run.go:mapToolTerminal`).

### Outcome vocabulary

`outcome` is closed (`internal/orchevents/events.go`): `done`, `error`, `blocked`,
`cancelled`. The final event's `status` carries the verification status
(`verified`, `partial`, `blocked`, `failed`), independent of `outcome`.

### Correlation

Use these fields to stitch lines together:

- `run` — every event of one process run shares the writer's id, including child
  events.
- `child_run` — the fresh UUID of the spawned child run; appears on `agent_spawned`
  and every child event. Child runs correlate through `child_run` + `chain`
  (`internal/orchevents/writer.go`).
- `call_id` — the delegate tool call id links `delegation_requested` /
  `delegation_accepted` / `delegation_rejected` / `agent_spawned`, and the tool
  lifecycle links `tool_started` with its terminal event.
- `agent` — the target agent of a delegation; on tool events the tool name rides
  `tool` instead.
- `chain` — delegation path, `"general > coding"` style.
- `depth` — delegation depth at spawn.

## File semantics

The writer in `internal/orchevents/writer.go`:

- Creates parent directories `0700` and the file `0600`, opening with
  `O_CREATE|O_WRONLY|O_APPEND`.
- Enforces a hard 4 MiB (`4 << 20`) byte cap. After the write that reaches the cap,
  the writer appends exactly one marker line —
  `{"type":"events_truncated",...}` with the next `seq`, a fresh `ts`, and the run
  id — and drops all later events for that file.
- Sticky first write/encode error: the first failure is recorded and returned by
  `Writer.Err` and never fails the run. The writer stays usable — later emissions
  still attempt a write — but lost durability is never retroactively restored
  (`latchLocked`). The run decides what lost evidence means for its exit code.
- Telemetry can never upgrade an outcome (`cmd/run.go`): a verified completion
  whose own event log was unusable exits unverified, printing to stderr:
  `ff run: orchestration event log write failed (<err>); session evidence may be incomplete`
  and `ff run: result marked unverified: orchestration event log unusable`.
  A failed run keeps its failure.

## Privacy

The log deliberately contains no task text, no tool arguments, no response
content, and no secrets. `task_chars` counts characters of the task, it does not
quote it (`internal/runtime/delegation_tool.go` counts runes via
`len([]rune(taskText))`). `seq`, `ts`, `run`, `child_run`, `call_id`, `agent`,
`chain`, `tool`, counters, durations, statuses, and reasons carry the story;
`redact.Scrub` defends the three free-text fields even against producer bugs.

## Exit codes on the final event

The final `run_completed` / `run_failed` / `run_cancelled` / `run_blocked` line
carries `exit_code`. The mapping is implemented in `cmd/run.go`:

- `0` — verified: `EventDone` with `StatusVerified` (a plain answer, no tools).
- `5` — unverified: any tool-using run that never self-verified, a runtime stop
  (`EventBlocked`), or a verified answer whose orchestration evidence was unusable.
- `1` — errors or cancellation (provider failure, cancelled run, persistence
  failure).

Interruption shapes the terminal event by whatever evidence exists at the moment
of interruption: cancellation reached the runtime ends with `run_cancelled`
(outcome `cancelled`, `error` attached), runtime-enforced stops end with
`run_blocked`, anything else ends with `run_failed` (outcome `error`). All final
lines carry `status` and `exit_code`; `run_failed` and `run_cancelled` attach
`error` when the terminal error is known.

## Budget events

`budget_exhausted` is emitted once per run, at the first crossing only
(`internal/runtime/orchestration.go` `orchestrationShared.consumeTurn`: charges
1-based totals, `over=true` only past the limit, `fire` exactly once). It reports
`budget` (`total_turns`), `budget_limit`, `budget_value` (the total that crossed),
and `reason`.

Turn counting is model turns, not events and not reasoning deltas:

- Child turns charge one unit per inference attempt — the runtime emits
  `EventThinking` with an empty `Thinking` field once at the start of each model
  turn (`internal/runtime/runtime_turn.go`), and the delegation recorder charges
  only that marker (`internal/runtime/delegation_tool.go`).
- Reasoning deltas (non-empty `Thinking`) are never charged.
- Per-delegation rejections (`spawn`, `depth`, `task_chars`) are `delegation_rejected`
  events with their own `budget` name; they do not exhaust the aggregate pool.

## Examples

All examples are abbreviated: base fields (`seq`, `ts`, `run`) are omitted from
the payloads below and are present on every real line.

### (a) Tool lifecycle

```json
{"type":"tool_started","call_id":"call_7f3a","tool":"read_file"}
{"type":"tool_completed","call_id":"call_7f3a","tool":"read_file","duration_ms":12,"attempt":1}
{"type":"tool_failed","call_id":"call_7f3a","tool":"read_file","error":"open docs/Events.md: no such file"}
```

### (b) Delegation lifecycle

```json
{"type":"delegation_requested","agent":"coding","chain":"general > coding","call_id":"call_9c21","task_chars":412}
{"type":"delegation_accepted","agent":"coding","chain":"general > coding","call_id":"call_9c21","task_chars":412}
{"type":"agent_spawned","agent":"coding","chain":"general > coding","depth":1,"child_run":"run_5e02","call_id":"call_9c21"}
{"type":"child_progress","agent":"coding","chain":"general > coding","depth":1,"child_run":"run_5e02","call_id":"call_9c21","turns":2}
{"type":"child_completed","agent":"coding","chain":"general > coding","depth":1,"child_run":"run_5e02","call_id":"call_9c21","outcome":"done","turns":4,"tool_calls":3,"failed":0,"denied":0,"duration_ms":1840}
{"type":"tool_completed","call_id":"call_9c21","tool":"delegate","duration_ms":1905,"attempt":1}
```

### (c) Budget exhausted

```json
{"type":"budget_exhausted","budget":"total_turns","budget_limit":50,"budget_value":51,"reason":"aggregate child-turn budget exhausted (51 turns across all children, limit 50)"}
```

### (d) Delegation rejection

```json
{"type":"delegation_rejected","agent":"coding","chain":"general > coding","call_id":"call_1b34","budget":"task_chars","budget_limit":20000,"budget_value":22100,"reason":"task is 22100 characters, exceeding the limit of 20000 - delegate a smaller, more focused task"}
```
