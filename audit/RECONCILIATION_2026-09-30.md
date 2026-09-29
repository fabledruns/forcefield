# Cross-Audit Reconciliation & Patch-Priority Review

## Cross-Audit Status

```text
Audits inspected: 6 files (5 reports + 1 spec)
Historical findings inventoried: 71 rows across reports
Unique root causes after dedup: 58
Confirmed current bugs: 32 (0 P0, 4 P1, 7 P2, 21 P3)
Fixed: 40 rows (several rows close multi-part claims; see Fixed section)
Partially fixed: 17 historical claims closed in part (open remainder
  listed under bugs above: SEC-001, SEC-002, SEC-004, SEC-006, SEC-010,
  SEC-012, REL-001, REL-002, CRIT-003, OBS-001, SESS-001, H01, H03, H07,
  C5, N8, atomic-perms)
Refuted: 4
Superseded: 6 claims
Duplicate: 8 groups (folded, not double-counted)
Unproven: 9 categories (see Remaining Unproven)
Stale: 2 (code path gone)
Accepted risk (documented, by design): 6
```

Methodology: every classification below was checked against files on disk
at HEAD `4a70846` (clean tree), which includes the committed P1-fix commit
(`fix: close P1 reliability and safety gaps`). Older reports' `file:line`
citations were treated as stale pointers and re-traced. Where this review
disagrees with a prior report's status, the evidence for the override is
given inline. No production code was modified in this pass; validation was
`go build ./...` (PASS) plus focused test packages (below).

Note on severity drift: several old HIGHs are downgraded below, not
because the mechanism vanished, but because later fixes changed the
preconditions (default-ask, per-command Always scope, fail-closed
exits, supervised resume). The downgrade reasoning is explicit per item.

## Phase 4 - The Three Just-Fixed P1s (re-verified on HEAD)

**P1-1 fail-closed persistence: CLOSED.**
`internal/recovery/driver.go:205-249` (sticky `persistFailed`/`persistErr`
latch, one-shot `OnPersistFailure` hook, `notePersisted()` called after
tool-start at `:302`, tool-terminal at `:309`, terminal teardown at
`:323`); `cmd/run.go:221-228` (child context + hook wired to `cancel`;
runtime exits at its next cancellation check without starting new tool
batches); adoption gate `saveGateError` at `:248-253`, used at `:212-214`
and in `finishResumeSession` (`:261-270`, double-checked after settle);
driver override to `ExitTerminal` at `:237-239`; supervisor mirror
`cmd/supervise.go:278-284` returns errors and `:211-217` cancels
supervision on mirror failure instead of spending unverified budget.
Regression tests: `TestDriverPersistFailureLatchesAndNotifies`,
`TestDriverHealthyRunNeverLatches`,
`TestRun_CancelDuringToolExecutionStartsNoFurtherTools` (second tool: 0
executions, 1 model turn, `EventCancelled`),
`TestMirrorSuperviseEventSaveFailureDoesNotAdvance` (file keeps last good
budget), `TestMirrorSuperviseEventSuccessPersists`,
`TestPersistSuperviseEventSuccessPaths`,
`TestDriveSupervisionStopsWhenMirrorFails` (1 spawn, parks),
`TestSaveGateError`. Each was shown to fail against reverted behavior
during the fix pass. Residual (documented, not a bypass): tools already
in flight when the failure lands still complete; only *new* batches are
prevented. TUI keeps running with its explicit save-error warning
(interactive tradeoff).

**P1-2 verification exit: CLOSED.**
`internal/recovery/exit.go:36` (`ExitUnverified = 5`);
`Classify` (`:87-93`): `EventDone` + verified -> 0, anything else -> 5.
Driver captures `finalStatus` (`driver.go:201-204,329-335`) with
`FinalStatus()` accessor (`:366-371`). Both `ff run` paths enforce via
`exitForRunStatus` (`cmd/run.go:121,135,144-151`) - response still
printed, verdict in the exit code. Plain chat stays 0 (`FinalStatus`
reports zero-tool runs verified - untouched logic). Supervisor parks on 5
(`recovery/supervise.go:92`, `cmd/supervise.go:250,270-280`,
`settleSupervisorEpisode` in `run.go:286-290` clears the episode).
Checked for alternate Done paths: `ff` root `--resume` enters the TUI
(interactive, no exit contract); `RunContext` (status-dropping wrapper)
retained only for in-process callers. No path prints a Done response with
exit 0 except verified. Tests: Classify table (+5 unverified cases),
`TestDriverExitCodeReflectsVerification`, `TestExitForRunStatus`,
`TestRunCommand_UnverifiedPartialExitsUnverified`, supervise routing
`{"unverified stops without restart", [5], 5, 1}`, settle map. New tests
shown to fail against the old `Done->0` branch.

**P1-3 stream bound: CLOSED.**
`shellOutput` counts streamed bytes against the same budget
(`shell.go:30-48`); `emitChunk` (`:127-149`) sends one constant marker
(`:118`) on first over-budget line, then drops; mutex never held across
`onChunk`; both `streamPipe` call sites gated (`:674-683`); jobs pass nil
chunk (unaffected). Exhaustive grep proves `Shell.ExecuteStream` is the
only production `StreamingTool` implementation (MCP adapter has no
streaming path), so the bound covers 100% of production chunk sources.
Tests: `TestShell_StreamBudgetBoundsLiveOutput`,
`TestShell_SmallOutputStreamsFullyUnmarked` (normal output byte-identical,
no marker), `TestScheduler_ShellProgressBoundedByStreamBudget`
(scheduler-boundary proof; fails pre-fix with "1000 progress events for
1000 output lines"). Residual: transcript growth *across* many executions
is general TUI retention (P2 below), not this channel.

## ASAP PATCH LIST

Only currently actionable bugs. No P0 was found (see Why No P0 below).

### [P1] Provider headers silently override Authorization (secret-on-disk vector)

Location:
- `internal/providers/openai_compatible.go:342-347`
- `internal/providers/responses.go:237-242` (comment admits `explicit config header still wins`)
- `internal/providers/anthropic.go:308-313`
- `internal/providers/gemini.go:275-280`
- `internal/config/provider.go:241-245` (`isValidHeaderName` only, no deny-list)

Root cause:
Custom `Headers` are applied with `Header.Set` after the resolved API key
in all four adapters, so `headers: {Authorization: ...}` (or `x-api-key` /
`x-goog-api-key`) silently replaces the key with no warning - and stores a
live credential in `config.yaml`, defeating the env-only secret design
(`APIKey yaml:"-"`, `config.go:359-360`).

Failure scenario:
A pasted config snippet (or model-suggested "fix" that gets approved)
adds an `Authorization` header. The key now lives plaintext on disk,
survives rotation of the environment variable, and auth failures
misattribute to the wrong credential.

Impact:
Credential handling failure + operator footgun. Configuration is in the
hostile-input set, so a malicious config achieves secret persistence.

Why existing defenses do not close it:
Header-name validation checks token characters only; nothing compares
against the auth header names each adapter sets two lines above.

5-day relevance:
Yes - long-lived unattended runs authenticate thousands of times; a
shadowed credential fails closed only if noticed, and disk persistence
outlives the run.

Regression coverage:
None (no test asserts key-wins or rejection).

Recommended fix:
Reject (fail-closed, actionable error) header names colliding
case-insensitively with `Authorization`, `x-api-key`, `x-goog-api-key`,
`api-key` in config validation; optionally warn when any header value
matches a registered secret shape.

Confidence:
High

Source audits:
- audit/PARANOID_AUDIT_2026-09-29_2692c35.md (P2, confirmed verbatim on HEAD)

### [P1] `extraEnvArgs` accepts arbitrary env keys; WSL small-path passes them raw to `/usr/bin/env`

Location:
- `internal/tools/shell/shell.go:1006-1025` (`pairs = append(pairs, k+"="+s)`, no key check)
- `internal/sandbox/wsl_windows.go:187-188` (raw `req.ExtraEnv` after `/usr/bin/env`)
- `internal/sandbox/native_unix.go:35` (raw merge)
- Contrast `internal/sandbox/wsl_shared_windows.go:133-136` (`envNameRe` enforced on large path only)

Root cause:
Single choke point validates values but never keys. Model-controlled keys
(`-u`, `A=B`, empty, spaces) reach process setup; on the WSL small path
they become argv to GNU `env`, where they alter `env` semantics
(`-u`, `--split-string`, `LD_PRELOAD=x`) instead of setting variables.
Behavior also diverges by payload size (small accepted, large rejected).

Failure scenario:
Prompt-injected `env: {"-u": "PATH"}` on a small WSL command mangles the
execution environment; `PATH`/`LD_*` overwrites are accepted silently in
all modes.

Impact:
Environment-confusion primitive fed directly by hostile model output;
reaches `exec` setup on every platform.

Why existing defenses do not close it:
The `envNameRe` validator exists but is enforced on only one of two WSL
paths; native paths validate nothing; no `PATH`/`LD_*` blocklist exists
anywhere.

5-day relevance:
Yes - thousands of model-generated tool calls; one approved call with a
hostile env block poisons that execution.

Regression coverage:
None for key validation (`wsl_escape`/MCP config tests cover adjacent
name checks, not this choke point).

Recommended fix:
Validate keys with the existing `envNameRe` inside `extraEnvArgs`
(covers shell, jobs, native, both WSL paths at once) returning
`ArgumentError`; consider warning/blocking `LD_PRELOAD`/`LD_LIBRARY_PATH`/`PATH`
overwrites in permission execution facts.

Confidence:
High

Source audits:
- audit/5DAY_AUDIT_REPORT.md (FF-SEC-013, LOW - underestimated: it assumed no injection path, but the WSL small-path argv route is a real one)
- audit/RECONCILIATION.md (residual LOW)
- audit/PARANOID_AUDIT_2026-09-29_2692c35.md (P2, confirmed)

### [P1] Sensitive-path escalation ignores foreground `shell` and all MCP tools

Location:
- `internal/runtime/scheduler.go:411-439` (`isSensitiveCall`: `read_file,write_file,list_files,search_files,search_code,find_files,git,secret_scan` + `shell_job:cwd`; `default: return false`)
- `internal/runtime/scheduler.go:592-607` (escalation + session-allow bypass - unreachable for the missing cases)

Root cause:
Foreground `shell` (command text never inspected) and every `mcp__*`
tool (opaque args, adapters deliberately claim no `BoundaryChecker` -
pinned by `TestMCPNoBoundaryChecker`) bypass the forced-Ask escalation.
Defense relies solely on base defaults (`ask`).

Failure scenario:
Operator sets shell (or an MCP file tool) to `allow` for convenience;
`shell {command:"cat ~/.ssh/id_rsa"}` or an allowed MCP read of
`.aws/credentials` executes without escalation. Per-command Always scope
does not help: it narrows re-approval, it does not escalate.

Impact:
Permission-boundary gap on the exact files the escalation set exists to
protect (sandbox/permission boundaries, secret handling).

Why existing defenses do not close it:
Default-ask is a default, not a boundary - operator-allowed rules skip it,
and nothing re-checks sensitivity afterward for these two call shapes.

5-day relevance:
Yes - unattended runs operate under pre-approved rules; that is precisely
when escalation (not prompting) is the last guard.

Regression coverage:
Covered for the listed tools (`scheduler_sensitive_test.go`); no test
asserts escalation for `shell`-command text or MCP string args (i.e. the
gap is untested by construction).

Recommended fix:
Extend escalation to foreground `shell` by scanning `command`+`cwd`
against `IsSensitivePath` anchors (home-relative sensitive names to keep
precision), and to MCP calls by inspecting string arguments for
sensitive-path shapes (or require MCP tools to declare path-like fields).

Confidence:
High (absence verified); Medium on exploit frequency given default-ask.

Source audits:
- audit/PARANOID_AUDIT_2026-09-29_2692c35.md (P2 - raised to P1 here because pre-approved unattended rules are the 5-day norm, and this is the hole in exactly that configuration)

### [P1] No idle-body timeout: trickle streams hold a turn forever with no error

Location:
- `internal/providers/http.go:15,20-21` (`ResponseHeaderTimeout: 120s`, no `Client.Timeout` - pinned by `timeout_test.go:24-36`)
- Absence verified: no idle/per-chunk/total body timeout in any adapter

Root cause:
Once headers arrive, nothing bounds body progress. A 1-byte-per-119s
trickle never errors, never triggers turn retry (no failure to classify),
never yields `ExitRetryable`, and therefore never triggers supervised
restart. The 5-day budget burns with zero progress and zero signal.

Failure scenario:
Day 2: degraded provider emits slowloris-style streams; every turn hangs
indefinitely; supervisor sees healthy-but-slow children and waits;
operator returns to a run that consumed days of wall clock with nothing
to show and nothing to resume from (nothing failed, so nothing was
recorded as retryable).

Impact:
Silent bounded-progress failure - the only provider failure mode with no
detection path at all. (Header-TTFB is bounded; transients retry;
mid-stream errors classify. Trickle does none of these.)

Why existing defenses do not close it:
`Client.Timeout == 0` is intentional (long streams must survive) and every
retry/turn/supervisor mechanism keys off errors, of which a trickle
produces none.

5-day relevance:
Directly decisive for 120h operation against a flaky provider.

Regression coverage:
None (idle stall is untested; `slow_ttfb_test.go` covers the opposite
direction - slow-but-progressing bodies).

Recommended fix:
Add an idle-body timeout (no chunk for N seconds -> transient error ->
turn retry -> `ExitRetryable`) while keeping total-stream unbounded.

Confidence:
High

Source audits:
- audit/PARANOID_AUDIT_2026-09-29_2692c35.md (P2, confirmed verbatim on HEAD)

**Cross-audit verdict on the headline suspect:** a subagent-surfaced
"per-agent `system_prompt` override dropped" claim was refuted by tracing
`runtime.go:245-251` (registry merged before use) + `definition.go:150-151`
- live path redundant, not lossy. Recorded as P3 dead code, not a bug.
Details in the Refuted section.

---

## ASAP PATCH LIST - P2 (Important, contained)

### [P2] No global rate limiter; supervised restarts can stampede a recovering provider

Location:
- `internal/providers/retry.go:41-46` (per-request policy), `:319-339` (per-instance `requestGate`, fails fast, never queues)
- Absence verified by grep (no token bucket / `x-ratelimit` handling anywhere)

Root cause / scenario / impact:
Per-request backoff+jitter+Retry-After is correct, but nothing coordinates
across provider instances, discovery fetches, or supervised children. After
an outage, N sessions + discovery + restarts re-hit at full speed; the
per-instance gate fails fast (`errRequestInFlight` -> non-transient) instead
of smoothing. Realistic multi-session 5-day impact; single-session impact
bounded by budget+backoff.
Fix: process-wide token bucket / minimum-interval limiter shared by
inference + discovery + supervised children, honoring `Retry-After`
globally. No regression test exists.
Confidence: High. Source: FF-REL-001 remainder; RECONCILIATION top-10 #4 (quota-phrase half since fixed).

### [P2] Trace/session file-count growth without rotation, GC, delete, or TTL

Location:
- `internal/trace/trace.go:162-169` (burst arithmetic: `continue` consumes the `over` budget - 100 runs within `minTraceFileAge=1h` leaves 100 files), `:190-191` (crash files kept by design)
- Sessions: 1000-cap + marker (`session.go:152-188`) but `manager.go` offers only `Save/Load/List/ListCorrupt` - no delete/GC/TTL; `docs/Session.md:99-102` confirms manual retention

Root cause / scenario / impact:
Per-file bounds exist; file *count* is unbounded over restart-heavy 120h
(each supervised restart opens a trace file; crash files never cleaned;
per-checkout trace dirs multiply). Ends in disk pressure - now a *visible*
terminal failure via the P1-1 gate rather than silent divergence, which is
why this is P2 and not P1.
Fix: delete budget independent of scan budget, session rotation/GC/delete
commands + TTLs. Partial cover: `TestRetentionPrunesOldestBeyondCap`
(old-file path only).
Confidence: High. Source: FF-REL-002 remainder; RECONCILIATION top-10 #6.

### [P2] Unfenced memory prompt text (poisoning persists across turns)

Location:
- `internal/memory/memory.go:252-276` (`FormatForPrompt` plain section - no fence markers; verified in prior pass, unchanged on HEAD)
- Deferred explicitly; no red-team proof exists

Root cause / scenario / impact:
Approved-once poisoned memory injects into every future system prompt
without fencing. Mid-run writes require approval (`add_project_memory:
ask` default), so the realistic vector is a single approved poisoned write
(or pre-seeded memory), not autonomous self-poisoning in unattended runs.
Fix: `<memory>` fence markers + re-scrub at render. No regression test.
Confidence: Medium. Source: FF-SEC-006/H03 remainder; RECONCILIATION top-10 #8.

### [P2] `secret_scan` has no TOCTOU protection on any platform (and Windows has none anywhere)

Location:
- `internal/tools/security/secret_scan.go:133-150` (`ResolveWithinWorkspace` + `os.Stat` + `os.ReadFile`, no `O_NOFOLLOW`, no re-validation)
- `internal/tools/filesystem/open_nofollow_windows.go:8` (`os.Open` fallback); `read_file.go:88` Unix-only hardening

Root cause / scenario / impact:
A hostile repo racing symlink swaps against a scan (or a Windows read) can
redirect reads across the workspace boundary into tool results -> model
context. Narrow window, requires hostile repo + timing; `read_file` is
hardened on Unix, so the scanner is the softest path.
Fix: `O_NOFOLLOW` + re-validate + symlink-refuse in `secret_scan` (mirror
`write_file.go:86-115`); document Windows limitation. `toctou_test.go`
covers read; no scanner TOCTOU test.
Confidence: Medium. Source: FF-SEC-010 remainder.

### [P2] Interactive-command heuristic bypasses (deterministic, availability-grade)

Location:
- `internal/tools/shell/shell.go:850` (`commandSeparators` lacks newline, single `&`), `:878-913` (`splitFields` space/tab only), `:886-887` (wrapper-flag stop: `env -i vim`, `sudo -u root vim`), `:898-911` (bare `bash`/`sh` missed), `:842-846` (`python -i` position check)

Root cause / scenario / impact:
Downgraded from HIGH/P1 (all three prior audits) because the real
containment - `Stdin=nil` + timeout + kill + default-ask - holds
regardless: a missed interactive program burns at most one 300s window,
and loop limits bound the campaign (then Terminal parks). No TTY escape is
possible through this gap.
Fix: split on newlines/single-`&` (quote-aware), skip flag tokens after
wrappers, match bare shells and flag-tolerant REPL names, extend wrapper
list; pin each bypass in `interactive_bypass_test.go` (currently only
`bash -c` covered).
Confidence: High (mechanism quoted); Medium that timeout-burn is the worst
outcome. Source: FF-SEC-004 (all audits).

### [P2] Per-operation session-allow map grows without eviction

Location:
- `internal/runtime/scheduler.go:448-467` (`sessionAllow` keyed per unique command+cwd+envHash, no cap/TTL)

Root cause / scenario / impact:
Correct-for-security scoping trades a bounded map for an unbounded one:
tens of thousands of unique generated commands over 120h accumulate for
process lifetime. Eviction is safe (re-askable decisions).
Fix: LRU-cap (~512). No test. Confidence: High (mechanism); Medium
(magnitude under default caps). Source: new observation from the
per-command Always fix (RECONCILIATION section 1 limitation "no expiry").

### [P2] Oversized system prompt bypasses the token budget (always sent full)

Location:
- `internal/runtime/context.go:175-177,213-216,271-272` (system tokens subtracted from room but system appended unconditionally; `MaxMessages` excludes system)

Root cause / scenario / impact:
Downgraded from HIGH (H01/COMMIT100) because task (64/64/32), memory
(200/8KiB), and summary caps bound realistic growth; the remaining vector
is config-author-controlled prompt lengths (trusted config, not hostile
input). A giant `system_prompt` still overruns the provider window -> 400s
-> terminal (visible, not silent).
Fix: count system against the budget and shed history harder (or refuse
oversize system prompt at config load with a clear error). No test.
Confidence: High. Source: H01/N4 remainder.

## ASAP PATCH LIST - P3 (Later)

Real but contained; patch after P1/P2. One-line each with location:

- **Ollama clean-EOF kind** (`providers/ollama.go:278-282` `ErrUnexpectedEOF` vs other adapters' `protocolError`): misclassification as Unknown; both paths end non-success. One-line kind change + test. (N6, CONFIRMED, High)
- **Ollama bare-Done observability** (`providers/ollama.go:318-322`: Done always sent, but zero `Usage|StopReason` matches anywhere in file): usage stays zero; budgeting uses the token estimator, not provider usage - observability-only. (N8 original claim CONFIRMED; the "silent success" variant is FIXED)
- **Ollama ListModels request reuse** (`providers/ollama.go:79-84` same `*http.Request` across retries vs `StreamChat:227-235` per-attempt rebuild): contract violation, harmless for bodiless GET. (N9, CONFIRMED)
- **SSE oversize kind** (`providers/sse.go:54-55` raw `bufio` error -> `ErrKindUnknown` not Protocol): classification fidelity. (N5, CONFIRMED)
- **Gemini thought-parts as Text** (`providers/gemini.go:433-449` default branch; `includeThoughts:true` at `:339-345` with no `thought` field in `geminiPart:139-146`): reasoning leaks as text into context. (N7, CONFIRMED, Medium)
- **ScrubMap typed containers** (`internal/redact/redact.go:172-189`: only `string`/`map[string]any`/`[]any`/`[]string`; `map[string]string`, `[]map[string]any` pass through): narrow - tool args are `map[string]any` in practice. Extend `scrubValue` + test. (CONFIRMED narrow, High)
- **Atomic-save preserves insecure existing perms** (`internal/session/manager.go:126-128`, `internal/config/config.go:449-452`: fresh files 0600, pre-existing 0644 stays 0644): repair-on-save or warn. (PARTIALLY - prior "FIXED" covered fresh files only)
- **`search.within()` case divergence** (`tools/search/search.go:410-422` always case-sensitive vs `sandbox/policy.go:269-282` case-fold on Windows): fail-closed (availability-only). Unify on the sandbox helper. (N17, CONFIRMED)
- **Deduped tool results count toward limits** (`runtime.go:2088-2090` `RecordTool` before `skipHistory`): replay spam can trip `MaxToolCalls`/failure counters; arguably correct stuck-detection. Decide + pin. (N3, CONFIRMED)
- **10s coalescing is time-based, not Turn.ID-based** (`session.go:749`): slow-Ask batch-split risk under strict providers; suspected, unproven at runtime. (FF-SESS-001 remainder)
- **TUI detached discovery fetch** (`tui/discovery.go:40-41` `go func` + `Background`): provider-side single-flight fixed (`providers/discovery.go:121-151`); TUI side runs to timeout, result dropped if picker closed - bounded, minor. Pass the picker ctx. (C5 split)
- **WSL Linux-side orphans** (`sandbox/wsl_windows.go:18` documented): Windows-native now kills whole tree via job objects (`process_windows.go:32-50` - FF-SEC-012 FIXED for native); killing `wsl.exe` cannot reap Linux children by platform design. Needs Linux-side kill relay for full 5-day WSL hygiene. (DEFERRED carry)
- **Slow-TTFB default Ollama** (`ollama.go:128-136` 120s, no kimi-style extension): cold-load timeouts are transient -> turn-retry -> `ExitRetryable` -> supervised resume now covers them; residual is latency, not loss. (H06 remainder)
- **`validID` Windows reserved names** (`session/manager.go:40-52`): no `CON`/`PRN`/`:` handling; failure mode is failed save (now visibly gated), not corruption. (FF-SEC-017, CONFIRMED)
- **Go version floor drift** (`go.mod:3` `1.26.4` vs `README.md:48` `1.22+`): doc-only; bump floor. (FF-CFG-001, CONFIRMED)
- **Doc remainders**: `supervise` / `run --resume --max-turns` / `chat --resume` missing from README CLI list; `doctorSearch`/workspace/API-key splits undocumented in CLI.md; memory-path detail (`memory/projects/<slug-hash>.json`, `global.json`) missing from README. Docs-only. (D1/D4 remainder, verified)
- **Dead-code/hygiene**: `permOptionGap`, dual `Version` vars, event aliases (documented compat - remove deliberately or leave); `UpdatedAt` churn on every save (cosmetic List reorder). (INFO carry)
- **Programmatic `Limits<=0` unlimited** (`runtime.go:33-44` + guards): config path cannot express it; code-only footgun, documented by test. Require explicit opt-in if ever exposed. (H07 remainder)
- **In-run history retention** (`runtime.go:1994-1997` full slice retained, provider view windowed): bounded by default iteration caps; accepted. (H07 remainder)
- **`effectivePromptFor` dead branch** (`runtime.go:504-510` returns `def` where guard checks override - benign because `245-251` merges overrides into the registry first; pinned by `TestAgentOverridePromptBeatsLegacyPrompt`): comment or remove. (REFUTED as live bug -> hygiene)
- **Supervisor needs an external supervisor**: `ff supervise` is foreground; SIGHUP/terminal-close/reboot still park until external supervision (systemd/tmux/nohup). Operational, not code. (Top-10 #1 residual)

## Fixed / Closed Findings

(Each: finding -> current fix -> why closed -> test.)

- **FF-SEC-005 shell OOM** -> 2 MiB shared result cap + metadata (`shell.go:42-86`, `limits.go`) **plus P1-3 stream budget** (`emitChunk`, same budget, single marker) -> result AND stream bounded; shell is the sole production `StreamingTool` (verified by exhaustive grep). Tests: `TestShell_SetLimitsOverrideCapsOutput`, `TestShell_StreamBudgetBoundsLiveOutput`, `TestShell_SmallOutputStreamsFullyUnmarked`, `TestScheduler_ShellProgressBoundedByStreamBudget`.
- **P1-1 persistence** -> driver latch + run-context cancel + save gates + supervisor mirror verification (Phase 4 section). Tests: latch/healthy/cancel-during-tool/mirror/mirror-success/persist-paths/drive-stop/gate unit.
- **P1-2 false success** -> `ExitUnverified=5` + status-aware `Classify` + driver capture + both `run` paths + supervise parking (Phase 4 section). Tests: Classify table, driver verification, `exitForRunStatus`, end-to-end partial, routing.
- **P1-3 stream bypass** -> see SEC-005.
- **FF-SEC-009 concurrent ask** -> `askMu` serialization (`scheduler.go`); tests pass on HEAD.
- **FF-SEC-011 timeout** -> 300s ceiling + `ClampTimeout` + scheduler + config validation (`config.go:693-694`); `TestShellTimeoutCeilingRejected`, `TestClampTimeout`.
- **FF-CTX-002/C03 FinishLength** -> Block-without-exec both shapes (`runtime.go:2020-2023`); `TestFinishLengthWithToolCallsIsBlocked` + hardening variant.
- **FF-SEC-016 lenient args** -> strict `ValidateArgs` before permission (`validate.go`, `scheduler.go:260-278`); `TestValidateArgs_OptionalStringWrongTypeBeforePermission`.
- **FF-SEC-018 verification strings** -> passed-requires-note + verified-requires-passed (`task_tool.go:128-140`); evidence tests. (Truthfulness itself remains unenforceable - inherent, see Unproven.)
- **FF-SEC-003 Always scope** -> per-command+cwd+env for shell/shell_job, broad deny, session-only (`scheduler.go:459-467,587,687-689`); always-scope test file (6 tests).
- **FF-TUI-001 resume blindness** -> collapsed tool rendering (`model.go:381-402`); `resume_test.go`.
- **FF-REL-001 transport half** -> 429 (incl. `resource_exhausted`/`insufficient_quota`, `retry.go:107-119`) + 5xx-not-501 + timeout/connection retried, backoff/jitter/Retry-After caps, clean-only turn retry (`maxTurnRetries=2`); `retry_test.go`, `transient_test.go`, `slow_ttfb_test.go`.
- **FF-SEC-007 secrets core** -> central `redact` (patterns + deep map/slice + env registry) at results/chunks/errors/persist/memory/doctor/permission surfaces **plus** prior-cycle display fixes (TUI non-string `permission.go:233-246`, headless `stdin.go:53`), each with FAIL-pre/PASS-post regression tests. Residuals (approved content reaches model; 0600 plaintext files) are design.
- **H03 fence escape** -> `</tool_result>` escaped (`scrub.go`), fence tests. (Memory unfenced -> P2 above.)
- **H04 nested scrub** -> deep `ScrubMap`/`ScrubSlice` (`redact.go:149-189`), `TestScrubMapNested`. (Typed-container remainder -> P3 above.)
- **H05 write cap** -> 5 MiB refuse + chunking note (`write_file.go:65-66`) + test.
- **H09 overclaim** -> `FilesystemConfined:false` + honest copy + honesty tests (`native.go:63-90`, `sandbox.go:218-220`).
- **FF-SEC-012 Windows-native kill** -> job objects `KILL_ON_JOB_CLOSE` + taskkill+fallback+`waitGone` (`process_windows.go:32-99`); `TestKillTerminatesTree`, `TestJobCloseKillsTreeWithoutKill`. (WSL-side residual -> P3.)
- **FF-REL-002 cap/marker/replay** -> 1000 + `[compacted]` + `Compacted` + replay skip (`session.go:152-188,339-344`); growth/durability tests. (Rotation/GC -> P2.)
- **FF-OBS-001 logging** -> opt-in redacted JSONL trace with caps (`trace.go:17-29`), crash-kept; `trace_test.go` caps/retention tests. (Burst arithmetic + file-count -> P2.)
- **FF-SESS-001 ID-dedup half** -> intra-batch + cross-batch dedup (`session.go:739-755`); coalesce tests. (Time-window remainder -> P3.)
- **FF-SEC-010 Unix half** -> `O_NOFOLLOW` + re-validate + symlink-refuse (`read_file`, `write_file`); toctou tests. (secret_scan/Windows remainder -> P2.)
- **FF-CRIT-003 window half** -> 100-message per-turn window + token budget + reserve + caps negotiation + task/memory caps + CJK-safe estimator. (System-bypass remainder -> P2.)
- **FF-CFG-003 negatives** -> `validateRunLimits`/`validateTools` reject `<0` incl. per-agent prefix path (`config.go:656-698`); config/agent tests.
- **N1 rune-safe trace cut** -> `trace.go:219-245`; `TestScrubArgsTruncationIsRuneSafe`.
- **N2 Compacted rollback** -> `manager.go:69-80`; `TestSave_FailureRestoresCompactedCount`.
- **N10 gate release** -> release-before-decode in all adapters (`openai_compatible.go:604-610` et al.).
- **N11 runGit** -> removed (no hits). STALE.
- **N13 compacted marker render** -> `model.go:400-411`; `TestSessionEntries_RendersCompactedMarker`.
- **N14 chat-resume trap** -> `cmd/chat.go:30-58` mirrors root resume. FIXED (was STILL OPEN at reconciliation).
- **N15 events table** -> 12/12 rows + aliases (`docs/Runtime.md:64-80`).
- **N16 cue/policy sync** -> `cue/tools.cue:7-16` documents ask-default rationale, matches `config.go:196-222`.
- **N18 retention test** -> `TestJob_RetentionEvictsOldestTerminal` (32-bound).
- **C5 provider discovery** -> synchronous single-flight (`providers/discovery.go:121-151`), 20s cap at call site. (TUI residual -> P3.)
- **N8 silent-success variant** -> `ollama.go:318-322` always sends terminal Done/Err. (Usage/StopReason observability -> P3.)
- **H02 shell Always** -> folded into SEC-003 fix.
- **H10/HYG dirty tree** -> HEAD `4a70846` clean; FF-SEC-003 + P1 fixes committed (no uncommitted-fix irreproducibility remains).
- **opencode.json contradiction** -> ignored, untracked (verified `git ls-files` empty).
- **D2 persist docs, D3 paths, stdout pollution, WSL job parity, CJK estimator** -> as reconciliation recorded, still accurate on HEAD (spot-verified: `writeFileAtomic` + stderr notice + `job_tool.go:166` + hardening context test).
- **RC6 E1-E7** -> test-correctness notes; closed (no new vulns; caps/honesty changes only).

## Refuted / Superseded Findings

- **Per-agent `system_prompt` override dropped (paranoid-audit suspect) - REFUTED.** `effectivePromptFor` (`runtime.go:508-509`) textually returns `def` where the guard checks the override, but `runtime.go:245-251` merges `cfg.Agents` into the registry *before* `Get`, and `definition.go:150-151` writes the override into `def.SystemPrompt` - so the live value is already the override. Pinned by `TestAgentOverridePromptBeatsLegacyPrompt`. Remaining: misleading dead branch -> P3 hygiene, not a bug.
- **FF-100-025 TUI turn-repair duplication - REFUTED.** TUI (`model.go:733,1154,1292`) and the headless driver both call the *shared, idempotent* recovery helpers (`Heal`/`CancelAndRepair`) on their own sessions; no double execution path exists (recovery never re-executes by construction - pairing + `RepairInterruptedTurn` synthesize-only).
- **C3 `emitMu` stall - REFUTED (intended serialization).** `scheduler.go:155-160` is a per-batch local mutex ordering concurrent tool-goroutine emits onto one channel; blocking is consumer backpressure by design, scope is one batch, no shared-manager lock involved.
- **Trace `Meta` unscrubbed (paranoid suspect) - REFUTED.** `trace.go:264` stores `Meta` raw, but every call site (`:105,289,296,308,313,319,324,333,342`) passes only counts/identifiers (provider, model, tokens, `final_chars`); args/snippets/errors take the scrubbed paths (`scrubArgs`, `snippet`, `Scrub`). No secret reaches `Meta`.
- **Top-10 #1 "no auto-restart" - SUPERSEDED.** `ff supervise` + `ff run --resume` + persisted budget + `ExitRetryable`-only restart + save-verified mirrors replace the "fatal until human" architecture (opt-in, foreground). Residual (no daemonization/SIGHUP) -> P3 operational note.
- **FF-SEC-015 - SUPERSEDED/DUPLICATE** of FF-REL-001 (same retry code, security view). Folded.
- **FF-SEC-003 "gap retained"/per-tool text, opencode.json "tracked", `runMu` "uncommitted", CJK "untested" - SUPERSEDED** by committed code + tests (per reconciliation section 9, re-confirmed on HEAD).
- **N11 runGit, H10 dirt instances - STALE** (path gone / tree clean).

## Cross-Audit Duplicates

```text
DURABILITY (1 root cause, was counted ~6 ways)
+-- FF-REL-002 session growth
+-- H08 last-wins
+-- FF-SESS-001 coalescing
+-- N2 save rollback
+-- P1-1 save swallowed (paranoid) -> FIXED
\-- supervisor budget staleness -> FIXED (mirror verification)

VERIFICATION FALSE-SUCCESS (1 root cause, ~4 ways)
+-- FF-CTX-002 FinishLength->Done
+-- FF-SEC-018 task trust strings
+-- Missing-Done confusion
\-- P1-2 EventDone->0 (paranoid) -> FIXED via ExitUnverified

PROVIDER RETRY (1 root cause, ~3 ways)
+-- FF-REL-001 5xx/timeout/mid-stream
+-- FF-SEC-015 (dup, security view)
\-- H06 slow-TTFB (sub-case)

ALWAYS-SCOPE (1 root cause, ~3 ways)
+-- FF-SEC-003 global Always
+-- H02 per-name shell
\-- Top-10 #10 non-command remainder -> P3

OUTPUT BOUNDS (1 root cause, ~3 ways)
+-- FF-SEC-005 shell OOM
+-- RC6-E1 Content-duplication note
\-- P1-3 stream bypass (paranoid) -> FIXED

SANDBOX HONESTY (1 root cause, ~3 ways)
+-- FF-SEC-001 WSL escape
+-- FF-SEC-002 native zero isolation
\-- H09 FilesystemConfined overclaim -> FIXED (honesty, not confinement)

ERROR-KIND FIDELITY (1 family: N5/N6/N8/N9/N10) -> P3 bundle
DOC DRIFT (D1-D4, N15/N16, CFG-001) -> fixed except P3 remainders
```

## Accepted Risk (documented, by design)

Not bugs to patch; recorded so a future audit does not re-litigate them
without new evidence. Each is explicit in code and/or docs:

- **Native/permissive default runs with user privileges.** Documented
  usability choice (`docs/Sandbox.md`, `sandbox.go:208-228`); sensitive
  paths still escalate to Ask (`scheduler.go:592-607`). Opt-in `strict`
  exists. (FF-SEC-002a)
- **WSL lexical mitigation is bypassable by design.** Documented at
  `shell.go:360` ("mitigation only, not a boundary") and
  `docs/Sandbox.md:191-192` (indirection bypasses named). No OS cage is
  claimed. (FF-SEC-001 remainder)
- **Mid-stream failure is fatal without in-turn resume.** Replaying
  partial output would duplicate side effects (`retry.go:169-174`);
  supervised restart resumes the *session* instead. (H06/REL-001 remainder)
- **Concurrent same-session writes are last-wins.** Files stay atomic and
  parseable (`manager.go:133-164`, `concurrency_test.go`); no cross-process
  lock by design. (H08)
- **`pwd` has no policy.** Read-only `Getwd`, `allow` by default
  (`pwd.go:24-37`, `config.go:212`); TUI header discloses cwd anyway.
  (FF-SEC-014)
- **Permission prompt truncates long strings explicitly** (300c + note,
  scrub-before-cut at `permission.go:203-229`). Malicious-tail risk is
  disclosed, not hidden. (FF-PERM-001)

## Remaining Unproven Areas

```text
not tested:
- Trickle-body provider stall (no idle-timeout test possible until the timeout exists)
- Slow-Ask batch-split under strict providers (coalescing time window)
- Supervisor behavior across real process kills (budget resume is unit-tested, not kill-tested)
- Real disk-full / AV-lock save failure (deterministic-failure IDs used instead)

traced only:
- Unix pgroup kill, WSL relay / Linux-side orphans (Windows host; code + docs traced)
- Concurrent-session last-wins interleavings (atomicity proven, interleaving outcomes by design)
- Sustained-429 stampede dynamics (no multi-session soak)

environment limitation:
- RACE COVERAGE: UNPROVEN (no C compiler on this host; prior trees reported clean - not transferable)
- WSL behaviors require WSL; job-object behavior requires Windows CI (tests exist, not run here beyond suite)

requires live provider:
- 429/5xx/timeout/mid-stream classification end-to-end (hermetic httptest only)
- Quota-phrase matching against real provider bodies
- Slow-TTFB cold-load recovery timing

requires Unix/WSL:
- O_NOFOLLOW enforcement, EvalSymlinks/junction paths, taskkill-vs-pgroup parity

requires race detector:
- Scheduler semaphore/askMu/emitMu interleavings, session concurrent-save parseability, driver single-goroutine assumption

requires long soak:
- File-count growth rates, sessionAllow growth, transcript growth, memory/disk curves over 120h
- Restart-budget consumption patterns under flaky providers

requires adversarial MCP:
- Malicious tool descriptions/schemas at scale (bounds unit-tested with crafted inputs, no live hostile server run)
- Poisoned MCP prompts reaching the model through approved results (by-design exposure, no red-team)

inherent (not provable in code):
- Model truthfulness (verification notes, test-pass claims, discoveries)
- Approved-content exfiltration (user-approved secrets reach the model by design)
- Plaintext 0600 session files (design; OS-user boundary)
```

# PATCH ORDER

```text
1. Provider auth-header deny-list (P1) - smallest, contained, credential-grade; config validation + test.
2. extraEnvArgs key validation (P1) - one choke point, aligns all paths; table test.
3. Sensitive escalation for shell-command + MCP args (P1) - scheduler-only change + permission tests.
4. Idle-body provider timeout (P1) - adapter/transport change; needs trickle test + full provider suite.
5. Global rate limiter (P2) - after 4 (shares backoff semantics); multi-instance test.
6. Trace/session rotation + GC (P2) - after 5 (independent); retention-burst test + session delete command.
7. Memory fencing (P2) - prompt-only change; render test.
8. secret_scan TOCTOU parity (P2) - mirror read_file hardening; TOCTOU test.
9. Interactive-heuristic hardening (P2) - bypass table tests first, then detection.
10. sessionAllow LRU cap (P2) - trivial; scheduler test.
11. System-prompt budget accounting (P2) - context tests.
12. P3 bundle in file order (fidelity kinds, ScrubMap types, perm repair, within() unification, dedup-count decision, validID, version floor, docs, hygiene) - each independently reviewable.
```

*Validation performed in this pass: `go build ./...` PASS; `go test -count=1 ./internal/recovery/ ./cmd/ ./internal/runtime/` PASS (covers committed P1 tests); full-suite green established at HEAD by the P1 commit (`go test ./...` all ok per commit, re-verified for affected packages here). Race detector unavailable on this host: RACE COVERAGE UNPROVEN. No production code modified; this report is the only write.*
