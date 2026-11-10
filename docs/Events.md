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
without a `chain`) are dropped by the writer rather than recorded. `child_text`
events are dropped by the writer too — they carry response content, which the
log never holds.

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
| `agent_spawned` | `agent`, `chain`, `depth`, `child_run`, `call_id`; batch children add `batch_index` |
| `child_progress` | `agent`, `chain`, `depth`, `child_run`, `call_id`, `turns`; batch children add `batch_index` |
| `child_text` | `agent`, `chain`, `depth`, `child_run`, `call_id`, `text`; batch children add `batch_index`. Never persisted (see below) |
| `child_completed` | `agent`, `chain`, `depth`, `child_run`, `call_id`, `outcome`, `turns`, `tool_calls`, `failed`, `denied`, `duration_ms`, `error` (when the child failed or was cancelled); batch children add `batch_index` |
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

### Child events and native batch delegation

A **native batch delegation** is one `delegate` call that carries a `tasks`
array. The runtime launches every declared task as its own child run, and each
task's events carry `batch_index`: the 1-based position of the task in the
declared array. Two tasks of the same batch never share an index, a `child_run`
id, or a chain — a batch child's chain leaf is the agent name plus its index
(`coding#1`, `coding#2`), so two tasks targeting the same agent stay distinct
correlation keys.

Child tool activity is published through the same sink: a child's own tool
calls appear as `tool_started` / `tool_completed` / `tool_failed` /
`tool_denied` / `tool_cancelled` events carrying the child's `child_run` and
`chain`. Only the tool's name, duration, and outcome travel — never its
arguments, never its output. The parent run's own tool events come from
`cmd/run.go` and carry no `child_run`; the two sets are told apart by that
field.

### Live child text is never persisted

`child_text` carries the child's own streamed text so a live view can show it.
The headless writer **drops every `child_text` event**: the log's privacy
contract excludes response content, and the live view installs its own sink
(the TUI's), so nothing is lost by keeping the text out of the file. The
runtime scrubs the text before it is published, so even the live view never
sees a registered secret.


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
- `chain` — delegation path, `"general > coding"` style. A native batch
  delegation's children carry a distinct leaf per task (`general > coding#1`,
  `general > coding#2`).
- `depth` — delegation depth at spawn.
- `batch_index` — 1-based position of a child inside a native batch delegation.
  Absent (zero) for every event outside batch delegation.

### Lifecycle guarantees (Phase 4 audit)

The Phase 4 adversarial lifecycle audit returned a **conditionally safe**
verdict. Its findings are preserved here verbatim; they remain accurate
after Phase 5.

**Execution identity and sink lifecycle isolation (v1.6.x hardening).**
Every orchestration execution is bound to an immutable execution context:
the runtime's root scope captures the active sink snapshot and generates a
unique `run` execution ID at `StreamChat` entry. Spawned children inherit
both the captured sink and execution ID. Emissions flow exclusively through
the scope's captured sink, preventing in-flight children of an old run from
ever emitting into a newer run's sink after sink re-attachment.

**Sink invalidation on reset.** Each TUI sink instance (`internal/tui/tui.go`,
`tuiOrchSink`) has an immutable orchestration generation frozen at creation.
When a reset (`/clear`, `/new`, session switch) occurs, `bumpOrchGen` closes
the previous sink instance (rendering future emissions silent no-ops and
draining residue with the old stamp) and instantiates a brand-new sink for the
incremented `orchGen`. The model's `Update` gate drops any batch whose generation
does not match the active `orchGen`.

**Correlation isolation with execution identity.** `internal/tui/agentsstate.go`
`orchAnchor` gates on execution ID (`RunID`): an event can only anchor to a
row if their run IDs match. Provider call-ID reuse is structurally defended:
reused tool-call IDs across runs produce distinct rows isolated by `RunID`.
Nested delegations verify that a candidate parent row shares the event's `RunID`,
preventing old nested children from anchoring to unrelated parent rows in a new session.

**Terminal state immutability.** The first terminal outcome observed for a row
(whether via the stream feed or the structured orch feed, or via `cancelOpen`)
is immutable. Late progress, live child text, or completion events never reverse,
reopen, or rewrite terminal status or output.

**Dedicated adversarial lifecycle test suite.** All eight lifecycle scenarios
identified during audit now have dedicated, deterministic tests using channel
barriers without timing heuristics:
1. *Re-attach racing an unwinding run end to end:* `TestOrchLifecycle_ReattachRacingUnwind` (`internal/tui`), `TestOrchestrationScopeSinkIsolation` (`internal/runtime`).
2. *Cross-run call-ID reuse collision:* `TestOrchLifecycle_CallIDReuseCollision` (`internal/tui`).
3. *Cross-feed first-terminal-wins under concurrent feeds:* `TestOrchLifecycle_CrossFeedFirstTerminalWins` (`internal/tui`).
4. *`cancelOpen` racing late structured events:* `TestOrchLifecycle_CancelOpenRacingLateEvents` (`internal/tui`).
5. *Transcript collapse dedup across resets:* `TestOrchLifecycle_DrainCollapsedDedupAcrossResets` (`internal/tui`).
6. *Bounded-queue drop accounting under mixed pressure:* `TestOrchLifecycle_BoundedQueueDropAccounting` (`internal/tui`).
7. *Generation mismatch through the headless writer:* `TestOrchLifecycle_HeadlessWriterRunAttribution` (`internal/orchevents`).
8. *Nested-event anchoring after a reset:* `TestOrchLifecycle_NestedEventAnchoringAfterReset` (`internal/tui`).

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
Live child text (`child_text`) is never written to the log at all.

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
