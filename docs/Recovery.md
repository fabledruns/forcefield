# Recovery

Packages: `internal/recovery`, `internal/process`, `internal/session` (Turn envelope)

Forcefield treats the session file as the only persistent run state.
The runtime loop is stateless across processes; both the interactive TUI
and headless resume (`ff run --resume`) persist the same event stream
through one shared driver, so crash semantics are identical.

## Run-recovery contract

1. Assistant batch + pending tool calls persist **before** tools run.
2. Tool results + pending-call resolution persist together.
3. Turn close (`EndTurn`) persists on every terminal event.
4. Adoption heals stranded turns via the existing session recovery.
5. Replay reads `ProviderMessages` from the persisted session.
6. Recorded calls are never re-executed.

Crash honesty is load-bearing: if the process dies after a tool side
effect but before its result persists, recovery marks the call
`interrupted` and synthesizes a cancelled result. It never claims to
know whether the side effect happened and offers no exactly-once
guarantee.

There is no supervisor in the recovery package: exit codes tell a
future supervisor what happened, and `Budget` bounds how often it may
retry.

## Exit codes

| Code | Name | Meaning |
| ---- | ---- | ------- |
| 0 | `ExitOK` | Ran to `EventDone`. |
| 2 | `ExitTerminal` | Runtime-enforced stop (`EventBlocked`) or non-transient failure. Needs a human fix. |
| 3 | `ExitRetryable` | Transient interruption (rate limit, 5xx, timeout, connection). Safe for supervised restart. |
| 4 | `ExitNeedsHuman` | Cancelled or stalled on denials. Never auto-restarted. |
| 1 | Supervisor failure | Spawn/wait failure or unknown child code. Never a run outcome. |

Classification reuses `providers.IsTransient`; there is no second
retryability system. Done wins over earlier denials; denied-only
blocked/error runs report `ExitNeedsHuman`; unknown outcomes fail
closed to `ExitTerminal`. Quota/billing, auth, invalid requests, and
protocol errors are never transient.

## Supervision

`Supervise` runs child attempts until one parks. Only `ExitRetryable`
restarts while the budget allows; success, terminal failure,
cancellation/denial, spawn/wait failures, and unknown codes all stop
immediately. An exhausted budget stops with the last code.

The supervisor loop stays in-memory and exact. `NoteSupervisorRestart`,
`NoteSupervisorExhausted`, and `ClearSupervisor` mirror its counter
into `Session.SupervisorState` so the budget survives the supervisor
process being killed and restarted:

- Record a committed retry before the backoff wait, so a kill during
  backoff resumes with remaining budget.
- Latch exhaustion; a latched episode refuses further restarts.
- Clear on terminal child outcomes (0/2/4); terminal failures never
  accumulate retry state.

Helpers are nil-session safe and set values absolutely so replay
converges. They run synchronously between child exit and the next
spawn; a kill in that window leaves either the pre- or post-write
file, both valid. There is no locking: concurrent supervisors must
not share a session, and lifecycle writes never touch `Messages` or
`Turn`.

## Process-tree lifecycle

Package `internal/process` owns teardown for every external command
Forcefield spawns: cancellation or timeout must kill the whole tree,
not just the direct child, and a violent death of Forcefield itself
must still reap the Windows-side tree.

- `Kill` is the synchronous tree kill (taskkill `/T /F` on Windows,
  SIGKILL to the process group on Unix).
- `Track` is the Windows Job Object backstop (`KILL_ON_JOB_CLOSE`,
  released after `Wait`); on Unix it is a no-op because group
  membership is established before `Start` and inherited reliably.
- `Run` isolates (`Configure`), tracks, and on cancellation
  terminates cooperatively first, then kills after a grace period.

Scope: OS processes spawned directly (shell jobs, supervised
children). Linux-side processes inside the WSL distribution outlive
the `wsl.exe` relay by platform design; see [Sandbox](Sandbox.md).
Short-lived probers with no known descendants intentionally skip
`Track`.
