# Forcefield Audit Reconciliation — post-RC6 + FF-SEC-003

**Date:** 2026-09-08
**HEAD:** `e65d990` (+ uncommitted FF-SEC-003 change: `internal/runtime/scheduler.go`, `internal/runtime/scheduler_always_scope_test.go`, `docs/Sandbox.md`)
**Mode:** read-only investigation + audit-document updates (no source changes)
**Method:** every status below was re-verified against current source and tests. Commit messages and old conclusions were treated as claims, not evidence.

**Historical baselines (preserved, not rewritten):**
- `audit/5DAY_AUDIT_REPORT.md` (2026-08-31, target `e10e62a`+dirty): **38/100** (scored 40 in-table), verdict **NOT YET**.
- `audit/COMMIT100_AUDIT_REPORT.md` (2026-09-07, target `136c0b4`+dirty): Engineering 68 / Security 52 / Reliability 64 / UX 70 / Long-Horizon 48 / Production 55.
- `audit/RC6_HARDENING_REPORT.md` (2026-09-07, tip `787d0f7`): CONDITIONAL GO (milestone) / NO-GO (unattended, adversarial).

**Status vocabulary:** FIXED · FIXED WITH LIMITATIONS · PARTIALLY FIXED · STILL OPEN · ACCEPTED RISK · DEFERRED · REGRESSED · UNVERIFIED. A documented limitation is not a fix. A passing test proves the tested behavior, not universal safety.

---

## 1. Latest change verified: FF-SEC-003 per-command Always scope

**Historical:** PARTIALLY FIXED (session-scoped, still per-tool-name).
**Current: FIXED WITH LIMITATIONS.**

- What was wrong: one `Always allow shell` authorized every future shell command in the session (`sessionAllow map[tool]Decision`).
- What changed (working tree, uncommitted): `sessionAllowKey()` scopes `shell`/`shell_job` Allow records by `name + \x00 + TrimSpace(command)` (`internal/runtime/scheduler.go:426-459`); shell_job actions without `command` fall back to the tool key; `AlwaysDeny` stays per-tool-name (fail-closed, checked first at `:477-481`); sensitive-path escalation untouched (`:482-497`); docs updated (`docs/Sandbox.md:156-163`).
- Proof: 5 new tests pass (`scheduler_always_scope_test.go`: scoped reuse, whitespace normalization, non-persistence, per-tool preservation for `read_file`, broad deny, shell_job scoping); full `go test ./...`, `go build`, `go vet`, `gofmt` clean; `go test -race ./internal/runtime/ ./internal/session/` clean. The "7 PASS" figure = 5 new + 2 pre-existing Always tests.
- Limitations (why not unqualified FIXED): normalization is `TrimSpace` only (no semantic equivalence — intentional, exact text is used); cwd/env/timeout are NOT part of the Allow key; non-command tools keep per-tool scope; no expiry. Severity of remainder: MEDIUM.
- Note: the fix is currently **uncommitted**, so `HEAD` alone does not contain it (same structural caveat as FF-HYG-001).

---

## 2. Finding-by-finding reconciliation

### 2.1 Original CRITICAL findings

| ID | Was | Now | Why (evidence) |
|---|---|---|---|
| FF-SEC-001 WSL shell `/mnt` escape | STILL OPEN | **STILL OPEN** (HIGH, was CRITICAL) | Mechanism unchanged: shell text unconfined in every mode. Mitigations added since: WSL lexical deny (+shell_job parity `job_tool.go:166`), honest docs, per-command Always narrowing. Downgraded CRITICAL→HIGH because exploitation now requires an approval (default `ask`) and is honestly documented — not because the primitive closed. |
| FF-SEC-002 native zero isolation + auto-allow | STILL OPEN | **Split: (a) ACCEPTED RISK, (b) PARTIALLY FIXED** | (a) No confinement in permissive mode is a documented usability choice (RC6 §6, `Sandbox.md`). (b) Silent reads narrowed: sensitive-path escalation forces Ask (`scheduler.go:482-497`), central redaction + env registry at all persist/display surfaces, per-command Always. Non-sensitive reads still auto-allow by design. |
| FF-CRIT-003 unbounded context | PARTIALLY (window) | **FIXED WITH LIMITATIONS** | Per-turn token+count window (`runtime.go:57`, `context.go`), reserve, caps negotiation, pair-atomic groups, opt-in digest, FinishLength→Blocked, 1000-msg session cap + marker. Residual: system prompt bypasses budget; digest off by default; in-memory growth within a run (iteration-bounded by default); no rotation. |
| FF-SEC-003 global Always | PARTIALLY | **FIXED WITH LIMITATIONS** | See §1. |
| FF-SEC-005 shell OOM | HIGH | **FIXED** | 2 MiB shared cap + metadata + tests (`shell.go`, `limits.go`, `shell/limits_test.go`). Residual (documented): Content duplicates capped streams (~4 MiB in-mem worst case) + 6000c runtime guard. |
| FF-SEC-006 prompt injection | HIGH | **PARTIALLY FIXED** | Fence + closing-tag escape verified (`session/scrub.go:13-18`, `scrub_test.go:67-75`); memory prompt text unfenced by design (deferred); no red-team proof exists. |
| FF-SEC-007 session secret leak | HIGH | **FIXED WITH LIMITATIONS** | Central `internal/redact` (patterns + deep ScrubMap + env registry), applied at tool results, progress chunks, provider errors, session persist (content+args+pending), memory, doctor lines, permission display; sensitive escalation blocks silent `.env` reads. Residual: user-approved content still reaches the model by design; files remain plaintext 0600 by design. |
| FF-SEC-009 concurrent ask | HIGH | **FIXED** | `askMu` serialization (`scheduler.go:54-57,560-561`) + cancel-maps-to-Cancelled + tests. |
| FF-REL-001 5xx/timeout/mid-stream | HIGH | **PARTIALLY FIXED** | Transport 5xx (≠501)/timeout/connection retried with backoff+jitter+Retry-After; turn-level clean-only retry (`maxTurnRetries=2`); mid-stream stays fatal BY DESIGN (accepted — replay would duplicate side effects); slow-TTFB Ollama cold loads deferred. |
| FF-REL-002 session growth | HIGH | **PARTIALLY FIXED** | 1000-msg cap + `[compacted]` marker + `Compacted` count + skip-markers-in-replay + tests. Residual: silent-ish drop (marker in file only), no rotation/GC/delete/TTL, TUI drops markers. |
| FF-TUI-001 resume hides tools | HIGH | **FIXED** | Tool history renders as collapsed blocks (`tui/model.go:381-402`, `resume_test.go`). |
| FF-SEC-004 interactive bypass | HIGH | **PARTIALLY FIXED** | `bash -c` recursion added (`shell.go:849-887`, bypass test); remains heuristic by design, backed by Stdin=nil + timeout/kill. |

### 2.2 Original MEDIUM findings

| ID | Was | Now | Why |
|---|---|---|---|
| FF-SEC-010 TOCTOU | PARTIALLY | **PARTIALLY FIXED** (unchanged) | Unix O_NOFOLLOW + re-validation + EvalLinks; Windows open no-ops; secret_scan has neither. Documented limitation. |
| FF-SEC-011 timeout unbounded | MEDIUM | **FIXED** | 300s cap + `ClampTimeout` + scheduler ceiling + tests. |
| FF-SEC-012 Windows kill | MEDIUM | **PARTIALLY FIXED** (unchanged) | taskkill + fallback + drain; grandchildren survive without job objects (deferred). |
| FF-SEC-015 | dup of REL-001 | **Same as FF-REL-001** | — |
| FF-SEC-016 lenient args | MEDIUM | **FIXED** (corrects COMMIT100 §6 "gap retained") | Strict `ValidateArgs` (rejects unknown fields/wrong types, `tools/validate.go:12-82`) runs BEFORE permission (`scheduler.go:240` before `:256`). Verified in current tree. |
| FF-CTX-002 FinishLength→Done | MEDIUM | **FIXED** | Blocks with and without tool calls (`runtime.go:1832-1835`); test pins no-exec (`finish_length_test.go:49-93`, hardening variant). |
| FF-PERM-001 prompt truncation | MEDIUM | **ACCEPTED RISK** (by design) | 300c + explicit note + scrub-before-truncate (`permission.go:220-229`). Malicious-tail concern retained but explicit. |
| FF-SESS-001 coalescing | MEDIUM | **PARTIALLY FIXED** | 10s window + ID dedup + turn check (`session.go:481-507`, `coalesce_test.go`); residual: time-based, not Turn.ID-based. |
| FF-OBS-001 no run log | MEDIUM | **PARTIALLY FIXED** | Opt-in local JSONL trace (redacted, 4 MiB cap, crash-kept); reasoning/deltas not saved; off by default. |
| FF-CFG-001 Go drift | LOW | **STILL OPEN** | `go.mod: go 1.26.4` vs `README.md:43 Go 1.22+`. Doc-only drift. |
| FF-DOC-001/002 drift | LOW | **PARTIALLY FIXED** | Config persist claim corrected (`Config.md:265` accurate); CLI list accurate; PRD aspirational only (acceptable). Remaining: Events table 6 vs 12 actual; TUI reasoning tags; Index gaps. |
| FF-HYG-001 dirty tree | MEDIUM | **STILL OPEN** (structural) | HEAD `e65d990` is clean of the old dirt, but the FF-SEC-003 fix is currently uncommitted — same irreproducibility shape. Resolves on commit. |

### 2.3 Original LOW findings

| ID | Was | Now | Why |
|---|---|---|---|
| FF-SEC-013 env key validation | LOW | **Residual LOW** | No charset check (`shell.go:977-997`), but no injection path: env travels via `exec.Env`, never shell-parsed. Strict arg typing added. |
| FF-SEC-014 pwd leaks cwd | LOW | **ACCEPTED RISK** | No policy on `pwd` (verified); TUI header shows cwd anyway. Trivial info disclosure. |
| FF-SEC-017 validID Windows | LOW | **STILL OPEN** | No reserved-name (`CON` etc.) handling (`manager.go:40-52`); failure mode is a failed save, not corruption. |
| FF-SEC-018 task trust | LOW | **FIXED** | `verification:"passed"` requires `verification_note` evidence; `verified` requires passed (`task_tool.go:126-141`). Truthfulness still unenforceable — inherent. |
| Dead code / dup atomic write | INFO | **Mostly unchanged** (+1 new: dead `runGit` helper in `git.go:323`, zero callers — INFO). Dual Version vars + event aliases are documented compat. |
| FF-CFG-003 negative limits | LOW | **PARTIALLY FIXED** | `tools.*` negatives rejected (`config.go:660-667`); `agent.*`/per-agent negatives silently default (CUE rejects, Go ignores — documented split). |
| C5 discovery leak | INFO | **STILL OPEN** (deferred, documented) | Detached fetch runs to timeout; result dropped if picker closed (`tui/discovery.go:44-49`). Bounded, minor. |

### 2.4 COMMIT100 FF-100 findings (deltas vs that report)

- **C01/C02** (=SEC-002/SEC-001): as §2.1.
- **C03 FinishLength-with-calls**: **FIXED** (verified §2.1).
- **H01 system/task bloat**: **PARTIALLY FIXED** — task 64/64/32 + Summary 8+300r bounded (`task/state.go:172-206,292-311`, `TestTaskStateBounded`); memory 200 entries + 8 KiB (`memory.go:240-275`, `TestMemoryStoreBounded`); system prompt still bypasses budget (residual).
- **H02**: §1 (FIXED WITH LIMITATIONS).
- **H03**: **PARTIALLY FIXED** — fence escape fixed + tested; memory unfenced deferred.
- **H04 nested leak**: **FIXED** — deep `ScrubMap`/`ScrubSlice` (`redact.go:149-189`, `TestScrubMapNested`).
- **H05 write unbounded**: **FIXED** — 5 MiB refuse + chunking note + test.
- **H06 mid-stream/TTFB**: accepted + deferred (unchanged).
- **H07 in-mem unbounded**: **PARTIALLY FIXED** — iteration-bounded by default; `Limits<=0` unlimited documented (`TestLimitsZeroMeansUnlimitedDocumented`).
- **H08 last-wins**: **ACCEPTED RISK** (parseability proven, no lock by design).
- **H09 overclaim**: **FIXED** — `FilesystemConfined:false` + honesty test (`native.go:72-99`).
- **H10 dirty tree**: **STILL OPEN** in its current instance (FF-SEC-003 uncommitted); the RC5 dirt itself was committed.
- **Quirks** (Gemini reasoning-as-Text, Gemini Stop-on-tool-turns, Ollama bare-Done/EOF, ListModels reuse, OC early gate release, SSE-oversize→Unknown): all re-verified present, all LOW/INFO — fidelity/classification issues, none affect execution correctness (runtime keys off `ToolCalls` presence; both EOF paths end non-success).
- **search `within()` case-sensitivity**: present (`search.go:425-437` vs `sandbox/policy.go` case-fold). Reframed: fail-closed (mismatch → deny), so availability-only on Windows, LOW.
- **env key gap**: = SEC-013 above.
- **CJK**: **FIXED** — conservative estimator + `TestTokenEstimationCJKBounded` (corrects the "no test" impression; test lives in `internal/hardening/`).
- **abandon runMu deadlock**: **MITIGATED** — buffered channel + non-blocking cancelled-send (`runtime.go:1628-1651`); residual theoretical only.
- **TUI-owned TurnState / headless ff run**: accepted design (ephemeral single-shot needs no repair).
- **10s coalescing, UpdatedAt churn, dead GlobalStore, memory stale cache**: unchanged (LOW/deferred/info as before).
- **job WSL parity**: **FIXED** (`job_tool.go:166` same lexical gate).
- **TOCTOU Windows/secret_scan**: unchanged, documented.
- **chat --resume trap**: **STILL OPEN** (`cmd/chat.go:25` always new; `--resume` only on root) — LOW UX.
- **stdout pollution**: **FIXED** (stderr notice, `config.go:315-318`).
- **/sessions conflation, /status bytes, uncapped transcript errors**: carried LOW (unchanged).
- **D1**: README CLI list accurate; PRD aspirational (acceptable). **D2**: FIXED (docs accurate). **D3**: memory.md references gone; sessions path documented. **D4**: generic probes (accepted).
- **opencode.json contradiction**: **RESOLVED** — file is ignored and not tracked (verified `git ls-files`).
- **runMu/EventCancelled/loop-detector/RunControl**: committed since (present in tree, tested). **runGit**: new dead helper (INFO, see N11).

### 2.5 RC6 accepted/deferred carry-forward

All RC6 §6 items stand as written, with two updates: FF-SEC-003 moves to FIXED WITH LIMITATIONS (§1), and H09/Strict-honesty is confirmed in current `native.go`. No accepted risk was found to be mislabeled; no deferred item was silently fixed or broken.

---

## 3. New issues found in this pass

| ID | Severity | Finding | Evidence |
|---|---|---|---|
| N1 | LOW | Trace arg JSON truncation cuts bytes (`trace.go:173-175`), can split UTF-8 (snippet path itself is rune-safe) | `trace.go:163-177` |
| N2 | LOW | `Save` failure rollback restores Messages/UpdatedAt but not `Compacted` count | `manager.go:71-79` vs `:166` |
| N3 | LOW | Reused (deduped) results still count toward `ToolCallCount`/`ConsecutiveFailures` | `runtime.go:1866-1868`; arguably correct (stuck detection), noted for awareness |
| N4 | LOW | Oversized system prompt bypasses budget (always sent full) | `context.go:182-184`; config-author controlled, low likelihood |
| N5 | LOW | SSE oversize surfaces as `Unknown`, not `Protocol` | `sse.go:54-55` vs `errors.go:97-100` |
| N6 | LOW | Ollama clean-EOF→`UnexpectedEOF` vs others' complete-then-flag | `ollama.go:275-280`; both paths end non-success |
| N7 | LOW | Gemini thought parts emitted as Text; tool turns labeled Stop | `gemini.go:431-447`, `:192-201`; fidelity only |
| N8 | LOW | Ollama bare-Done carries no StopReason/Usage | `ollama.go:315-317`; observability only |
| N9 | INFO | Ollama ListModels reuses one `*http.Request` across retries | `ollama.go:84`; harmless for bodiless GET |
| N10 | LOW | OpenAI ListModels releases gate before decode | `openai_compatible.go:612-613`; weakens single-flight slightly |
| N11 | INFO | Dead `runGit` helper (zero callers) | `git.go:323` |
| N12 | LOW | TUI permission display scrubs strings only; non-string args marshaled raw | `permission.go:224` vs `:250-258`; display-transient only |
| N13 | LOW | TUI drops `[compacted]` system markers from transcript | `model.go:403-405` vs `session.go:158-162` |
| N14 | LOW | `ff chat --resume` silently starts a new session | `cmd/chat.go:24-31` |
| N15 | LOW | Events table documents 6 of 12 types | `docs/Runtime.md:59-66` vs `event.go:14-31` |
| N16 | LOW | `tools.cue` documents load_skill/update_task_state as `allow`; effective default is `ask` | `cue/tools.cue:70-78` vs template (absent → default ask) |
| N17 | LOW | `search.within()` case-sensitive vs sandbox case-fold | `search.go:425-437`; fail-closed direction |
| N18 | LOW | Job retention-32 eviction implemented but untested | `jobs.go:440-443`, no test reference |

**Regressions: none found.** Every behavior change since COMMIT100 moved toward fixed and is pinned by tests; the dirty RC5 wave was committed; no protection was weakened. (The two systematized re-checks — ValidateArgs order and CJK coverage — contradicted COMMIT100's wording in the *safe* direction: both are fixed with tests.)

---

## 4. Re-scores (5DAY SPEC §33 methodology, weights in parens)

| Category (wt) | Was | Now | Δ-weighted | Justification |
|---|---|---|---|---|
| Reliability (15) | 4 | **7** | +4.5 | Loop+limits+turn-retry+idempotency+loop-detector+FinishLength/missing-Done handling verified; residual: mid-stream fatal (accepted), no auto-restart/SIGHUP, in-mem within-run growth |
| Provider resilience (10) | 4 | **7** | +3.0 | 429 + 5xx/timeout/connection retried, turn-level clean-only retry, caps negotiation, per-provider TTFB hints; residual: mid-stream fatal, slow Ollama TTFB, no fallback |
| Rate-limit resilience (10) | 6 | **6** | +0.0 | Bounded+jitter+Retry-After+quota unchanged and correct; `RESOURCE_EXHAUSTED`/`insufficient_quota` still missed; no global limiter |
| Context management (10) | 1 | **7** | +6.0 | Dual count+token window, reserve, caps negotiation, CJK-safe estimator, FinishLength blocked, digest opt-in; residual: system bypass, digest off default, no rotation |
| Session durability (10) | 6 | **8** | +2.0 | Atomic+corrupt-isolation+1000 cap + marker + TurnState/recover/repair + rendered history; residual: silent-ish drop, no rotation/GC/delete, last-wins, UpdatedAt churn |
| Process/resource (10) | 3 | **7** | +4.0 | Shell 2MiB/300s/kill, jobs bounded, task/memory/write/session/context caps, scheduler 4, MaxTimeout; residual: Windows grandchild, no CPU quota, file-count GC none, disk-fill via write count |
| Tool safety (10) | 4 | **6** | +2.0 | Strict/WSL confinement, strict validation pre-permission, interactive recursion, git allowlist, search/find caps+exclusions; residual: shell text unconfined everywhere, native default, heuristic/Windows gaps |
| Security (10) | 3 | **6** | +3.0 | Central redact+registry+boundaries, fence+escape, per-command Always + broad deny, escalation, askMu, no-config-secrets; residual: permissive defaults, lexical bypass, unfenced memory, per-name non-command Always, plaintext session files |
| Recovery (5) | 4 | **7** | +1.5 | Interrupted-never-rereun recovery, repair pairing, heal, atomic saves, manual resume path; residual: 1 iteration lost, no auto-restart/SIGHUP |
| Observability (5) | 3 | **6** | +1.5 | Opt-in redacted JSONL trace, doctor probes+honesty+scrubbed lines, actionable errors, task snapshots; residual: off default, no reasoning/deltas, no verbose/tokens |
| TUI/UX (3) | 7 | **8** | +0.3 | Resume renders tools, permission UX, cancel/quit split, pickers; residual: marker drop, chat-resume trap, no history, footer gaps |
| Cross-platform (2) | 6 | **7** | +0.2 | 3-OS CI+race, honest relay, EvalLinks junctions tested, rename retry; residual: kill best-effort, no job objects, Bash required |

**Total: 67.8 → 68/100** (was 38/100 scored, 40 in-table).

Score discipline notes: Context +6 and Process +4 are the largest moves, each tied to a closed P0 failure mode (400-abort, OOM) with tests; Security moves only +3 because the default-permissive exposure is unchanged; Rate-limit is flat because quota-phrase brittleness persists.

---

## 5. Five-day verdict

### NOT YET

Unattended five-day autonomy is still not recommended. What changed is the *distance*: the original three systemic guarantees of failure (context exhaustion within hours, any 5xx killing the run, unbounded shell OOM) are closed, and the failure budget table is now mostly recoverable-or-explicit. What keeps the verdict:

1. **No auto-restart/resume.** Terminal close, reboot, crash, sustained-429/quota halt, and mid-stream abort all park until a human runs `ff --resume`. Over 120h, at least one such event is likely; each is fatal-until-human.
2. **Defaults remain permissive.** An unattended run on stock config still grants broad reads and full user-privilege execution — safe only with deliberate `strict` + `ask` deployment.
3. **No rotation/GC.** Sessions/traces grow file-count without bound; disk needs watching.

vs historical `38/100 NOT YET`: same verdict word, different substance — then, failure was *certain within hours* (context/OOM/5xx); now, failure requires an operational event (blip/reboot/quota/disk) with recovery paths documented.

**First likely bottleneck:** a provider-side interruption (sustained 429, quota trip, or mid-stream abort) halting the run pending manual resume — or, operationally, terminal-close/reboot with no supervisor.

- **Best case** (strict, stable provider, tmux/nohup, watched billing/disk, non-adversarial repo): multi-day supervised runs plausible; five days reachable with one or two manual resumes.
- **Expected case:** a rate-limit/quota/mid-stream event parks the run within the first day or two; state is safe and resume works; context/session stay bounded throughout.
- **Failure-prone case** (stock native config, untrusted repo, Windows, truly zero supervision): prompt-injection hijack or secret exfiltration within hours; grandchild accumulation over days.

---

## 6. Top 10 current problems (severity × likelihood × impact × exposure)

| Rank | Problem | Why now |
|---|---|---|
| 1 | No auto-restart/resume after close/crash/reboot/quota-halt | Sole remaining certain-killer of unattended runs; every other P0 is bounded |
| 2 | Permissive defaults (unconfined FS + auto-allow reads) | Accepted risk, still the top security exposure; strict exists but is opt-in |
| 3 | Shell text unconfined in all modes (lexical mitigation bypassable) | Prompt-injection → exfil path persists where approvals lapse |
| 4 | Sustained-429/quota handling (RESOURCE_EXHAUSTED miss, no global limiter, billing halt) | Most likely provider-side halt over 120h |
| 5 | Mid-stream abort requires manual resume | By design; second most likely provider-side halt |
| 6 | Session/trace file-count growth without rotation/GC/delete | Disk watch required; no TTL |
| 7 | In-memory run growth (messages within run; `Limits<=0` unlimited) | Bounded by default caps; programmatic zero is a footgun |
| 8 | Unfenced memory prompt text (hijack persistence) | Deferred; poisoning survives across turns |
| 9 | Windows grandchild accumulation (no job objects) | Deferred; 5-day Windows exposure |
| 10 | Per-name Always for non-command tools + no expiry + no cwd/env/timeout in key | Narrowed for shell; remainder is MEDIUM residual |

Fixed items removed from the active list (kept in history above): shell OOM, 5xx-no-retry, concurrent-ask overwrite, session leak via persist, FinishLength exec, timeout unbounded, nested scrub, write unbounded, fence escape, overclaim, stdout pollution, resume-blindness, WSL job parity, chat-resume… (chat-resume stays: N14, folded into TUI backlog, not top-10).

## 7. Quick wins (value-ordered, small)

1. Add `RESOURCE_EXHAUSTED`/`insufficient_quota` to quota phrases (`providers/retry.go:107-114`) — one-line billing-halt fix.
2. Inherit `--resume` in `ff chat` ( persistent flag or explicit handling) — kills N14 trap.
3. Render `[compacted]` markers in transcript (`tui/model.go` system case) — closes N13.
4. Rune-safe `scrubArgs` cut (`trace.go:173-175`) — N1, five lines.
5. Restore `Compacted` in Save rollback (`manager.go:71-79`) — N2, three lines.
6. Complete Events table (`docs/Runtime.md:59-66`, `docs/TUI.md`) — N15, docs-only.
7. Remove dead `runGit` (`tools/git/git.go:323`) — N11, deletion.
8. Align `tools.cue` load_skill/update_task_state with effective `ask` (or vice versa with justification) — N16.
9. Add retention-eviction test (jobs 32) — N18.
10. Reject per-agent negatives in Go config validation (match CUE) — closes deferred item.

## 8. Roadmap deltas

- **Immediate:** items 1–5 above + commit the FF-SEC-003 change (resolves FF-HYG-001's current instance) + `ff run` non-interactive Always scoping docs line.
- **Near-term (unchanged from RC6):** rotation/GC/delete + TTLs; Windows job objects; memory `<memory>` fence; per-command Always expiry; TUI marker rendering + picker search; SBOM/signing; `-count`/`-shuffle` stress in CI.
- **Long-term (unchanged):** supervisor/auto-restart + SIGHUP; session backend abstraction; OTel hook; LLM summarization service; MCP/multi-agent only with new threat models.

## 9. Stale-claim resolutions (Phase 9)

- `FF-SEC-003 per-tool Always` (all audit docs + `docs/Sandbox.md` pre-fix text): SUPERSEDED for shell/shell_job by per-command scoping; the working-tree `Sandbox.md` already describes the new behavior. Older report text is historical.
- `FF-SEC-016 gap retained` (COMMIT100 §6): SUPERSEDED — strict validation now precedes permission.
- `opencode.json tracked+ignored` (5DAY §25): RESOLVED — ignored, not tracked.
- `ff run stdout pollution` (COMMIT100): RESOLVED — stderr notice verified.
- `D2 persist lie` (COMMIT100 §24): RESOLVED — docs accurately describe SaveConfig.
- `Strict FilesystemConfined:true overclaim` (COMMIT100): RESOLVED — honestly `false`.
- `runMu/EventCancelled/loop-detector uncommitted` (COMMIT100): RESOLVED — committed and tested.
- `CJK untested` (impression): RESOLVED — `internal/hardening/context_test.go` pins conservative estimation.
- `go 1.26.4 vs 1.22+`, Events 6/12, chat-resume, permOptionGap, dual-Version, event aliases, flake goreleaser, PRD aspirational CLI: CONFIRMED STILL PRESENT (product-doc/hygiene backlog, unchanged severity).
- Historical scores/verdicts in old reports: left untouched and labeled historical (see notes appended to each report).

## 10. The ten questions

1. What was wrong before? §2 tables (38/100 baseline: unbounded context/OOM, fatal 5xx, global Always, silent reads, injection-prone replay, invisible history).
2. What has actually been fixed? §2 FIXED/FIXED-WITH-LIMITATIONS rows, each with file:line + test evidence above and in §§2–4 sweeps.
3. What remains dangerous? Top 10 §6 rows 1–5 (supervisor gap, permissive defaults, shell-text exfil path, billing halt, manual-resume aborts).
4. What is merely deferred? RC6 §6 list as carried: slow-TTFB, memory fence, GC/rotation, TUI markers/pickers, job objects, stdin/discovery leaks, global memory, SBOM/signing — plus N13/N14/N18.
5. What is intentionally accepted? Native/permissive privilege, WSL lexical limits, mid-stream fatality, last-wins sessions, PERM truncation display, `pwd`/cwd disclosure, zero-limits footgun (documented), headless-run TurnState absence.
6. Current score? **68/100** (§4).
7. Five days unattended? **NOT YET** (§5) — same word as 38/100, different substance.
8. Single highest-risk remaining issue? **No auto-restart/resume** (operational certainty over 120h).
9. Best next fix? **Supervisor/resume story** architecturally; tactically the quick wins §7 items 1–2 (quota phrases, chat-resume flag).
10. Which historical findings closed? FIXED: SEC-005/009/011, CTX-002, TUI-001, SEC-016, SEC-018, C03, H04, H05, H09, stdout pollution, D2, opencode.json, WSL job parity, CJK. FIXED WITH LIMITATIONS: CRIT-003, SEC-003, SEC-007. Rest in §2.
