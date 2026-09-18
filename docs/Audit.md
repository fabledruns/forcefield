# Forensic Hardening Audit

Forensic, code-first audit of Forcefield's runtime, scheduler, session,
provider, filesystem, and process lifecycle. Every finding below was
established from the actual source at the audited baseline — not from
commit messages, prior reports, or the README. Each fix ships with a
regression test verified to fail before and pass after.

- Audited baseline: `083166d` (`fix(session): harden persistence errors and replay bounds`)
- Fixes landed in: `1612afc` (`fix(runtime): close confirmed audit failure modes`, C1–C4) and `083166d` (H3-A, H3-C)
- Method: end-to-end execution tracing (user input → session → model turn → tool execution → history → persistence → cancellation/cleanup), failure-condition attack on each stage, existing-test review before judging any suspected bug.

## Confirmed findings and fixes

| ID | Failure | Fix | Regression test |
|---|---|---|---|
| C1 | `shell_job start` built its process on the scheduler's per-attempt context, which is cancelled the moment `Start` returns — killing every background job at birth. | Detach with `context.WithoutCancel` for process construction; lifetime stays governed by deadline, explicit cancel, TTL, and the `RunControl` watcher. | `TestScheduler_ShellJobStartSurvivesPerAttemptCancel` (real scheduler path; fails pre-fix on the captured context). |
| C2 | OpenAI-compatible, Anthropic, Gemini, and Responses adapters treated bare EOF without a terminal marker as successful `FinishStop`, committing truncated turns. | EOF without the protocol terminal (`[DONE]`/finish reason, `message_stop`, `finishReason`, `response.completed/incomplete`) now surfaces a `protocolError`. | `Test{OpenAICompatible,Anthropic,Gemini,Responses}_TruncatedStreamIsError`; existing `TestNvidiaStreamFlushesToolCallsOnEOF` updated (it pinned the old behavior; Nvidia shares the OpenAI transport). |
| C3 | Gemini mints tool-call IDs from a process-local counter restarting at `call-1`, colliding with persisted IDs after resume and silently dropping new calls from history. | UUID-based synthetic IDs (`call-<uuid>`); both mint sites share it. | `TestSyntheticCallIDsSurviveRestart` + session resume persistence test. |
| C4 | No panic recovery on run/scheduler paths: one panicking tool crashed the process and skipped `cleanup.Wait`. | Narrow boundaries only: scheduler workers convert a tool panic to a failed result (semaphore/`WaitGroup` order preserved); the run goroutine converts orchestration panics to `EventError` with bounded cleanup wait. | `TestScheduler_PanickingToolFailsCallNotProcess`, `TestRun_ProviderPanicBecomesError` (pre-fix binary crashes). |
| H3-A | Steady-state TUI saves are fire-and-forget; a mid-run failure sat silently in `LastSaveError`, and invalid-ID `Save` failures never recorded it at all. | TUI warns once per distinct error at teardown; `Save` records `LastSaveError` on every failure path. Headless resume gate and whole-file-write durability unchanged. | `TestStopStreamWarnsOnSaveFailureOnce`, `TestStopStreamWarnsAgainAfterRecovery`, `TestSaveInvalidIDRecordsLastSaveError`. |
| H3-C | Runs truncate tool results to 6 KiB for provider history, but sessions persist up to 48 KiB and replayed the full record — resume silently widened context. | Single source of truth (`session.MaxModelToolResultChars` / `TruncateModelToolResult`); `ProviderMessages` replays the model-visible window. Persisted record and transcript unchanged. | `TestProviderMessages_ReplayMatchesModelVisibleWindow` (byte-identical replay), `TestProviderMessages_ReplayStaysBounded`. |

## Investigated, intentionally unchanged

- **H1 — filesystem TOCTOU.** Lexical shapes, pre-existing symlinks/junction chains, drive/UNC forms, and creation ops are all handled (suites pass on Windows). A same-uid loop-swapper demo leaked ~0.3% of `read_file` calls, so the check→use race is real — but exploiting it needs same-user workspace write access (already full privileges), a blind microsecond race, and approval-visible orchestration, while `shell` gives direct access one approval away. A race-free cross-platform fix needs substantially larger OS-specific machinery (openat2/dirfd chains, handle-final-path verification per OS). Not a confirmed vulnerability under the current threat model; no change made.
- **H2 — scheduler/runMu/cancellation.** Fully traced (runMu, workers, semaphores, `RunControl`, emit backpressure, provider gates, job reaper, TUI teardown, all 8 mutexes — no lock-order inversion). Stress probes (10× cancel→replace storm, mid-batch cancel, cancel with live background job) all pass. One genuine scheduling race found (`EventCancelled` non-blocking send can drop under load) but proven harmless: headless falls back to `ctx.Err()`, and the TUI's `EndTurn` guard rejects the late `Done`. No change made.
- **H3-B — concurrent same-session saves.** Last-writer-wins is documented design (`session.go`, `supervisor_state.go`, `TestSameSessionConcurrentSaveLastWinsDocumented`); sessions are single-owner, writes are atomic (never torn). No change made.

## Residual limitations

- A SIGKILL-immune subprocess (e.g. uninterruptible sleep) can hold `cleanup.Wait`/`runMu`; no userspace design can reap it.
- Custom tools/askers/providers that ignore `context` can stall their batch; all built-ins are bounded and cooperative.
- Same-session concurrent writers last-wins by design; concurrent supervisors must not share a session.
- `process.Run`'s post-`Kill` wait prefers hang-over-orphan by design.

## Verification

- Every new test fails pre-fix and passes post-fix (checked via targeted stash/revert).
- `go test ./...`: all packages pass. `go vet ./...`: clean. `gofmt -l`: clean.
