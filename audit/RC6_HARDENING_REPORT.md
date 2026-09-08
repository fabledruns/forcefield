# RC6 Hardening Report — RC5 baseline → test lab → harden → regression → final audit

## 1. Executive summary

RC5 (commit 100 + working-tree hardening) failed safely on resources but lied
in three places (Strict `FilesystemConfined`, Config persist-vs-memory,
README CLI/sessions) and executed partial output (FinishLength with tool
calls), accepted incomplete streams as success (missing Done), leaked nested
secrets (`ScrubMap` shallow), allowed fence breakout (`</tool_result>`
verbatim), and grew task/memory/write/session-context without explicit
bounds. This cycle reproduced each failure with a deterministic test first
(P1.15 lab, 17 files), then fixed the code with targeted reviewable changes
(no new dependencies, no rewrites), proved each fix with package-level
regression + race/shuffle, and aligned contracts so the system no longer
overclaims isolation. Full suite green: `go vet`, `go test`, race on
runtime/session/hardening/redact/task/memory, `-race -count=10 -shuffle=on`
on scheduler/fence/compaction paths, clean build, clean tree.

Remaining gaps are documented accepted risks or deferred work, not silent
behavior: shell text unconfined in every mode, per-name session Always,
unfenced memory prompt text, mid-stream fatal-by-design (no resume to avoid
duplication), slow-TTFB header bound, same-session last-wins, trace/cross-
process GC, TUI marker rendering. Verdict: **CONDITIONAL GO** for RC6 as a
hardening milestone with gated usage; **NO-GO** for unattended multi-day or
adversarial production.

## 2. RC5 baseline

- HEAD before work: `136c0b4` (commit 100) with 13 modified + 7 untracked
  (loop_detector, run_control, loading, new.go, go.mod dep promotion,
  runtime/event/jobs/TUI changes, audit renames). All dirty changes
  intentional RC5 hardening — nothing discarded.
- Baseline checkpoint: `77f8b38` "chore: RC5 baseline checkpoint (commit 100
  + working tree hardening)" — reproducible parent of all hardening.
- Environment: `go 1.26.4 windows/amd64`, `GOOS=windows GOARCH=amd64`,
  version `ff version dev` (no ldflags).
- Baseline state: `go vet ./...` clean, `go build ./...` ok,
  `go test ./...` ok (runtime ~7s, sandbox ~11s, shell ~30s, tui ~9s),
  race clean on runtime/session. Architecture: CLI→TUI→Runtime→
  {Agent,Provider,Session,Tools,Skills,Config}; runtime owns loop,
  providers stream one turn, tools deterministic, sessions local,
  TUI presentation-only (except turn repair).

## 3. Files changed

Baseline `77f8b38` → tip `787d0f7` (5 commits):
- `985b87d` test(P1.15) lab (17 files, +1181).
- `f334515` fix(P1.16) ScrubMap deep + fence escape (+80/-11, 4 files).
- `53c7480` fix(P1.17/P1.18) FinishLength + missing-Done (+38/-24, 2 files).
- `d47a20b` fix(P1.19) task/memory/write/session/context bounds (+118/-18).
- `787d0f7` fix(P1.16/P1.20/P1.21) shell_job WSL parity, native Describe
  honesty, stderr notice, README/Config/Runtime/Session/Sandbox docs (+139/-25).

Production files: redact/redact.go, session/scrub.go + session.go,
runtime/runtime.go + context.go + finish_length_test.go, task/state.go,
memory/memory.go, tools/limits.go + filesystem/write_file.go,
tools/shell/job_tool.go, sandbox/native.go, config/config.go.
Docs: README, Config, Runtime, Session, Sandbox (+ hardening doc.go,
MANUAL_SOAK.md). No new dependencies (`go.mod` untouched after baseline).

## 4. Tests added

P1.15 lab (all deterministic, stdlib only, race-safe):
- `internal/hardening/`: doc.go, MANUAL_SOAK.md, helpers, soak (60–100
  iter growth + repeated compaction), provider_chaos notes (runtime-level
  mid-stream/missing-Done/cancel), tool_correctness via runtime,
  injection (fence escape + memory trust), filesystem (strict traversal,
  native documented, symlink refused, 8MiB write bound), shell (bounded
  output/timeout/ceiling/cancel/env), resources (limits-zero documented,
  task ≤64, memory ≤8KiB), concurrency (distinct-session + divergent
  same-session parseable), crash (corrupt surfaced, compaction marker,
  tmp never parsed), context (CJK ≥2000, ascii sane).
- `internal/runtime/`: hardening_soak (60 varying-arg iters, window
  ≤108 msgs), hardening_finishlength (Block, no exec — failed pre-fix),
  hardening_chaos (mid-stream Err→Error, missing Done→Error, cancel
  terminates).
- `internal/providers/hardening_chaos_test.go`: sustained 429 bounded at
  4 reqs, Retry-After clamp, 500→recovery no duplication, malformed→error,
  cancel-during-retry stops.
- Package regressions: redact TestScrubMapNested, session
  TestFenceToolResult_EscapesClosingTag, sandbox
  TestNativeStrictDescribeHonestAboutShell; updated
  TestFinishLengthWithToolCallsIsBlocked (explicit semantic change).

## 5. Hardening campaigns completed

- P1.15 lab: DONE (failing reproductions for C03/H01/H03/H04/H05/CJK/
  compaction-marker/missing-Done; passing chaos pins fixed transport).
- P1.16 security: DONE (deep scrub, fence escape, shell_job WSL parity,
  Describe honesty, sensitive-escalation kept, explicit security model
  in Sandbox.md; defaults preserved per RULE 4 with docs).
- P1.17 provider reliability: DONE (FinishLength-with-calls blocks
  without exec; missing-Done errors; sustained-429/Retry-After/5xx/
  malformed/cancel proven bounded; mid-stream stays fatal by design to
  avoid duplication — documented).
- P1.18 execution/concurrency: DONE (truncation correctness, idempotent
  IDs kept, cancel-vs-error preserved, divergent-save parseability
  proven; cross-process locking deferred with docs).
- P1.19 resource bounds: DONE (task 64/64/32 + Summary 8+300r,
  memory 200 entries + 8KiB note + reject-over-cap, write 5MiB refuse,
  session 1000 + marker + Compacted + skip-markers-in-replay,
  CJK-safe overestimate; config-zero already defaults).
- P1.20 cross-platform: DONE (parity + honesty + WaitDelay/drain kept;
  job objects deferred with docs; MANUAL_SOAK covers Windows checks).
- P1.21 contracts/docs: DONE (README CLI/sessions/memory/cwd,
  Config persist-vs-memory, Runtime truncation contract, Session
  retention, Sandbox security model, stderr notice).

## 6. Previous audit findings

RC5 Commit-100 audit (FF-100-*) re-verified by reproduction + fix + test:

FIXED:
- H04 nested ScrubMap leak → deep ScrubMap/ScrubSlice; proof
  TestScrubMapNested + hardening nested; limit: non-string scalars pass.
- H03 fence breakout → escape `</tool_result>` to `<\/tool_result>`;
  proof fence tests; limit: memory prompt text still unfenced (deferred).
- C03 FinishLength-with-calls exec → Block without exec; proof
  hardening + updated finish_length test; limit: conservative false-positive
  block possible on complete-but-long args (fail-safe).
- Missing-Done success-confusion → stream-without-Done errors; proof
  hardening chaos; limit: providers must always send Done (all built-ins do).
- H05 write unbounded → 5MiB refuse with chunking note; proof 8MiB test.
- H09 Strict overclaim → FilesystemConfined always false for shell +
  honesty test; limit: UI now correctly shows less.
- Stdout pollution → stderr notice; proof: `ff run` stdout clean.
- D1–D3 docs (CLI/sessions/memory/cwd/persist/marker) → corrected.

PARTIALLY FIXED:
- H01 task/memory/context unbounded → caps + truncation + CJK-safe
  estimation; remaining: programmatic Limits{0} still unlimited (config
  path defaults; documented), in-mem messages bounded by MaxIterations.
- FF-SEC-003 Always per-name → session-scoped (not persisted) +
  sensitive still asks; remaining: per-command scoping deferred (docs).
- FF-SEC-001/002 WSL/native non-isolation → parity + honesty + security
  model docs; remaining: no OS cage by design (accepted risk, no theater).
- FF-SEC-004 interactive heuristic → kept as heuristic + Stdin nil +
  timeout/kill as real containment (documented).
- FF-SEC-010 TOCTOU → Unix O_NOFOLLOW + re-validate kept; Windows no-op
  + secret_scan none documented as limitation.
- FF-REL-002 session growth → 1000 + marker + Compacted + replay skip;
  remaining: TUI transcript drops system markers (deferred UI), no GC.
- FF-OBS-001 logging → JSONL trace kept + marker/Compacted observability;
  remaining: no central log/verbose (deferred).

ACCEPTED RISK (explicit, documented, no lie):
- Native/permissive default runs with user privileges (usability choice;
  sensitive paths still escalate to Ask; Sandbox.md states no isolation).
- WSL lexical `/mnt` patterns bypassable via shell indirection
  (documented mitigation, both shell + shell_job; no FS cage possible).
- Mid-stream fatal without resume (replay would duplicate side effects).
- Same-session concurrent last-wins (atomic files stay parseable; no
  cross-process lock).

DEFERRED (honest, tracked):
- Slow-TTFB >120s Ollama cold load (header bound kept; manual resume).
- Memory prompt fencing (bounds+scrub done; `<memory>` fence future).
- In-process session mutex + cross-process GC/rotation for sessions/traces.
- TUI compaction-marker rendering + picker search/delete.
- Windows job objects, stdin parked goroutine, discovery detached fetch.
- Global memory scope, per-agent negative limit validation, SBOM/signing.

## 7. New findings discovered (by hardening, all addressed or logged)

- Shell Result ~2x cap (Content duplicates capped streams): bounded at
  ~4MiB + runtime 6000c guard — test corrected to assert <5MiB + marker
  instead of false OOM. No fix needed beyond documentation.
- Soak identical-arg 60x correctly trips loop-3: test uses varying args;
  loop guard proven working. No change (correct behavior).
- 61-turn soak exceeds DefaultLimits 60: test uses Limits{70,...};
  confirms iteration cap enforced. No change.
- scriptedProvider index panic on single-turn FinishLength test: test now
  supplies second turn (pre-fix path) — exposed old exec-then-continue
  semantics. Fixed by Block.
- Session system markers must be skipped in ProviderMessages (else
  multi-system breaks strict APIs): implemented + documented.
- Memory FormatForPrompt needed utf8 import for rune-safe cut: added.
- No new exec/network/persistence vulns introduced (all changes are
  caps/escapes/blocks/honesty/docs; verified by full suite + race).

## 8. Security assessment

Boundaries now explicit and tested: deep scrub at every persistence/
transport surface (nested maps/slices), escaped single-block fence,
sensitive-path Ask even under Always, session-scoped non-persisted Always,
interactive + WSL lexical refusals on both shell paths, O_NOFOLLOW +
re-validate (Unix), atomic 0600/0700 + validID + ListCorrupt isolation,
header API keys + hashed discovery + redacted errors, honest Describe
(always false shell confinement) + explicit Sandbox security model.
Residual: permissive defaults (documented choice), lexical bypassability
(documented), per-name Always granularity, unfenced memory text, TOCTOU
Windows gap — all logged as accepted/deferred, none presented as a
boundary. No prompt-injection proof (no red-team harness), so injection
resistance is mitigation-depth, not a guarantee.

## 9. Reliability assessment

Partial output can no longer succeed: FinishLength blocks with or without
calls (no truncated-arg exec), missing-Done errors, mid-stream Err →
Error (never Done), malformed → error, sustained 429 gives up at 4 reqs,
Retry-After clamped, cancel stops retries promptly, 500→recovery without
duplication (idempotent IDs kept), shell timeout/ceiling/cancel enforced,
write/memory/task/session/context all bounded with observable policies.
Crash semantics preserved: atomic tmp+Sync+Rename, corrupt surfaced never
accepted, tmp debris never parsed, interrupted turns repaired never
re-executed. Remaining: mid-stream/slow-TTFB require manual resume;
same-session divergence last-wins (parseable); no auto-restart/SIGHUP.

## 10. Long-horizon assessment

60-iteration soak terminates with bounded provider view (≤108 msgs);
100-iteration session soak + 5-round recompaction stay ≤1000 msgs with
marker + Compacted count; task ≤64/64/32 with Summary 8+300r; memory
≤200 entries + 8KiB note; write ≤5MiB; shell ~2x cap + 6000c runtime
guard; context CJK-safe. File-count (sessions/traces), TUI transcript
mem, and programmatic unlimited limits remain unbounded — documented with
MANUAL_SOAK ops (tmux, strict, ask, billing watch, manual resume, disk
watch, Windows child check). No 5-day CI run (by design); accelerated
soak models the same mechanisms. Expected: hours stable supervised;
multi-day unattended still needs rotation/GC/restart work.

## 11. Concurrency assessment

Race clean on runtime/session/hardening/redact/task/memory;
`-race -count=10 -shuffle=on` green on scheduler/fence/compaction paths.
askMu serializes the single TUI modal (proven by existing sensitive
concurrency test); emitMu preserves order; runMu serializes loops;
idempotent IDs prevent duplicate exec on provider echo. Divergent
same-session saves stay parseable (proven); last-wins loss remains
(no lock). Session mutation itself has no mutex — concurrent in-process
AddMessage races (callers must serialize; TUI single-thread does).
Stdin park + discovery detach leaks remain (deferred).

## 12. Cross-platform assessment

WSL relay stays availability-only (documented); shell_job WSL parity
added; Unix pgroup + SIGKILL vs Windows taskkill + fallback + WaitDelay
+ bounded drain kept; explicit pipes avoid StdoutPipe deadlock; CRLF and
path-separator seams kept. Native Unix still requires Bash (minimal
images fail with clear error); git-missing silently cwd-anchors memory
(pre-existing). Windows grandchild survival without job objects remains
(best-effort + MANUAL_SOAK check). No Windows-only CI validation added
beyond existing WSL integration + rename-retry; job-object work deferred.

## 13. Resource-bounds assessment

Every hot resource now has a documented lifecycle: iterations 60/70
(config defaults; programmatic zero = explicit unlimited), tool calls
300, consecutive failures 5, provider window 100 msgs + token reserve,
tool result 6000c, shell 2MiB (~4MiB in-mem with Content dup) + 30s/300s,
read 5MiB / write 5MiB refuse, list 500 / search 100 / find 50 /
git 256KiB / job 1MiB+300s / secret 50, task 64/64/32 + Summary 8+300r,
memory 200 + 8KiB note + reject-over-cap, session 1000 + marker +
Compacted, SSE 1MiB / error 8KiB / non-stream 64MiB, discovery 10m
single-flight, scheduler 4-concurrency, trace 4MiB/run. Unbounded:
session/trace file counts, TUI transcript mem, programmatic zero limits —
logged with ops mitigations.

## 14. Test coverage and known gaps

Added: 17-file P1.15 lab + 3 package regressions + 1 honesty test +
1 semantic-change update. Full `go test ./...` green (hardening ~9s,
providers ~24s, shell ~31s, sandbox ~13s, runtime ~10s, tui ~9s).
Known gaps (no live-model E2E by design; no 5-day CI): real-model
60-turn pressure, sustained-429 billing over days, Windows grandchild
accumulation rate, concurrent-session loss rate, fence/memory red-team
success rate, programmatic-zero OOM threshold, CJK overshoot magnitude
(now overestimates safely), TUI marker rendering, session/trace GC,
job objects, stdin/discovery leaks, global memory scope, per-agent
negative validation, SBOM/signing. Most important next tests: live-model
soak + chaos + red-team + `-count=100` scheduler stress.

## 15. Exact commands used for validation

- `git rev-parse HEAD; git log --oneline -8; git status --short`
- `go version; go env GOOS GOARCH`
- `go vet ./...` → clean
- `go test ./...` → all ok
- `go test -race ./internal/runtime/ ./internal/session/ ./internal/hardening/ ./internal/redact/ ./internal/task/ ./internal/memory/ -count=1` → ok
- `go test -race -count=10 -shuffle=on ./internal/runtime/ -run "TestHardening|TestFinishLength|TestScheduler"` → ok
- `go test -race -count=10 -shuffle=on ./internal/session/ -run "TestFence|TestScrub|TestCompact|TestGrowth"` → ok
- `go build ./...` → clean; `git status --short` → clean

## 16. Final git commits

- `77f8b38` chore: RC5 baseline checkpoint (commit 100 + working tree)
- `985b87d` test(P1.15): hardening laboratory with reproductions
- `f334515` fix(P1.16): deep scrub + fence escape
- `53c7480` fix(P1.17/P1.18): truncated/incomplete never succeed
- `d47a20b` fix(P1.19): bounded task/memory/write/session/context
- `787d0f7` fix(P1.16/P1.20/P1.21): boundaries + contracts/docs (tip)

## 17. Reproducibility status

Baseline (`77f8b38`) + lab (`985b87d`) + fixes reproduce deterministically
on `go 1.26.4 windows/amd64` via commands in §15. No new dependencies;
`go.sum` unchanged after baseline. Tree clean at tip. Version remains
`dev` without ldflags (pre-existing release stamping gap, documented).
Failing-first → passing-after proven per fix (lab failed on baseline for
C03/H01/H03/H04/H05/CJK/marker/missing-Done; all pass at tip except
documented accepted-risk log lines).

## 18. RC6 release recommendation

**CONDITIONAL GO** — ship RC6 as a hardening milestone with gated usage,
not as unattended/adversarial production.

Conditions: run in `workspace.mode: strict`, keep `shell`/`write_file`
at `ask`, run under `tmux`/`nohup`, monitor billing/quotas, expect manual
`ff --resume` after provider blips/crash/reboot, watch
`.forcefield/sessions` + `.forcefield/traces` disk, verify no leaked
children on Windows, and read the Sandbox security model (defaults are
not isolation; WSL patterns are mitigation; Always is per-tool session
scope; output/memory are untrusted data).

**NO-GO** for: unattended multi-day autonomy and adversarial production
with untrusted repos/models (isolation, per-command Always, memory
fencing, mid-stream resume, concurrent locking, slow-TTFB, GC/restart
remain accepted/deferred per §6). The system now fails safely, recovers
correctly, remains bounded on every hot path, and — critically — no
longer lies about what it enforces. That is what this milestone set out
to prove.

---

## Post-RC6 changes (added 2026-09-08, reconciliation pass)

Two RC6 statements are superseded by later work; the milestone conclusions stand otherwise:

- RC6 §6 listed FF-SEC-003 per-command scoping as deferred. It has since landed (working tree, uncommitted at reconciliation time): Always allow for shell/shell_job is now scoped by tool + normalized command text, AlwaysDeny stays broad per-tool, sensitive escalation unchanged. Status: FIXED WITH LIMITATIONS (TrimSpace-only normalization; cwd/env/timeout not in key; non-command tools stay per-tool; no expiry). Proof: internal/runtime/scheduler_always_scope_test.go (5 new tests) + full suite/vet/build/gofmt + race on runtime/session. See audit/RECONCILIATION.md §1.
- RC6 §6 listed the Strict FilesystemConfined overclaim as fixed, and §8/§12 as honest. Re-verified current: internal/sandbox/native.go:72-99 reports FilesystemConfined:false with an explicit shell-text note (+ honesty test). No other RC6 accepted/deferred item was found mislabeled.

Current score and verdict live in audit/RECONCILIATION.md (§§4-5), not here.
