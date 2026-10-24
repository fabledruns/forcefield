# Orchestration Adversarial Test Suite

Status: in progress (mission branch `feat/orchestration-adversarial-tests`, cut from
`feat/secure-agent-orchestration` at `4cf3149`).

This document is the working threat matrix for the dedicated adversarial test suite
covering the multi-agent orchestration layer (orchestrator, `delegate` tool, child
scopes, child schedulers, permission isolation, spawn budgets). It records: the
invariants orchestration must uphold, the tests that verify each invariant, the
enforcement path each test actually exercises, discovered defects, and the
verification ladder. It is updated as stages complete.

Evidence levels used throughout:

- **[U] Unit** — the boundary function rejects bad input.
- **[I] Integration** — the real runtime/scheduler rejects the attack through the
  production enforcement path.
- **[R] Adversarial regression** — a deliberate mutation of the enforcement check
  makes the named test fail (performed only on committed files, fully reverted).

A test marked only [U] claims nothing about the live agent loop; a claim such as
"authorization cannot be bypassed" requires [I] plus [R].

---

## 1. Implementation facts (Stage A recon, verified against source)

Verified at commit `4cf3149`. These are the facts the tests build on.

### Construction and registration

- `newRuntime` registers `delegate` on the full tool universe **once**, only when
  `cfg.Orchestration.Enabled` (`internal/runtime/runtime.go` ~255).
- `withDelegateTool(orch, tools)` appends `delegate` to every **non-empty** agent
  tool list when orchestration is enabled; an explicitly empty list stays pure-text.
  When orchestration is **disabled** the list is returned unchanged — a configured
  agent override naming `delegate` then fails strictly in `Manager.Filtered`
  ("unknown tool"), which bricks `newRuntime`/`SetAgent`. **Defect O1** (see §6;
  after recon this was resolved as a load-time truthful rejection, not an
  omission).
- `knownTools` in `internal/config` includes `delegate`, so `tools:` limit blocks
  may address it, and agent `tools:` lists pass config validation untouched
  (unknown agent-tool names fail later in `Filtered`, by design, strictly).
- Plan mode (`planmode.go`) builds its subset from `planModeTools`, which does not
  contain `delegate`: planning turns can never delegate, structurally.
- `SetAgent` re-applies `withDelegateTool` on every switch; the switch is atomic
  (failure keeps the previous agent/tool set).
- The orchestrator is per-`Runtime` (one per process-level runtime in normal `ff`
  usage). `orch.sem` is shared by every run of that runtime; per-run spawn
  counters (`orchestrationShared`) are fresh per top-level `StreamChat`.

### Admission, budgets, concurrency

- `ExecuteStream` order: scope check (fail-closed without an agent-run context) →
  `ctx.Err()` check → strict string-arg extraction (`delegateArgs`) → agent name
  lower/trim → task trim → task size measured **in runes** against
  `maxTaskChars` (default 8000, floor 256, ceiling 65536) → registry lookup →
  semaphore acquire (`orch.sem`, cancellable) → `admitSpawn` (atomic depth +
  cumulative per-run budget charge) → `runChild`.
- Depth (`maxDepth`, default 1, ceiling 3): `admitSpawn` rejects `childDepth >
  maxDepth` **before** charging the budget; `childAgentManager` additionally strips
  `delegate` from any child universe at `childDepth >= maxDepth` (the structural
  second side of the depth bound).
- Per-run budget (`maxChildrenPerRun`, default 16, ceiling 64) is **cumulative
  across levels**: completed, cancelled, failed, and denied children never free
  budget. The honest error text reports the exact charged count.
- Config cross-field gap: nothing rejects `max_concurrent_children < max_depth`.
  With cap 1 and depth ≥ 2 a grandchild blocks on `orch.sem` until its delegate
  tool-call timeout (`tools.MaxTimeout`, 300s clamp) fires. **Defect O2** (§6).
- Concurrency cap (`maxConcurrentChildren`, default 4, ceiling 8) is the child-run
  semaphore. Slot release happens by deferred receive right after join — exactly
  once per acquire. Child schedulers are forced to `MaxConcurrency = 2` (tool-call
  parallelism inside one child), independent of the spawn cap.
- Rejected spawns (validation failures, depth limit, budget limit, cancelled
  acquire) never charge the budget; a spawned-but-cancelled child **does** charge
  (documented cumulative semantics).

### Permission isolation

- Child scheduler = `newScheduler(childMgr, parentPermissions, childAsker,
  childCfg)` with a **fresh empty** `sessionAllow` map. Parent session-scoped
  approvals (server-serialized `sessionAllowKey`) never reach children, and child
  session decisions never reach the parent.
- Deny mode (default): `childAsker` = `denyAllAsker`. Every child ask resolves
  Deny-without-error, touches no approval surface, persists nothing.
- Interactive mode: child asks are wrapped: `Origin{Agent: def.Name, Chain:
  "root > child"}` stamped, routed to the parent's **current** asker captured at
  spawn time, and any `Persist()` answer is downgraded (Always→Once), so a child
  can never write rules. Ask serialization: parent + every interactive child share
  `orch.askLock` via `scheduler.askSerial`; deny-mode schedulers keep their own
  `askMu`. Headless/nil asker fails closed (`resolveAskWithManager` returns Deny
  with an error).
- Sensitive-path calls always force Ask even under session/global Allow
  (`isSensitiveCall` + `checkBoundary` pre-flight is fail-closed before any
  rule check).
- Scheduler order per call: boundary pre-flight → session deny → session allow
  (operation-scoped) → global rule → Ask resolution. `ValidateArgs` runs **before**
  permission so a prompt cannot hide malformed intent.
- Every child tool call therefore passes through the **real** scheduler path; no
  child has a fake permission route.

### Child run and reporting

- Children run through the parent's own `Runtime.run` loop (not `StreamChat`), so
  no root `runMu` deadlock is possible. Child snapshot copies the parent snapshot;
  `agent`, `manager`, `scheduler`, `mode` are replaced (children are always chat
  mode).
- The child event stream has exactly one consumer (`childRunRecorder`); first
  terminal event wins (`EventDone/Error/Blocked/Cancelled`), counts
  `EventTool*` terminal events once each, and records denial names.
- `runChild` joins unconditionally (cancellation waits for the child), then the
  recorder result is composed: header (turns, tool_calls, failed, denied + names),
  honest status line, scrubbed body, denial/failure notes. Deny mode surfaces
  denials but no prompt interactions.
- Report body is capped at `delegateReportMaxBytes = 5000` bytes via
  `tools.TruncateString` (rune-boundary cut) + `tools.TruncationNote`. Composition
  scrubs (`session.ScrubContent` → `redact.Scrub`) **before** truncation, so a
  canary split by the cut can never appear half-redacted.
- Child `EventText` deltas are dropped by the recorder by design; only terminal
  composition surfaces child text.
- The run loop (`runtime_run.go`) recovers orchestration/provider panics into a
  bounded `EventError` ("agent run panicked: …") after bounded cleanup.
- Idempotency: `executed` map keyed by non-empty tool-call ID records the terminal
  event kind; a replayed ID reuses the recorded result with an explicit
  `[note: …]` appended and emits the recorded terminal event. Same-turn duplicates
  with the same ID both execute (dedup is cross-turn; same-batch same-ID calls do
  not reuse within that batch) — verified behavior, pinned by tests below.
- Event channels: buffered `size 1`; cancellation-path emits are best-effort
  (events may drop at the cancel boundary — pre-existing emit-drop semantics;
  tests treat required invariants as join + no-leak + honest status, with the
  child terminal event optional).

### Redaction surface

- `redact.Scrub`: keyword prefilter + credential-shape patterns + a process-wide
  exact-value registry (`AddSecret`, values ≥ 8 chars). `ScrubMap`/`ScrubSlice`
  recurse; `ScrubError` preserves the chain. The scheduler redacts stream chunks
  (`onChunk`) and every tool result content field.
- The TUI permission modal and the stdin asker scrub arguments/path text before
  display. The stdin asker renders origin (`Chain` falling back to `Agent`);
  `internal/tui/permission.go` renders tool, resolved path, and scrubbed args but
  **not** `request.Origin`. **Defect O3** (minor, attribution parity).

---

## 2. Invariants and threat matrix

| # | Invariant (adversarial phrasing) | Enforcement path | Tests | Level |
|---|---|---|---|---|
| T1 | A `delegate` call with malformed/hostile arguments (missing/typed fields, empty, NUL, control chars, oversized runes) is rejected, spawns nothing, charges no budget, mutates no permission state | `delegateTool.ExecuteStream` → `delegateArgs` → registry lookup | `TestAdversarialDelegateInput*` (runtime), `FuzzDelegateArgs`, `FuzzTruncateDelegateBody` | U, I |
| T2 | Task size limit is measured in runes and the documented bound matches enforcement exactly (limit accepted, limit+1 rejected) | `ExecuteStream` rune count vs `resolved.maxTaskChars` | `TestAdversarialTaskSizeBoundaries` (incl. multibyte) | U, I |
| T3 | Delegation is unavailable outside an agent-run scope, after cancellation, and when not wired; fail-closed every time | scope nil-check, `ctx.Err()` checks | `TestAdversarialDelegateOutsideRun`, `TestAdversarialDelegateAfterCancellation` | U, I |
| T4 | A child never inherits the parent's session approvals (both directions) | fresh `sessionAllow` map per child scheduler | `TestAdversarialParentSessionApprovalCannotAuthorizeChild`, `TestAdversarialChildDecisionCannotMutateParent` | I |
| T5 | A child cannot create or modify persisted global rules, even answering "always allow" or "always deny" | `childAsker` Persist()/downgrade + child's own session map | `TestAdversarialChildAlwaysAllowIsDowngraded`, `TestAdversarialChildAlwaysDenyPersistsNothing`, `TestAdversarialDenyModePersistsNothing` | I, R |
| T6 | Deny-mode child asks: no prompt surface interaction, denied result, zero persistence | `denyAllAsker`, recorder denial counting | `TestAdversarialDenyModeFailsClosedOnEveryAsk` | I, R |
| T7 | Missing/nil/failing askers fail closed (headless automation, asker error, asker swapped after spawn does not widen in-flight children) | `resolveAskWithManager` nil guard; `childAsker` captures asker at spawn | `TestAdversarialFailClosedWithoutAsker`, `TestAdversarialFailingAskerDeniesChild`, `TestAdversarialAskerSwapAfterSpawnDoesNotWidenChild` | I |
| T8 | Concurrent parent/child prompts are serialized on one approval surface; no cross-contamination of approvals | `askSerial` shared lock | `TestAdversarialConcurrentChildPromptsAreSerialized` | I, R (M1, M5) |
| T9 | Child asks carry accurate attribution (full chain, root first); a missing origin fabricates nothing | `childAsker` Origin stamp | `TestAdversarialInteractiveAskCarriesDelegationChain` | I |
| T10 | The TUI modal shows attribution for child asks (origin line) — parity with the stdin asker | TUI `formatToolBlock` | `internal/tui` test (after O3 fix) | U |
| T11 | Forbidden depth cannot regain `delegate`, including via explicit agent definitions naming it | `childAgentManager` strip + `admitSpawn` depth check (two independent sides) | `TestAdversarialDelegateStrippedFromChildAtDepthLimit` (depth 1), `TestAdversarialGrandchildAtExactMaxDepth` + `-OneBeyond` | I, R (M3) |
| T12 | Cumulative spawn budget: completed children never free budget; rejected spawns charge nothing; concurrent admission charges exactly once | `admitSpawn` under `shared.mu` | `TestAdversarialSpawnBudgetCumulative`, `TestAdversarialRejectedSpawnsDoNotChargeBudget`, `TestAdversarialConcurrentAdmissionChargesExactlyOnce` | I, R (M2) |
| T13 | Semaphore: cap holds under true overlap (deterministic barrier evidence); every acquired slot is released exactly once; cancellation during acquire releases nothing and charges nothing | `orch.sem` acquire/release, `ExecuteStream` select | `TestAdversarialSpawnCapHoldsUnderBarrier`, `TestAdversarialSlotReleasedExactlyOnce`, `TestAdversarialCancelDuringAdmission` | I, R (M9, M10) |
| T14 | Cancelling at any lifecycle stage joins the child, prevents success-after-cancel, leaks no orchestration goroutine, and never turns cancellation into success | `runChild` select-join, `childCtx` cancel, event channel close | `TestAdversarialCancel*` series, `TestAdversarialAbandonedConsumerStillFinishesRun` | I |
| T15 | Secrets seeded into every injectable channel (task, child final text, provider errors, tool errors, panics, config-derived keys) cannot appear in the delegate report, the parent transcript, permission-prompt text, or truncation notes | `redact.Scrub` at composition + scheduler scrub; `AddSecret` registry | `TestAdversarialSecrets*` series, TUI prompt-scrub test | U, I, R (M6) |
| T16 | Hostile child output cannot create authorizations, become instructions, or upgrade a blocked/failed child to success; report stays bounded, attributed, honest | terminal event source (`EventDone` only from real loop), `composeDelegateResult`, `truncateDelegateBody` | `TestAdversarialAuthorityClaimsDoNotCreateApprovals`, `TestAdversarialContradictoryStatusStaysHonest`, `TestAdversarialHostileTextStaysData`, `TestAdversarialControlCharactersDoNotCorruptComposition` | I |
| T17 | Replayed/duplicate tool-call IDs cannot re-execute a child; replays of cancelled/failed/denied results stay that terminal type; conflicting arguments do not win; same-turn duplicates both execute (documented) | `executed` map, `executeCalls`, recorder first-wins | `TestAdversarialReplay*` series | I |
| T18 | Limits (depth, fan-out, concurrency, task chars, tool calls, loop detection) reject exactly at boundaries: accepted at limit, rejected at limit+1, honest error text | `admitSpawn`, `ExecuteStream`, run-loop limits | boundary tables across resource/lifecycle files | U, I |
| T19 | Configuration rejects out-of-range/unknown/too-large values at load, in both enabled and disabled states; malformed types/near-overflow ints fail at the parse boundary | `config.validateOrchestration`, `yaml.Unmarshal` | `internal/config/orchestration_adversarial_test.go`, `FuzzValidateOrchestration` | U, R (M8) |
| T20 | Disabled orchestration: no `delegate` anywhere in any universe; agent overrides that name it keep the runtime buildable (warning, not a hard failure) and grant no delegation capability | `withDelegateTool` + `resolveAgentTools` (O1 fix), `MCPWarnings` reporting | `TestAdversarialDisabledOrchestrationOmitsDelegateRequest`, existing disabled tests | I, R (M7) |
| T21 | Scope attribution: child scope depth/chain/snapshot is never confused with the parent's; two concurrent top-level turns on one runtime serialize on `runMu`; two runtimes do not share budgets | context scope values, `rootScope`, `runMu` | scoped probes in resource file | I |
| T22 | A stalled child provider is bounded by the delegate tool timeout ceiling; the nested self-starvation config (O2) degrades to bounded failure, not a crash or permanent leak | `tools.MaxTimeout` clamp, `toolCtx` select | `TestAdversarialNestedSelfStarvationIsBounded` (cancel-bounded variant), config cross-field test after O2 fix | I |
| T23 | Provider/tool panics inside a child run become a terminal child error, scrubbed, with bounded cleanup — never a crashed process, never a leaked child | `run` panic recovery, scheduler panic recovery, recorder | `TestAdversarialChildProviderPanicBounded` | I, R |
| T24 | Pending permission state written during an adversarial run equals the pre-state byte-for-byte (canonical `Rules`) when persistence is forbidden | `recordingRulesStore` + `Manager.Rules()` snapshots | asserted inside T4–T6 tests | I |

---

## 3. Attack inventory → test files

New files (same-package `package runtime` where unexported state is required;
package-level note: fuzzer covers only pure functions — `config.Load` touches the
global home path and is **not** fuzzable in-process; documented gap):

| File | Attacks |
|---|---|
| `internal/config/orchestration_adversarial_test.go` | boundary values at exact ceilings/floors, +1 rejects, unknown child_modes, zero/negative/huge values, malformed types, nulls, disabled-with-bounds, cross-field cap<depth (O2), case sensitivity. `FuzzValidateOrchestration`. |
| `internal/runtime/orchestration_adversarial_input_test.go` | every `ExecuteStream` validation branch, task boundaries in runes, hostile identifiers, control chars, cancellation-before-start, no-scope calls, budget non-consumption on rejection. |
| `internal/runtime/orchestration_permissions_adversarial_test.go` | session isolation both directions, downgrade both Always kinds, deny-mode zero-interaction, persist-nothing byte-identity, nil/failing asker, spawn-time asker capture vs mid-run swap, concurrent prompt serialization, origin chain accuracy. |
| `internal/runtime/orchestration_secrets_test.go` | canary matrix: child final text, provider errors, tool errors, tool panics, run panics, truncation-boundary cuts, permission prompt display (via TUI test), cancellation error strings. |
| `internal/runtime/orchestration_hostile_output_test.go` | authority-claim text, fake approvals/tool results, control chars/ANSI, oversized bodies, UTF-8-boundary truncation, contradicting status claims, nested failure summaries. |
| `internal/runtime/orchestration_resource_adversarial_test.go` | budget/depth/concurrency exact boundaries, barrier-proven cap overlap, slot reuse, cancel-during-admission, concurrent admission, cumulative accounting, per-runtime scope, nested self-starvation bounds. |
| `internal/runtime/orchestration_integrity_adversarial_test.go` | duplicate/replayed IDs (all terminal kinds), same-turn duplicates, conflicting args, empty IDs, copy independence, first-wins recorders. |
| `internal/runtime/orchestration_lifecycle_adversarial_test.go` | cancellation at every stage, join-before-return proofs, abandoned consumer, panic recovery paths, child provider stall bounds. |
| `internal/runtime/orchestration_adversarial_fuzz_test.go` | `FuzzDelegateArgs`, `FuzzTruncateDelegateBody`, `FuzzFenceToolResult` (+ `FuzzValidateOrchestration` in config). Seeded with malformed/Unicode/control/boundary payloads. Explicit properties per target. |
| `internal/tui/permission_adversarial_test.go` | child-origin line rendering + argument scrubbing in the modal display (small, presentation-only). |

Existing coverage deliberately NOT duplicated (it stays as the baseline suite):
fail-closed outside-run scope, delegate e2e happy path, task-user-role-only,
permission downgrade basics, caps/limits, cancellation join, duplicate-ID replay,
recorder concurrency, stdin origin rendering. The adversarial suite extends and
mutates against, rather than rewrites.

---

## 4. Determinism rules

- No `time.Sleep` for synchronization. Barriers: buffered channels, gated askers
  (each `Ask` blocks until explicitly released), wait-until-child-started pattern
  (as in `blockingChildProvider`), event-channel close as join proof.
- Peak-overlap claims need either barrier gates or existing
  `concurrencyProbeProvider`-style counters; never inferred from timing windows.
- Test-directed cancellation is primary; bounded deadlines exist only as deadlock
  detectors and are asserted distinctly from the property.
- Provider fakes: `scriptedProvider` (sequential, NOT concurrency-safe),
  `concurrencyProbeProvider`, `blockingChildProvider` (existing), plus new:
  `keyedProvider` (routes child turns by task text so concurrent children are
  deterministic), `gatedAsker` (serialized-ask proof + overlap detector),
  `gatedChildProvider` per-child release for barrier tests.
- Shared global state (`redact.AddSecret` registry, `USERPROFILE`) is
  save/restore-wrapped per test; no `t.Parallel` in tests that touch globals.

---

## 5. Fuzz targets and properties

| Target | Input | Properties |
|---|---|---|
| `FuzzDelegateArgs` | arbitrary JSON-ish byte slices → `json.Unmarshal` → map, or crafted `map[string]any` | no panic; missing-field classification never changes with content; successful extraction returns strings unchanged; deterministic |
| `FuzzTruncateDelegateBody` | arbitrary string + int size | output is exactly the `tools.TruncateString` kept prefix plus the fixed note when the record says truncated, or the input verbatim otherwise; deterministic; valid UTF-8 remains valid UTF-8; byte bound holds except upstream's minimal-progress fallback — when not even one complete rune fits, the shared cut keeps the smallest valid prefix (one crasher found and pinned in this suite's testdata: first-rune-wider-than-bound with `maxBytes=1`; same fallback shape applies to all-invalid bodies, unifying the earlier widened-window note) |
| `FuzzFenceToolResult` | tool name + arbitrary body | single envelope; body cannot contain a leading closing fence; deterministic |
| `FuzzValidateOrchestration` | struct fields as ints/strings | error ⟺ at least one field outside documented bounds (`depth∈[1,3]`, `children∈[1,8]`, `perRun∈[1,64]`, `taskChars∈[256,65536]`, mode ∈ {deny,interactive}, cap ≥ depth when both set post-O2); no panic |

Reproducible commands: `go test -fuzz FuzzX -fuzztime=10s ./internal/runtime`
(smoke, CI) and unlimited `-fuzztime=60s+` campaigns manually. Corpus seeds:
valid minimal, each boundary, each rejected shape, Unicode, NULs, ±INT_MAX.

---

## 6. Discovered production defects (Stage A) — planned minimal fixes

| ID | Defect | Invariant violated | Fix (minimal) |
|---|---|---|---|
| O1 | Agent/tool overrides naming `delegate` with orchestration **disabled** fail `Manager.Filtered` at runtime construction with the misleading "unknown tool" error, though `delegate` is a listed, known tool name (`knownTools`). Recon (Stage A/B) revised the planned fix: the omit-with-warning approach would contradict the documented fail-closed design (docs/Agents.md explicitly documents hard failure), and MCP-omission is not the right convention for static native tools. **Resolved fix:** keep fail-closed hard-failure semantics; move the rejection to `config.validate()` with a truthful message ("delegate requires orchestration.enabled"), via `validateDelegateNeedsOrchestration`. Outcome identical (config rejected, no capability), timing and wording improved. | T20 | config pre-load cross-field rejection + `TestAdversarialDisabledOrchestrationRejectsDelegateInAgentTools`; capability side remains covered by existing disabled tests | U, I |
| O2 | No cross-field validation of `max_concurrent_children < max_depth` — nested delegation becomes a 300s stall per attempt (sem starvation + tool timeout), hostile-to-operators config silent at load. | T18/T22 | `validateOrchestration` rejects cap < depth with a load-time error naming both fields; CUE schema gets the same constraint; docs updated. |
| O3 | TUI permission modal renders no `Origin` attribution for child asks (stdin asker does). Operator ambiguity when approving a child's ask in TUI mode. | T9/T10 | Render an origin line in the modal (`formatToolBlock`) with the same chain-fallback used by stdin; display-only, no semantics change. |

Design notes (no rewrite, recorded for the report):

- **N1** `withDelegateTool` implicitly *adds* `delegate` to every non-empty tool
  list when enabled — an agent that never opted into orchestration gains the
  capability by default. This is the documented opt-into-tools rule
  (`docs/Runtime.md`); recorded as fail-open-vs-operator-intent risk, not changed.
- **N2** Cap scope: `orch.sem` is per-`Runtime`. One `ff` process hosts exactly one
  runtime in normal usage, so "process-wide" (docs wording) and per-Runtime agree
  in production; in-process multi-runtime tests must not claim a process-wide cap.
  Tests and docs state the actual scope: one orchestrator per runtime.
- **N3** Emit-drop at the cancellation boundary drops non-terminal events after
  ctx is done (pre-existing, documented); lifecycle tests assert join/no-leak and
  treat the child terminal event as optional under cancel.

## 7. Mutation matrix (Stage D; every row executed once and then fully reverted)

Restore procedure: each target file was snapshotted to
`%TEMP%\opencode\stageD\<name>.go` **before** the mutation and copied back
afterwards — never `git checkout -- <file>`, because `internal/config/config.go`
carries this suite's uncommitted O1/O2 fixes and reverting it to 4cf3149 would
destroy them. Restore was verified by re-running the named tests green after
every row. All ten rows were observed to fail at least one named test at least
once, for the recorded reason (exact text below). Note on observed wording:
several serialization-contract mutations surface through the tests' staging
deadlock detector rather than the direct overlap flag; that honest reason (the
second prompt never appearing after the first resolved) is the serialization
contract breaking — the detector message is the contract's observable.

| Mutation | File | Observed failing test + exact reason |
|---|---|---|
| M1 remove `askSerial` branch (always per-scheduler `askMu`) | `scheduler.go` | `TestAdversarialConcurrentChildPromptsAreSerialized` → "deadlock detector: the second child prompt never appeared after the first resolved" (serialization of cross-scheduler asks broken) |
| M2 drop `spawned >= maxPerRun` check | `orchestration.go` | both `TestAdversarialGrandchildDelegationConsumesRunBudget` ("limit message must carry the cumulative charged count of 2") and `TestAdversarialConcurrentAdmissionChargesExactlyThreeOfFive` ("delegate outcomes = (5 admitted, 0 refused), want (3, 2)") |
| M3 drop depth check in `admitSpawn` AND always allow delegate in `childAgentManager` (dual; removing only one half is what the other half then bypasses) | `orchestration.go` | `TestDelegateStrippedFromChildAtDepthLimit` ("final response = nil, want the parent's closing answer" — the grandchild actually spawned and consumed the reroute) + `TestAdversarialDelegateDepthLimitReportsHonestDepth` (refusal text no longer the honest depth numbers) |
| M4 deny-mode asker returns parent asker instead of `denyAllAsker` (deny-mode fail-closed branch removed) | `orchestration.go` | `TestAdversarialDenyModeFailsClosedOnEveryAsk` → "child tool executed 2 times in deny mode, want 0" |
| M5 remove ask locking (both branches) | `scheduler.go` | `TestAdversarialConcurrentChildPromptsAreSerialized` → "deadlock detector: the second child prompt never appeared after the first resolved" (same honest reason as M1: the two asks stopped serializing) |
| M6 make `redact.Scrub`'s pattern loop a no-op (skip `p.re.ReplaceAllString`) | `internal/redact/redact.go` | `TestAdversarialCanarySecretsNeverSurviveDelegation` ("canary leaked verbatim") + `TestAdversarialChildFailureErrorTextCanary` ("canary leaked verbatim"); `TestAdversarialExactValueRegistryCanary` stays green as intended (separate replacement path). Note: mutating `composeDelegateResult`'s own scrub alone is masked by defense-in-depth (scheduler event scrub + run-loop history scrub), so the mutation targets the one shared primitive instead |
| M7 `withDelegateTool` never appends delegate | `orchestration.go` | `TestWithDelegateToolVisibility` ("withDelegateTool(orch, [echo]) = [echo], want [echo delegate]"). Honest masking note: the planned e2e names (`TestDelegateRunsChildAgent`, `TestDelegateTaskEntersAsUserMessageOnly`) stay green under this mutation because that unit-test universe wires `r.manager = full` directly, so scheduler lookup bypasses the model-visible definition list — capability-grant mutations need the pure-function pin |
| M8 `validateOrchestration` returns nil always | `config.go` (copy-backup only) | `TestAdversarialOrchestrationRejectsMalformedTypes` ("Load() accepted malformed orchestration input") + `TestAdversarialDisabledOrchestrationStillValidates` ("disabled orchestration must still validate child_permissions, got err=<nil>") + `FuzzValidateOrchestration` seeds ("accepted a combination outside every documented bound") |
| M9 remove the deferred sem release | `delegation_tool.go` | `TestAdversarialDelegateDepthLimitReportsHonestDepth` ("semaphore slot leaked after the depth refusal: 1 in use") + `TestAdversarialSemaphoreCapsInFlightAndFreesExactlyOneSlot` ("deadlock detector: only 2/3 children entered ... peak=2") |
| M10 remove pre-acquire ctx skip AND post-acquire stopped guard (dual-layer; verified first that removing only the post-acquire guard is masked by the pre-acquire `<-ctx.Done()` select case — the test stayed green under the guard-only mutation) | `scheduler.go` | `TestAdversarialSchedulerStoppedGuardSkipsQueuedCalls` (`-count=2` deterministic) → "deadlock detector: batch never completed after cancellation" (the queued call executes after stop and parks on its un-opened release channel) |

A security test that stays green under its mutation is a coverage gap; every row
above failed as recorded. The two rescoping decisions (M6 → shared redact
primitive, M10 → dual-layer stop guard, M7 → pure-function pin) are themselves
findings about defense-in-depth layering and test wiring visible only through
mutation.

## 8. Verification ladder (Stage E)

1. Focused: `go test ./internal/runtime -run 'Adversarial|Fuzz'` and
   `go test ./internal/config -run 'Orchestration.*'`.
2. Stress: `-count=10` on the focused runtime set.
3. Fuzz smoke: `-fuzztime=10s` per target.
4. Whole repo: `go test ./...`, `go vet ./...`, `gofmt -l` (excluding user files
   untouched state).
5. Race detector: `go test -race ./internal/runtime ./internal/config` — CGO/gcc
   unavailable locally (confirmed; documented in the final report with the exact
   CI command to run).
6. Mutation checks (§7) — one at a time, revert between.
7. Single local commit at completion; no push, no PR, no remote change.

## 9. Stage status log

- Stage 0 (repo protection): complete — branch `feat/orchestration-adversarial-tests`
  cut from `4cf3149`; user modifications (docs/Tools.md, internal/session/session.go,
  internal/tools/search/*) and untracked user test files remain untouched/unstaged.
- Stage A (recon + threat model): complete — this document + confirmed defects O1/O2/O3.
- Stage B (adversarial review of the test plan): complete — fixture redesigns recorded
  (gated asker, honest-report accounting, canary property); see §3/§4 notes.
- Stage C (TDD implementation): in progress —
  - Group 1 (config boundaries): `internal/config/orchestration_adversarial_test.go`
    green (`-count=3`), O1+O2 fixes landed with truthful validation.
    Now also holds `FuzzValidateOrchestration` (bounds ⟺ error predicate,
    O2 cross-field rule included; `-fuzztime=10s` smoke clean).
  - Group 2 (permission isolation): `internal/runtime/orchestration_permissions_adversarial_test.go`
    green (`-count=3`), 9 tests.
  - Group 3 (resource/admission): `internal/runtime/orchestration_resource_adversarial_test.go`
    green (`-count=5` on the three timing-sensitive tests, `-count=2` on the group,
    full runtime package green after), 12 tests [R]×7/[U]×5.
  - Group 4 (secrets canary): `internal/runtime/orchestration_canary_adversarial_test.go`
    green, 3 tests [R] — shape canaries (FFSEC marker embedded in every
    synthetic secret body, 10 redact shapes), exact-value registry canary
    (`AddSecret`/`ResetSecrets` cleanup), child-failure error-text canary.
    Exhaustive drain scan helper (`eventCanarySurfaces`) plus model-window
    scan (`messageRecordingProvider`).
  - Group 5 (hostile output): `internal/runtime/orchestration_hostile_adversarial_test.go`
    green, 2 tests [R] — fence-escape injection pinned at the real provider
    window (one real closing fence, hostile close inert, evidence kept),
    oversized report cut with honest shared marker (kept=5000, real total,
    head/tail anchors).
  - Group 6 (lifecycle/sibling attribution): `internal/runtime/orchestration_lifecycle_adversarial_test.go`
    green, 2 tests [R] — no `[delegate: …]` progress after the delegate
    terminal (parent-stream join observable); sibling reports/progress
    attribute to their own child only (two agents, distinct turn counts,
    per-agent Sscanf parsing). Integrity/idempotency addition beyond the
    pinned `duplicate_history_test.go` + `TestDelegateDuplicateCallIdReplaysRecordedResult`
    is exactly these attribution gaps; same-turn duplicate IDs remain
    pinned repo behavior ("no cross-turn identity yet"), unchanged.
  - Group 7 (fuzz): `internal/runtime/orchestration_adversarial_fuzz_test.go`
    green — `FuzzDelegateArgs`, `FuzzTruncateDelegateBody`,
    `FuzzFenceToolResult` (see §5 for exact properties). All four targets
    smoke-tested `-fuzztime=10s` clean, corpus replay green.
  - Remaining: TUI origin [R] + O3 fix; then Stage D mutations (M1–M10),
    Stage E ladder, final report.
- Stage D (independent audit + mutations): complete — all ten rows M1–M10
  executed one at a time against copy-backups in `%TEMP%\opencode\stageD\`
  (scheduler.go, orchestration.go, delegation_tool.go, redact.go, config.go),
  every row observed failing at least one named test for its recorded reason,
  every restore verified green afterwards (see §7 for exact texts). Two rows
  were rescoped during execution: M6 → `redact.Scrub` shared primitive
  (defense-in-depth masks a compose-layer-only mutation), M10 → dual-layer
  stop guard (pre-acquire ctx skip masks a guard-only mutation). One honest
  masking note: M7 is caught by the pure-function pin
  `TestWithDelegateToolVisibility`, not by the e2e names, because that unit
  universe wires `r.manager = full` directly.
- Stage E (verification + final report): complete — see §10.

---

## 10. Final report

### Deliverable

A dedicated adversarial security/robustness suite for the orchestration
layer, mandated evidence levels kept separate, deterministic throughout
(barriers/channels, never sleeps) with the honest widened-window exceptions
documented where timing genuinely factors in. All new files:

| File | Contents |
|---|---|
| `internal/config/orchestration_adversarial_test.go` | Group 1: exact-boundary/±1 config tables, disabled-mode still-validates, malformed types/overflow, O1+O2 pins, `FuzzValidateOrchestration` (8 tests + 1 fuzz) |
| `internal/runtime/orchestration_resource_adversarial_test.go` | Group 3: admission/semaphore/depth honesty, concurrency cap, stop-guard, staged cancellation (12 tests) |
| `internal/runtime/orchestration_permissions_adversarial_test.go` | Group 2: child isolation of permission state, deny-mode fail-closed, interactive serialization (9 tests) |
| `internal/runtime/orchestration_canary_adversarial_test.go` | Group 4: FFSEC shape canaries across all 10 redact shapes + provider-window scan, exact-value registry canary, child-failure error-text canary (3 tests) + shared drain/scan fixtures |
| `internal/runtime/orchestration_hostile_adversarial_test.go` | Group 5: fence escape pinned at the real provider window, oversized report honesty (2 tests) |
| `internal/runtime/orchestration_lifecycle_adversarial_test.go` | Group 6: no child progress after delegate terminal, sibling attribution with per-agent turn counts (2 tests) |
| `internal/runtime/orchestration_adversarial_fuzz_test.go` | Group 7: `FuzzDelegateArgs`, `FuzzTruncateDelegateBody`, `FuzzFenceToolResult` (3 fuzz targets) |
| `internal/tui/permission_adversarial_test.go` | TUI origin attribution [R] (1 test) |
| `internal/runtime/testdata/fuzz/FuzzTruncateDelegateBody/c7d72faf75885eed` | Pinned fuzz crasher (minimal-progress fallback boundary) |
| `internal/config/config.go`, `internal/tui/permission.go` | Minimal fixes only: O2 cross-field validation, O1 truthful load-time rejection (in config.go), O3 origin line (in permission.go) |

Totals: 37 deterministic adversarial tests (config boundaries 8, permission
isolation 9, resource/admission 12, secrets canary 3, hostile output 2,
lifecycle/attribution 2, TUI origin 1) and 4 fuzz targets (3 runtime + 1
config), plus the O1/O2/O3 regression pins inside the named files. No
production behavior was rewritten.

### Defect decisions

- **O1** (resolved): a `delegate` name under `agents.*.tools` with
  orchestration disabled now fails at `config.validate()` with the truthful
  message, instead of failing tool-universe construction with a misleading
  "unknown tool". Fail-closed semantics preserved; only the message and
  timing changed.
- **O2** (resolved): `validateOrchestration` (and the CUE schema) rejects
  `max_concurrent_children < max_depth`, eliminating the silent starvation
  stall for nested delegation configs.
- **O3** (resolved): TUI permission modal renders "Requested by child agent:"
  with the stdin asker's chain-fallback (chain when present, child name
  otherwise), nothing fabricated for top-level asks. Display-only.

### Design notes

N1–N3 (§6) unchanged; Stage D gave N2 concrete weight — M9's mutation
evidence observed `orch.sem` directly (the cap is one orchestrator's sem,
not a process-wide lane). No production change requested.

### Mutation evidence

All ten rows executed and reverted; 10/10 caught at least one named test,
3 rescopes recorded with reasons, 1 honest masking note (M7). Full texts
in §7. One fuzz crasher discovered by the suite itself and pinned.

### Verification ladder results (Stage E)

1. Focused: `./internal/runtime -run 'Adversarial|Fuzz'`,
   `./internal/config -run 'Orchestration|Fuzz'` — green.
2. Stress `-count=10` on both focused sets — green (2.9s / 1.6s).
3. Fuzz smoke `-fuzztime=10s` per target — all four green post-stage-D;
   one crasher (`c7d72faf75885eed`) found, understood as upstream's
   minimal-progress fallback, property refined, artifact pinned and replayed
   green.
4. Whole repo: `go build ./...` clean; `go vet ./...` clean; `gofmt -l internal/`
   clean; `go test ./...` all packages ok (39 packages, runtime 14.0s–21.2s
   across runs).
5. Race detector: **outstanding locally** — exact evidence:
   `go test -race` → `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`;
   with `CGO_ENABLED=1` → `runtime/cgo: cgo.exe: exit status 2` (no C compiler).
   CI path (must run before merge): `CGO_ENABLED=1 go test -race
   ./internal/runtime ./internal/config ./internal/tui -run
   'Adversarial|Fuzz|Permission'` on a runner with a C toolchain.
6. No push, no PR, no remote change; single local commit of the suite + the
   O1/O2/O3 minimal fixes, excluding all user files (docs/Tools.md,
   internal/session/session.go, internal/tools/search/* and the user's
   untracked test files).

### Outstanding

- The `-race` run is the one ladder step that must be executed in CI (item 5);
  everything else is fully verified locally.
