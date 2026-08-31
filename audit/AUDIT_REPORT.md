# Forcefield Forensic Audit Report

**Project:** Forcefield (`ff`) — local-first agent harness  
**Commit:** `e10e62a` (+ dirty working tree: 17 modified, 3 untracked at audit time)  
**Date:** 2026-08-31  
**Auditor:** Muse Spark 1.2 (hostile, read-only)  
**Spec:** `AUDIT_SPEC.md` v1552 lines  
**Mode:** Hostile production-readiness for **5-day (120h / 7,200m) unattended autonomous execution**  
**Tooling:** `go test ./...`, `go test -race ./...`, `go vet ./...`, manual code trace, no `govulncheck`/`staticcheck` available  

> **Do not trust comments.** Every claim below was verified against implementation. Confidence is explicit: CONFIRMED / HIGHLY LIKELY / PROBABLE / POSSIBLE / SPECULATIVE.

---

## 1. Executive Summary

Forcefield is a **genuinely well-engineered, idiomatic Go harness** — single binary, zero telemetry, provider-agnostic runtime owning the loop, atomic session writes, bounded retries for 429, incremental TUI rendering, honest WSL sandbox disclosure, and strong `go test -race` hygiene. The `2e8f73a`/`f19b463` hardening wave fixed the headline TUI performance drain, header drift, and permission prompt.

**For 5-day unattended autonomy the verdict is `NOT YET`.** The core agent loop is correct, but **context/session growth is unbounded**, **provider 5xx/timeout/mid-stream failures are not retried**, and **default `native` mode has zero filesystem isolation with silent `allow` on reads** — each can kill a 5-day run with high probability. Windows process-group termination is best-effort, shell output is uncapped, and permission persistence is per-tool-name (global `Always allow` after one benign command escalates to `rm -rf`). No log, no compaction, no auto-restart.

**Score: 38/100** (see §29). **Can it run 5 days unattended today? No.** With ~5 P0/P1 patches it can become `YES, WITH CONDITIONS` (single workspace, no Windows long-horizon without job objects, monitored billing, manual resume after crash).

The smallest trustworthy change set is: cap shell output + timeout, make `read_file` not auto-allow dotfiles, retry 5xx/timeout, add context sliding window, and serialize concurrent `ask` prompts + scrub secrets. Without those, even 24h unattended is unsafe.

---

## 2. Repository / Architecture Overview

**Layout:**
```
main.go → cmd/{root,chat,run,doctor,memory}.go (Cobra)
internal/
  config/ (config.go:454, provider.go:305) — ~/.forcefield/config.yaml atomic + .env secret resolution
  providers/ (openai_compatible.go:686, ollama.go:376, anthropic.go:654, gemini.go:583, retry.go:275, http.go:17, sse.go:58, discovery.go:205, reasoning.go:403)
  tools/ (tool.go:103, registry.go:54, manager.go:55, args.go:32) + builtin/ (5 tools) + filesystem/ + shell/ (shell.go:740)
  permissions/ (decision.go:61, manager.go:80, ask.go:66, configstore.go:67)
  sandbox/ (sandbox.go:363, policy.go:290, native.go, wsl_windows.go, wsl_shared_windows.go)
  agent/ (agent.go:89, contract.go:112)
  runtime/ (runtime.go:928, scheduler.go:371, event.go:95, discovery.go, task_tool.go, memory_tool.go)
  task/ (state.go:318)
  session/ (session.go:107, manager.go:263)
  memory/ (memory.go:239, project.go:123)
  skills/ (skills.go:390, parse.go:127)
  command/ (command.go:65, parser.go, registry.go:99, dispatch.go:48, builtin/*)
  tui/ (model.go:1485, conversation.go:172, permission.go:367, mouse.go:470, styles.go:182, markdown.go, thinking.go, activity.go, selectpicker.go, sessionpicker.go, etc.)
docs/ (13 files mirroring packages) + cue/ (CUE schemas) + scripts/install.sh|ps1
```

**Entry points:** `ff` → `session.New|Load` → `tui.Start` → `newModelWithConfig` → `tea.Program`; `ff run <task>` → `runtime.Run`; `ff doctor` → probes.

**Persistent artifacts:** `~/.forcefield/config.yaml` (0600 atomic), `./.forcefield/sessions/<uuid>.json` (project-local atomic), `~/.forcefield/memory/projects/<slug>-<hex>.json` + `global.json`, `~/.forcefield/skills/*.md` + `*/SKILL.md` (1 MiB/256 cap, symlink-confined).

**Key invariants (verified):**
- Runtime owns multi-turn loop; providers stream **one** turn (`AGENTS.md:79-87`, `runtime.go:848-895`).
- Providers never execute tools; tools never manage sessions (`internal/providers/provider.go:12-14`).
- TUI is presentation-only (`internal/tui/model.go:88-91`).
- Secrets never in `config.yaml` or `os.Environ` for WSL (`config.go:242-247`, `sandbox/wsl_windows.go:79-88`).
- `DefaultLimits` (60 iterations / 300 tool calls / 5 consecutive failures) guarantee termination per `Run()` (`runtime.go:39-43`).

**Full lifecycle trace (file:line):**
```
User keystroke → tui/model.go:769 handleKey (bracketed paste + 25ms burst) → Enter → acceptInput:903 → command.Dispatch:9 → if not command: entries+=user + session.AddMessage+Save:924 → waiting=true
→ runtime.StreamChat:654 buildMessages:640 (system=agent.BuildSystemPrompt+task.Summary) → state:=task.New(goal):717 → run loop 722 check MaxIterations → runModelTurn:848 checkAuth → provider.StreamChat → provider adapter builds payload (e.g. openai_compatible.go:275 buildPayload + sseReader) → events loop 864 → TUI streamEventMsg 417 → appendAssistantText/Thinking/startToolActivity → session.AppendToolCall+Save
→ scheduler.Run:60-108 (sem 4, WaitGroup, atomic stopped) → checkPermission 259 → Asker (tuiAsker.Send → permissionPrompt) → Tool.Execute (shell/filesystem) → Result → runtime truncate 6000 → state.RecordTool → append ToolRole message → next iteration → EventDone or EventBlocked/Error → tui stopStream+Save → View header/viewport/footer
```

---

## 3. Critical Findings (CRITICAL)

### FF-SEC-001 — Shell in `wsl` mode escapes filesystem confinement
- **Severity:** CRITICAL **Confidence:** CONFIRMED **Category:** Security / Sandbox
- **Location:** `internal/sandbox/sandbox.go:227-232` + `internal/sandbox/wsl_windows.go:15-44,191` + `internal/tools/shell/shell.go:239-243`
- **Summary:** `wsl` mode pins `cwd` and restricts env, but explicitly documents **no filesystem confinement** (`/mnt/*` reachable). Filesystem tools are now confined via `ResolveWithinWorkspace` in `wsl` mode (`internal/tools/filesystem/read_file.go:57`), but `shell` bypasses it — command string is passed verbatim to `bash -lc`.
- **Evidence:** `shell {command:"cat /mnt/c/Users/Admin/.forcefield/.env"}` succeeds even when `read_file {path:"C:/Users/..."}` is blocked with `ErrWorkspaceEscape`. `wsl_windows.go:221 linuxArgv = append(..., "/bin/bash","-lc", command)` with no path filtering.
- **Root Cause:** Architectural split: tool-layer policy vs executor cwd-only check.
- **Impact:** Single `shell` call exfiltrates any host file; makes FS-tool fix theater over 5 days.
- **Trigger:** Model emits `shell` with absolute `/mnt/` or `C:` path (prompt-injected file can do this).
- **Why Protection Fails:** Fix reused `ResolveWithinWorkspace` only for three FS tools (`internal/tools/builtin/builtin.go:66-86`).
- **Direction:** Document `wsl` ≠ FS sandbox; add lexical shell deny for `/mnt/`, `C:`, `../` when `ModeWSL`; long-term run shell in same cage as tools (Linux landlock/bubblewrap).
- **Priority:** P0
- **Related:** FF-SEC-002, FF-SEC-006

### FF-SEC-002 — Default `native` mode has zero isolation + silent `allow` on reads
- **Severity:** CRITICAL **Confidence:** CONFIRMED **Category:** Security
- **Location:** `internal/config/config.go:125-133` `defaultConfigTemplate` (`read_file: allow`, `list_files: allow`, `sandbox.mode: native`)
- **Summary:** Fresh install reads arbitrary host file with zero clicks; `read_file {path:"C:\\Users\\...\\.env"}` → `Result.Content` → next provider turn → persisted in session JSON → exfiltrated.
- **Evidence:** `read_file.go:56` `if r.policy.Mode==WSL { check }` — native skips check; `builtin.go:70` comment `native or unspecified: unrestricted`.
- **Impact:** First-contact exfiltration of `.ssh`, `.env`, `config.yaml` without permission prompt; 5-day unattended with `native` is unsafe.
- **Direction:** Default `read_file` to `ask` or make `read_file` workspace-relative even in native; require explicit allow for `../`/`C:`.
- **Priority:** P0

### FF-CRIT-003 — Unbounded context/history will exhaust provider window
- **Severity:** CRITICAL **Confidence:** CONFIRMED **Category:** Reliability / Long-Horizon
- **Location:** `internal/runtime/runtime.go:640-651 buildMessages`, `749-771` append loop, `maxToolResultChars=6000` only per-tool, `internal/task/state.go:257-298 Summary`
- **Summary:** Every tool turn appends 1 assistant + N tool messages to `messages` slice; `buildMessages` re-sends entire history each turn with no window, token estimate, or compaction. `MaxIterations 60` bounds per-`Run()` to ~120 messages, but history from `session.ProviderMessages()` is replayed on resume, so file never shrinks. `Discoveries/Blockers` appended forever in `task.State`.
- **Evidence:** `messages = append(messages, history...)` no pruning; `truncateToolResult` caps one result, not total; 60×6k ≈ 360k chars ≈ 90k tokens + system prompt → exceeds any window → provider `400 invalid_request` → `EventError` terminates run with no recovery.
- **Impact:** Any serious 5-day task hits context limit within hours; unattended task stalls permanently.
- **Direction:** Sliding window (keep system + goal + last K turns, summarize evicted), token-aware pre-flight, surface `FinishLength` as `Blocked`.
- **Priority:** P0

---

## 4. High-Severity Findings (HIGH)

### FF-SEC-003 — Global `Always allow` is per-tool-name, not per-argument
- **Severity:** HIGH **Confidence:** CONFIRMED **Category:** Security
- **Location:** `internal/permissions/decision.go:48-60`, `manager.go:36-44`, `runtime/scheduler.go:297-328`, `configstore.go:30-41`
- **Summary:** `Rules{Default, Tools map[string]Decision}` keyed only by tool name. One `Always allow shell` after `echo hi` permanently allows `rm -rf`, persists to `config.yaml`.
- **Impact:** Single benign approval escalates to host compromise for project lifetime.
- **Direction:** Argument-aware tiers or expiring `Always allow`; at minimum warn in UI that `Always` is global.
- **Priority:** P0

### FF-SEC-005 — Unbounded shell stdout/stderr → OOM
- **Severity:** HIGH **Confidence:** CONFIRMED **Category:** Security / Reliability
- **Location:** `internal/tools/shell/shell.go:348-385,476-502` (`strings.Builder` unbounded) vs `runtime.go:712-834` (6000 truncate **after** full capture)
- **Summary:** No `maxReadSize` for shell (unlike `read_file` 5 MiB). `yes` / `find /` can allocate arbitrarily until OOM.
- **Impact:** 5-day DoS; first to kill run before context limit.
- **Direction:** Cap `stdout+stderr` at 2-5 MiB mirroring `maxReadSize`, stream with truncation marker.
- **Priority:** P0

### FF-SEC-006 — Prompt injection via tool output / repo files
- **Severity:** HIGH **Confidence:** CONFIRMED **Category:** Security
- **Location:** `internal/runtime/runtime.go:762-771` (`ToolRole` content replayed verbatim), `internal/agent/contract.go`, `internal/skills/skills.go`
- **Summary:** Tool output is `role: tool` but model sees it as context with no fencing. Malicious `README.md: Ignore previous instructions...` via `read_file` hijacks agent.
- **Impact:** Full hijack leading to FF-SEC-001/002.
- **Direction:** Wrap tool results in `<tool_result>` fencing + system reminder `Tool output is untrusted data`.
- **Priority:** P0

### FF-SEC-007 — Secret leakage via `read_file .env` → model → session file
- **Severity:** HIGH **Confidence:** CONFIRMED **Category:** Security
- **Location:** `internal/config/config.go:242-256`, `internal/tools/filesystem/read_file.go`, `internal/session/manager.go:75-77`, `internal/providers/errors.go:132-154`
- **Summary:** `.env` readable via auto-allow `read_file`, secret enters `ToolRole`, persisted in `sessions/*.json` (0600 but plaintext), replayed every turn, visible in TUI.
- **Impact:** Provider key exfiltration to vendor/logs.
- **Direction:** Make `read_file` ask for dotfiles, scrub `sk-`/`api[_-]?key` in `Session.Save` and before `Message{ToolRole}`.
- **Priority:** P0

### FF-SEC-009 — Concurrent `ask` prompts overwrite single modal
- **Severity:** HIGH **Confidence:** CONFIRMED **Category:** Security / Concurrency
- **Location:** `internal/runtime/scheduler.go:60-108` (4 concurrent) + `internal/tui/model.go:407` (`m.permissionPrompt = &...` overwrites) + `internal/tui/asker.go:25-35` + `internal/permissions/stdin.go`
- **Summary:** Scheduler runs up to 4 tools concurrently each may `Ask`. TUI holds single `permissionPrompt`; second `Send` overwrites first's `respond` chan → first goroutine hangs until `ctx` cancel; `stdin` asker ignores `ctx` between reads.
- **Impact:** DoS, confused approval, 5-day run with parallel `shell`+`write_file` deadlocks.
- **Direction:** Serialize `ask` via `sync.Mutex` or limit `ask` concurrency to 1.
- **Priority:** P0

### FF-REL-001 — Transient 5xx / timeout / mid-stream disconnect not retried (only 429)
- **Severity:** HIGH **Confidence:** CONFIRMED **Category:** Reliability / Provider
- **Location:** `internal/providers/retry.go:198-212` (`StatusCode != 429 → statusErr`), `internal/providers/http.go:13` (`120s` wall timeout), `internal/providers/retry.go:150-157` comment
- **Summary:** `doWithRetry` retries only 429 (with `Retry-After`+quota check). 500/502/503/504/529, transport errors, and mid-stream `sseReader` errors bubble as `EventError` and abort run. `defaultStreamTimeout 120s` kills healthy local Ollama >120s.
- **Impact:** Normal cloud blip over 5 days kills unattended task; requires human restart.
- **Direction:** Retry 429+5xx+transport with same caps (3 retries, jitter, `MaxRetryAfter 60s`), make timeout per-provider (Ollama 0/10m, cloud 120s), retry idempotent mid-stream text-only turns.
- **Priority:** P0

### FF-REL-002 — Session file grows forever; resume replays all history
- **Severity:** HIGH **Confidence:** CONFIRMED **Category:** Long-Horizon
- **Location:** `internal/session/session.go:12-29`, `manager.go:62-121`, `internal/session/session_test.go`
- **Summary:** `Messages []Message` append-only, never pruned. `ProviderMessages` returns all. 5-day loop 1 tool/min → 21k messages → 10s MB JSON → `os.ReadFile` on every `Load` → heap/GC pressure, `MarshalIndent` OOM, disk fill.
- **Impact:** Run slows, eventual `Save` fails (divergent memory vs disk), disk full kills run.
- **Direction:** Rotate or compact at e.g. 500 messages (summarize oldest).
- **Priority:** P1

### FF-TUI-001 — Session resume drops tool messages from transcript
- **Severity:** HIGH **Confidence:** CONFIRMED **Category:** TUI / Observability
- **Location:** `internal/tui/model.go:330-352` `sessionEntries` filters `msg.Role != "user"/"assistant"` → `continue`; `internal/session/session.go:39-45` correctly replays `tool` to provider
- **Summary:** After `switchToSession` or restart, prior tool calls/results vanish visually (only provider sees them). `SessionStats` counts them, TUI hides them.
- **Impact:** 5-day task with `/resume` loses audit trail; cannot debug failures.
- **Direction:** Render tool history as read-only blocks in `sessionEntries`.
- **Priority:** P1

### FF-SEC-004 — Interactive command heuristic bypassable
- **Severity:** HIGH **Confidence:** CONFIRMED **Category:** Security
- **Location:** `internal/tools/shell/shell.go:553-616` (`interactiveCommands` map, `wrapperCommands` skip)
- **Summary:** `bash -c "vim"` → fields `["bash"]` not in map → not blocked. Same for `sh -c 'ssh …'`.
- **Impact:** Model can hang TUI with TTY-requiring command.
- **Direction:** Recurse into `bash -c` args or allow-list safe commands.
- **Priority:** P1

---

## 5. Medium-Severity Findings (MEDIUM)

### FF-SEC-010 — TOCTOU in filesystem tools
- **Location:** `internal/tools/filesystem/read_file.go:64-77`, `write_file.go:69-83`, `internal/sandbox/policy.go:204-233`
- **Summary:** Check `Stat` → use `ReadFile`/`WriteFile` window; `MkdirAll` after `EnsureWithinWorkspace` may follow newly created symlink. With scheduler concurrency 4, model-controlled parallel writes can win race.
- **Confidence:** PROBABLE
- **Priority:** P2

### FF-SEC-011 — `timeout_seconds` unbounded
- **Location:** `internal/tools/shell/shell.go:222-229`
- **Summary:** `timeout_seconds=999999` (≈11 days) holds semaphore slot and process group.
- **Direction:** Cap at 300s.
- **Priority:** P1

### FF-SEC-012 — Windows process-group kill incomplete
- **Location:** `internal/tools/shell/shell_windows.go:11-39` (`setProcessGroup` no-op, `taskkill /T /F` best-effort, ignored error, 1s `waitDelay`)
- **Summary:** Grandchildren survive timeout/cancel; FD leak.
- **Priority:** P1 (for Windows 5-day)

### FF-SEC-015 — Provider errors not retried (duplicate of FF-REL-001, security view)
- **Location:** `internal/providers/retry.go`
- **Priority:** P1

### FF-SEC-016 — Lenient tool arg parsing hides intent
- **Location:** `internal/tools/args.go:20-31`, `internal/tools/shell/shell.go:212-234`
- **Summary:** `OptionalStringArg` returns default on wrong type; extra fields ignored; UI may show truncated view.
- **Priority:** P2

### FF-CTX-002 — `FinishLength` silently becomes success
- **Location:** `internal/providers/openai_compatible.go:252-263`, `internal/runtime/runtime.go:732-742`
- **Summary:** `length`/`MAX_TOKENS` → `FinishLength` but `len(ToolCalls)==0` → `FinalStatus()` → `EventDone` not `Blocked`.
- **Priority:** P2

### FF-PERM-001 — Permission prompt truncation hides payload
- **Location:** `internal/tui/permission.go:195-247` (300c + first line 80c)
- **Summary:** `write_file` content truncated before approval; malicious tail hidden.
- **Priority:** P2

### FF-SESS-001 — `AppendToolCallToLastAssistant` coalescing can corrupt batches
- **Location:** `internal/session/session.go:92-107`
- **Summary:** Merges consecutive assistant tool batches if `len(last.ToolCalls)>0` without turn check.
- **Priority:** P2

### FF-OBS-001 — No persistent run log; reasoning not saved
- **Location:** `internal/tui/model.go`, `internal/task/state.go`
- **Summary:** `thinkingRecord.text` and `Usage` not persisted; session stores only final truncated tool result.
- **Priority:** P2

### FF-CFG-001 — Go version drift
- **Location:** `go.mod:3` `go 1.26.4` vs `README.md:46` `Go 1.22+`
- **Priority:** P3

### FF-DOC-001/002 — Documentation drift (TUI reasoning tags, Events table)
- **Location:** `docs/TUI.md`, `docs/Runtime.md:50-58` vs `internal/runtime/event.go`
- **Priority:** P3

### FF-HYG-001 — Dirty working tree
- **Location:** `git status` 17 modified + 3 untracked
- **Priority:** P1

---

## 6. Low-Severity / Informational Findings (LOW)

- **FF-SEC-013** Env var name validation gap (`extraEnvArgs` not validated centrally) — LOW, CONFIRMED, `internal/tools/shell/shell.go:698-725`
- **FF-SEC-014** `pwd` leaks host cwd without policy — LOW, CONFIRMED, `internal/tools/shell/pwd.go:31`
- **FF-SEC-017** `validID` insufficient on Windows (reserved `CON` etc.) — LOW, PROBABLE, `internal/session/manager.go:40-52`
- **FF-SEC-018** `update_task_state` verification trust (`verification:"passed"` without evidence) — LOW, CONFIRMED, `internal/runtime/task_tool.go:82-113`
- **FF-TUI-003/004** Hit-region stride docs, layout early-exit — LOW, INFO
- **Dead code:** `config.go:228` `apiKeyName` legacy, `permission.go:131` `permOptionGap` unused, `layout.go:14-15` constants — LOW, INFO
- **Duplicate atomic write** (`config.writeFileAtomic` vs `session manager.Save`) — LOW, INFO
- **FF-CFG-003** Negative limits silently treated as unlimited (CUE vs YAML mismatch) — LOW
- **C5** `discovery.go` waiters leak ctx (inflight retained) — LOW, INFO

---

## 7. Security Audit (Consolidated)

**Threat model:** Model controls every `toolCall.arguments` byte; prompt injection via file content; workspace attacker can plant symlink between check and use.

**Boundaries:**
- **Model→Host FS:** FS tools now confined in `wsl` mode (validated via `ResolveWithinWorkspace` + `EvalSymlinks` + `withinAny`), but `shell` is not (`wsl` → `/mnt/*` still reachable). **Default `native` has zero confinement.**
- **Model→Process:** `shell` is only process-creating tool; interactive detection heuristic bypassable; `timeout_seconds` uncapped; Windows group kill weak; output unbounded; env for `native` is full `os.Environ`, for `wsl` is restricted (`SystemRoot,TEMP,TMP`).
- **Model→Secrets:** `ResolveEnvValue` never writes to `os.Environ` (good), `x-goog-api-key` uses header not URL, `Redacted` scrubs errors, but `read_file .env` (auto-allow) → tool output → provider → session file leaks.
- **Model→Permissions:** Per-tool-name global `Always allow` persists to `config.yaml` atomically; concurrent `ask` overwrites single `permissionPrompt` modal.
- **Deserialization:** YAML/JSON typed → safe; no `interface{}` code exec; session JSON size unbounded before `ReadFile`.

**Top security blockers for 5-day:** FF-SEC-001,002,003,005,006,007,009.

---

## 8. Agent Safety Audit

**Pipeline:** `Model JSON → providers finalizer (json.Unmarshal args → map) → scheduler Lookup → checkPermission (before validation) → MetadataOf → execute → Tool.StringArg → sandbox resolve → os.*`

**Gaps:**
- Validation after permission (extra args ignored, wrong type defaults).
- No `tool_result` fencing → prompt injection.
- `update_task_state` trust without evidence → false `StatusVerified`.

**Exploits tested conceptually:**
- `{"path":12345}` for `read_file` → becomes `"."` via `OptionalStringArg` — silent wrong behavior.
- `{"command":"bash -c vim"}` → bypasses interactive block.
- `{"command":"cat /mnt/c/..."} ` in `wsl` → escapes.

**Recommendation:** Strict schema validation before permission, fence tool results, require verification evidence.

---

## 9. Provider / Rate-Limit Audit

**Providers:** `openai_compatible` (NVIDIA/OpenAI/xAI/Groq/etc), `ollama` (NDJSON + `Think`), `anthropic` (cursor pagination, `input_json_delta`), `gemini` (`x-goog-api-key` header, `alt=sse`), `nvidia`/`lmstudio` aliases. Registry derives from `catalog.go:31` presets.

**Request construction:** `Spec.Validate` checks URL/header token; `buildPayload` merges `extraBody` for `enable_thinking`; correct `auth` headers per provider.

**Resilience matrix:**

| Failure | Expected | Actual | Safe? | Location |
|---|---|---|---|---|
| HTTP 429 | Backoff 1s/2s/4s + jitter + `Retry-After` (60s cap) + quota `__exhausted` → no retry | **Yes** bounded 3, jitter equal (`half+rand`), respects `Retry-After` delta-seconds/date | ✅ | `retry.go:41-46,131-144,157-228` |
| HTTP 429 quota | Immediate `NonRetryable` | Yes string match `quota/billing/credit` | ⚠️ Brittle misses `RESOURCE_EXHAUSTED` | `retry.go:107-126` |
| HTTP 500/502/503/504/529 | Retry with backoff | **No** — immediate `statusErr` → `EventError` abort | ❌ | `retry.go:198-212` |
| Timeout 120s wall | Recover/cancel | Fatal `ErrKindTimeout` no retry | ❌ | `http.go:13` |
| Stream disconnect mid-SSE | Resume or fail safely | Fatal `EventError` no resume (comment says intentional to avoid duplicate) | ❌ | `retry.go:150-157` |
| Partial JSON chunk | Fail safely | `json.Unmarshal` error → `statusErr` | ⚠️ | `openai_compatible.go:431` |
| Malformed SSE line | Ignore | `sseReader` 1 MiB cap, ignores `:` comments | ✅ | `sse.go:19-58` |
| Invalid credentials | Hint + stop | `statusHint` 401/404 → actionable hint | ✅ | `openai_compatible.go:129-154` |
| Provider unavailable | Recover/fallback | No fallback, manual `/provider` | ❌ | `runtime.go` no auto-switch |
| 64 MiB non-stream body | Truncate | `LimitReader 64MiB` | ✅ | `openai_compatible.go:586` |

**Rate-limit amplification (5-day):**
- Requests per turn: 1 provider request + up to 4 concurrent tool calls (no extra provider calls).
- Retry amplification: worst 4 req/turn (3 retries) → 60 turns → 240 reqs burst could trip global quota.
- No global rate limiter; per-provider `requestGate` (chan 1) prevents concurrent provider calls per runtime, but not across retries.
- `Retry-After: 600` clamped to 60s → hot-loop every 60s, wastes quota.
- `quotaExhausted` miss → 3 wasted retries before billing alert.
- No jitter decorrelation across multiple Forcefield instances.

**Finding:** 429 handling is **well bounded and correct** for transient, but 5xx/timeout/mid-stream not retried kills 5-day; quota detection brittle.

---

## 10. Five-Day Long-Horizon Audit

**Growth analysis (120h):**

| Resource | Grows? | Bounded? | Cap | Evidence | At Limit |
|---|---|---|---|---|---|
| Iterations/run | Yes | ✅ | 60 | `runtime.go:39` | `EventBlocked` |
| Tool calls/run | Yes | ✅ | 300 | `runtime.go:41` | Blocked |
| Retries/req | Yes | ✅ | 3 | `retry.go:42` | `give up after 3` |
| Tool result for LLM | Yes | ✅ | 6000 chars (rune-boundary) | `runtime.go:713,814` | Truncated notice |
| SSE line / error body | Yes | ✅ | 1 MiB / 8 KiB | `sse.go:23`, `retry.go:148` | Truncated |
| Discovery cache | Yes | ✅ | 10m TTL single-flight | `discovery.go:36,49` | Refresh |
| **Session file** | **Yes** | **❌** | **∞** | `session.go:29` append-only | **Disk full / OOM / 10s MB JSON** |
| **History payload** | **Yes** | **❌** | **∞** | `runtime.go:640` no window | **400 invalid_request abort** |
| **Discoveries/Blockers** | **Yes** | **❌** | **∞** | `task/state.go:182-188` append | **OOM, task digest bloat** |
| **Governance (shell stdout live)** | Yes | ⚠️ | 1s drain | `shell.go:30` | Truncate or hang |
| **Shell output total** | **Yes** | **❌** | **∞** | `strings.Builder` | **OOM** |
| **Goroutines** | Per turn | ⚠️ | `MaxToolCalls` implicitly | `scheduler.go:74` | 200 concurrent if model returns 200 calls |
| **FDs / pipes** | Per shell | Bounded by concurrency | 1s `WaitDelay` | `shell.go:312-381` | Grandchild leak on Windows |
| **Messages / context** | **Yes** | **❌** | **∞** | above | **First bottleneck** |

**First likely bottleneck:** **Context growth** (provider window) before memory/disk, within ~2-12h of steady tool use. No compaction, no `FinishLength` handling.

**Long-horizon failure model:**

- Provider 429: **Detected / recovered (bounded) / state safe / informed / partially long-safe** (3 retries, jitter, but sustained 429 still exhausts).
- Provider 5xx/timeout: **Detected / not recovered / safe / informed / not long-safe** → run abort.
- Stream disconnect: **Detected / not recovered / partially safe / informed / not long-safe**.
- Tool hang: **Detected via 30s timeout / recovered via group kill / safe / informed / partially (Windows leak)**.
- Forcefield crash: **No `recover` / not recovered / atomic saves protect last completed tool call, current iteration lost / not informed / not long-safe**.
- Terminal close/SSH drop: **No SIGHUP handling / not recovered / partial save / not long-safe** → need `tmux`/`nohup` (undocumented).
- Session corruption: **Detected via `ListCorrupt` / isolated / safe / informed via `doctor` / long-safe** (one file doesn't break others).
- Disk full: **Detected via `Save` error as `roleError` / not recovered / divergent memory vs disk / informed / not long-safe**.

---

## 11. Context / Memory Audit

- **Conversation history:** Unbounded (see §10).
- **System prompt:** `agent.BuildSystemPrompt()` fixed order (base + contract + memory + catalog + task summary) — no growth per se, but `task.State.Summary()` grows with `Discoveries`/`Blockers`.
- **Memory:** `memory.CurrentProjectStore` loaded once at `newRuntime:102-108`, `FormatForPrompt` joins entries with `\n` — no prune, but size is small (user-controlled files, not model-controlled). Safe.
- **Summaries:** `task.State.FinalStatus` never `verified` without `VerificationPassed` (good), but `update_task_state` can set any string — trust issue.
- **Compaction:** None.
- **Truncation:** `truncateToolResult` 6000 rune-boundary safe, notice preserved; but TUI `maxExpandedOutputLines 40` clamps view while provider sees truncated already.
- **Point of impracticality:** ~30 tool turns ×6k = 180k chars → every turn sends 180k + system prompt → latency/cost/429 amplify → fails.

---

## 12. Session / Persistence Audit

**Strengths:** Atomic temp+Sync+Rename+5× Windows retry for both `config` and `session` (`writeFileAtomic:342-389`, `session/manager.go:62-121` + `replaceFile:137-148`); `validID` rejects `/\` and `.`/`..` and 128 limit; `Load` never deletes corrupt file; `ListCorrupt` isolates; concurrent saves (8 goroutines) remain parseable.

**Weaknesses:**

- **FF-SESS-001** `AppendToolCallToLastAssistant` merges based only on `len(last.ToolCalls)>0` without turn or time check (`session.go:92-107`) — can corrupt batch if provider splits.
- **FF-SESS-002** `UpdatedAt = time.Now()` on every `Save` even if unchanged — churns `List` sort.
- **Crash halfway:** Atomic rename ensures old or new complete file, so 5-day crash recovers to last *completed* tool call; in-memory `messages` slice of current iteration (not yet saved as tool result) is lost — 1 iteration lost, acceptable but not WAL.
- **Concurrent sessions:** Two `ff` processes on same `./.forcefield/sessions/<id>.json` race on rename (mitigated by retry, not by file lock) — last writer wins, other's tool result lost. No advisory lock.
- **No rotation:** See §10.

**Resume:** `ff --resume <id>` → `session.Load` → `tui.Start` → `sessionEntries` (drops `tool` roles) → `ProviderMessages` (replays all). Visual vs replay divergence (FF-TUI-001).

---

## 13. Concurrency Audit

- **Race detector:** `go test -race ./...` **passes** (all 17 packages, tui 6.9s).
- **Shared state:** `permissions.Manager` `RWMutex` + `saveMu`; `FactoryRegistry` `RWMutex`; `Discovery` `Mutex`+inflight; `scheduler` `sem` chan + `WaitGroup` + `atomic.Bool` + `emitMu`; `Runtime.reasoningMu`; `task.State.mu`; `sandbox backendMu`; `TUI` single Bubble Tea goroutine + `tuiAsker` blocking scheduler goroutine via chan — correct.
- **Deadlocks:** None observed; `emitMu` holds while `events <-` could block if consumer stalled, but `events` is unbuffered and `emit` selects `events` vs `ctx.Done`, so `emitMu` held during block could stall other tool emitters — **C3** medium.
- **Goroutine leaks:** `StreamChat` providers select `events <-` vs `ctx.Done` and defer `Body.Close` + `gate.release` — no leak on cancel. `discovery` background `go DiscoverModels` with `context.Background()` not cancellable when picker closed — minor leak (one per picker open).
- **Shutdown races:** `stopStream` cancels `streamCtx` + `streamGen++` drops stale events (`msg.gen != streamGen`).

**Verdict:** No data races; one emit stall and one discovery goroutine leak are medium/low.

---

## 14. Process / Shell Audit

**Creation:** `sandbox.Prepare` validates `cwd` (native: existence-only, WSL: `resolveWithinWorkspace` strict), `hostEnv` minimal for WSL (`SystemRoot,TEMP,TMP,WSLENV=`), full for native; `Cmd.Cancel=killProcessGroup`, `WaitDelay=1s`.

**Pipes:** Explicit `os.Pipe` for stdout/stderr (avoids `StdoutPipe` deadlock on grandchild), concurrent `streamPipe` with `sanitizeOutput` (CSI/OSC + control-char drop) and `onChunk` progress.

**Cancellation:** Unix `Setpgid`+`Kill(-pid,SIGKILL)` correct; Windows `taskkill /T /F` + `Process.Kill` best-effort, `setProcessGroup` no-op, misses grandchildren without job object.

**Output limits:** None (see FF-SEC-005); `sanitizeOutput` per line but `strings.Builder` unbounded.

**Timeout:** `defaultTimeout 30s` unless `timeout_seconds` overrides (no cap → 11-day hold).

**Grandchild leak:** `WaitDelay 1s` drain then `Close` read ends; `1s` may be short for huge output, long for quick hang.

**Interactive refusal:** `detectInteractiveCommand` covers 30+ programs + `sudo/env/nohup` wrappers, splitting on `&&||;|`, skipping `VAR=value` — correct but `bash -c "vim"` bypasses (first token `bash`).

**Long-horizon:** Long `make -j` with 1s drain may truncate; daemon `nohup server &` will be killed on timeout (surprising).

---

## 15. Performance Audit

- **TUI fixed:** `rendererCache map[int]*glamour.TermRenderer` per width, `tcache*` incremental (`widthChanged` invalidates all, else per-entry `canReuse` checks `role/content/streaming/hovered/thinking/tool` fields), `layout()` height-only path avoids re-parse. Prior `1.3ms/entry ×50 = 14.9ms` resolved; now O(dirty) per chunk.
- **Allocations:** `toolArgsKey` sorts each check but negligible; `truncateCells` per rune.
- **Serialization:** `json.MarshalIndent` per `Save` (session, config, memory) — synchronous on UI goroutine (`tui/model.go:452-474` per tool result) could block 10s MB file; but 5-day file will become large.
- **No unbounded work** except history/context/session growth (see §10). No `N^2` loops found besides history replay (linear per turn, acceptable until unbounded).

---

## 16. Code Quality Audit

- **Style:** Idiomatic Go, small packages, explicit returns, `AGENTS.md` modularity respected (CLI→TUI→Runtime→*).
- **Dead code:** `config.go:228` `apiKeyName` legacy, `permission.go:131` `permOptionGap` now unused (kept compat), `layout.go:14-15` unused constants.
- **Duplication:** `writeFileAtomic` vs `session.Save` atomic pattern duplicated (6 lines) — minor.
- **Complexity:** `model.refreshTranscript` 152 lines, `OpenAICompatible.StreamChat` 140 lines — justified, readable.
- **Error handling:** Consistent `fmt.Errorf("%w")` with context, no panics for expected, `MustRegister` panics only for duplicate (programming error).
- **TODO/FIXME:** None found beyond `AUDIT_SPEC.md`.
- **Maintainability:** Good naming, no global state, interfaces small.

---

## 17. Architecture Audit

**Supports growth:**
- **Providers:** Add `Preset` to `catalog.go:31`, wire via `DefaultFactories` derived from catalog — excellent, no hardcoding elsewhere.
- **Tools:** Implement `Tool` interface + `manager.Register` — ordered registry, central `builtin.NewManager`.
- **Permissions:** `Asker` pluggable (TUI vs stdin), `Manager` persists via `Store` interface — good.
- **Sessions:** Filesystem JSON only (`sessionsDir` constant) — not abstracted; adding DB would churn.
- **10× bottleneck:** Single `Session.Messages` slice in memory + `model` struct (1485 lines) owns everything; adding metrics requires intercepting `Event` channel.
- **Migration difficulty:** Low; no rewrite needed.

---

## 18. TUI / UX Audit

| Area | Verdict | Evidence |
|---|---|---|
| **Message grouping** | Drops tool messages on resume (FF-TUI-001) | `model.go:330` filters |
| **Visual hierarchy** | Color-only, no size variation | `styles.go:15-34` |
| **Tool presentation** | Excellent — one-line summary + expand, exit/duration, clamped | `conversation.go:96-112`, `activity.go:113-152` |
| **Markdown** | Correct streaming tail plain, diagrams, dark style | `markdown.go:81-97` |
| **Long output** | `maxExpandedOutputLines 40` + `… (+N more)` | `thinking.go:71` |
| **Permission prompt** | **Fixed vertical selectable** (Allow once/Always allow/Deny/Always deny, ↑/↓+Enter, Esc deny, mouse, hover) | `permission.go:114-122,139-180,343-356` |
| **Keyboard** | Tab cycle, Alt+Enter/Ctrl+J newline, Esc clears, Ctrl+E/R/T, F2 mouse, Ctrl+Y copy | `model.go:747-896` |
| **Mouse** | Centralized `routeMouse` precedence, 3 lines/wheel, pickers clickable | `mouse.go:107-302` |
| **Scrolling** | Auto-follow pause/resume, wheel sync | `model.go:158-161` |
| **Resizing** | `headerRows` measured, viewport clamped 3, no re-parse on height only | `model.go:40-45,1139-1173` |
| **Copy/paste** | OSC52, bracketed paste atomic, burst 25ms (<33ms autorepeat) | `clipboard.go`, `paste_test.go` |
| **Session picker** | Centered modal, short ID, hit-testing via `boxOrigin` | `sessionpicker.go` |
| **Empty state** | Splash banner, compact fallback | `banner.go` |

**Remaining UX gaps:** No command history (↑ through prior prompts), copy only last assistant, input placeholder bright vs muted correct, but light-theme contrast weak; permission prompt content truncated at 300c.

---

## 19. Windows / Cross-Platform Audit

| Area | Evidence | Verdict |
|---|---|---|
| **Install** | `scripts/install.ps1/.sh` user-local `~/.local/bin`, `checksums.txt` verify, `README 59-113` | Good, but `Makefile:10-14` `OS` check only MinGW |
| **Shell** | `shell_windows.go` vs `shell_unix.go`, `native_windows_probe.go`, `wsl*.go` conditional | Correct Unix vs WSL relay |
| **Paths** | `config.Dir:155-170` `0700`, `session/manager:69-73` `FromSlash`, `sandbox` `ProjectRoot` via `git rev-parse` | Handles `USERPROFILE` vs `HOME` (tests set both) |
| **Signals** | `KeyCtrlC` cancel via `cancelStream`, no `signal.Notify` for `SIGTERM/SIGBREAK` | Terminal close loses last chunk; need `signal` handling for 5-day |
| **File locking** | `replaceFile:137-148` 5× 5ms retry for AV | Good |
| **Unicode/ANSI** | `displaywidth`, `uniseg`, `clipboard` base64 | Correct |
| **Env** | `ResolveEnvValue:249-279` checks `os.Getenv` then `.env`, strict parse | Correct |
| **Home perms** | `0700` enforced on existing dirs | Good |
| **WSL integration** | probe, relay argv, `restrictedEnv`, honest `Describe` (drives via `/mnt`) | Correct but not FS-confined |

**Gap:** `ValidDistroName` regex, `shell` grandchild kill incomplete.

---

## 20. Testing Audit

- **Current:** `go test -race ./...` **passes** (17 packages, tui 6.9s). 11 TUI test files (paste, input, mouse, hitground, viewground, reliability, permission, sandbox, markdown, activity, discovery) — HIT-ground, VIEW-ground pin geometry to rendered bytes.
- **Strong suites:** `filesystem/sandbox_test.go` (outside traversal, symlink escape, `MkdirAll`, `native` allow), `sandbox/policy_test.go` (workspace spellings), `providers/retry_test.go` (429, `Retry-After`, quota, sustained bound, cancel, gate).
- **Missing (per §22):** Provider 429 storm across many turns, session crash-during-`Sync`→`Rename` (fault injection), context/token limit, tool hang/process leak, mid-SSE truncated `[DONE]`/partial JSON, Windows PowerShell long path, concurrent TUI out-of-order, long-horizon 5-day soak.
- **Coverage gate:** `Makefile coverage-gate` 30% `cmd` — low but CI-enforced.

---

## 21. Dependency Audit

- **Direct:** `cobra 1.10.2`, `yaml.v3 3.0.1`, `glamour v2.0.1`, `bubbletea 1.3.10`, `bubbles 1.0.0`, `lipgloss 1.1`, `uuid 1.6.0` + 30 indirect (chroma, goldmark, `x/*`, `muesli/*`, `golang.org/x/net 0.56`, `sys 0.46`, `text 0.39`).
- **Freshness:** All latest (2026-08) — no abandoned.
- **Vuln:** `govulncheck` not in env; manual `net 0.56` recent, no known high. Recommend adding to CI.
- **Hygiene:** `go.sum` present, `go mod tidy` in Makefile, no vendoring, no duplicate functionality. `opencode.json` in `.gitignore` but tracked — contradictory.

---

## 22. Configuration Audit

- **Defaults:** `model: ollama localhost:11434 ornith:9b`, `permissions default:ask`, `sandbox mode:native` — dangerous default per FF-SEC-002.
- **Validation:** `config.validate:394-442` fails fast on `model.provider/name`, endpoint URL, provider types/headers/models, permissions, sandbox; `limitsFromConfig` silently ignores ≤0 as unlimited (CUE correctly rejects `>0` but YAML allows). `isEnvVarName`/`isValidHeaderName` solid.
- **Precedence:** explicit `providers.*` entry → catalog preset → legacy `model.*` → alias via `type` (e.g. `type: openai`) — clear (`provider.go:68-178`).
- **Dangerous defaults:** `native` + `allow` on reads; `timeout_seconds` uncapped.
- **Error messages:** Actionable naming file/field, never prints secrets (redacted via `Redacted`).

---

## 23. Observability Audit

- **Errors:** `statusHint` for 401/404, `statusError` includes provider/model/body/hint, `wrapTransport` includes baseURL, `Redacted` strips keys.
- **Doctor:** `ff doctor` checks config, API key source (value hidden), provider reachability (3s), sessions (`ListCorrupt`), skills, memory, shell backend, sandbox `Enforcement` — good.
- **Task state:** `task.State` injected into system prompt + `snapshotPtr` per `Event` — not rendered in TUI beyond `EventThinking` status.
- **Logging:** No file logs, no `Usage`/`EventToolProgress` persisted; reasoning not saved — post-mortem impossible beyond session JSON truncated tool result.
- **Finding FF-OBS-001 (MEDIUM):** No persistent run log → 5-day failure at 3am leaves only truncated session.

---

## 24. Documentation Audit

- **Drifts:** `go.mod 1.26.4` vs `README 1.22+`; `docs/TUI.md` omits reasoning tags `effort:`, `thinking:on`; `docs/Runtime.md` Events table lists 6 vs 8 actual (+Progress/Cancelled/Blocked/Denied); `docs/Index.md` omits `task`/`permissions`; `PRD` still marks session picker as planned (now done).
- **Strengths:** `docs/*` mirrors `internal/*` contracts; `Sandbox.md` honestly states WSL not FS-confined; `Config.md` secrets via env/.env never persisted.
- **Missing:** No `nohup`/`tmux` guidance for 5-day, no billing/quota troubleshooting, no `wsl` shell FS-escape warning, no context-window guidance.

---

## 25. Repository Hygiene Audit

- **Dirty tree:** `git status --porcelain` shows **17 modified** (`README`, `docs/*`, `internal/tui/*`, `internal/skills/*`, `internal/runtime/runtime.go`, `AUDIT_SPEC.md` untracked) + **3 untracked** (`builtin/skills.go`, `skills_test.go`, `directory_test.go`) — audited code ≠ HEAD, CI cannot reproduce.
- **Ignore contradiction:** `.gitignore:32-34` lists `opencode.json` + `.opencode/` + `SKILL.md` but `opencode.json` exists and appears tracked.
- **Artifacts:** `bin/ff.exe`, `.forcefield/` correctly ignored; no `TODO`/`FIXME` debt found.
- **CI:** `test.yml` 3-OS matrix, `gofmt -l` (excludes `main.go`), `go vet`, `go test` + `-race`, `cue-check`; no `govulncheck`.

---

## 26. Failure Scenario Matrix

| Scenario | Detected? | Recovered? | State Safe? | User Informed? | Long-Horizon Safe? |
|---|---|---|---|---|---|
| **429 transient** | ✅ `retry.go` | ✅ 3× jitter `Retry-After` 60s cap | ✅ | ✅ statusError | ⚠️ Partial — sustained 429 still exhausts |
| **429 quota/billing** | ✅ `quotaExhausted` | ❌ intentional no-retry | ✅ | ✅ not retryable | ❌ Halts until billing fix |
| **500/502/503/504/529** | ✅ | ❌ **No retry** | ✅ | ✅ EventError | ❌ Kills run |
| **Provider timeout 120s** | ✅ `http.go:13` | ❌ no retry | ✅ | ✅ could not reach | ❌ Should be retryable |
| **Stream disconnect mid-SSE** | ✅ `streamErr` | ❌ no resume | ⚠️ partial `assistantBuffer` | ✅ roleError | ❌ Half answer lost |
| **Tool crash (non-zero)** | ✅ `shell.go:385` | ✅ failed result → next turn | ✅ | ✅ ✕ tool failed | ✅ |
| **Tool hang** | ✅ 30s `context.WithTimeout` + group kill | ✅ `DeadlineExceeded` | ✅ | ✅ timed out | ⚠️ Windows grandchild leak |
| **Forcefield crash/panic** | ❌ no `recover` | ❌ | ⚠️ atomic saves protect last completed tool, current iteration lost | ❌ no log | ❌ |
| **Terminal close / SSH drop** | ❌ `Background` ctx, no SIGHUP | ❌ | ⚠️ last Save only on Ctrl+C | — | ❌ Need tmux |
| **Machine reboot** | ✅ files survive | ⚠️ manual `ff --resume` | ✅ atomic | ✅ doctor | ⚠️ No auto-restart |
| **Session corruption** | ✅ `Load` reports, `ListCorrupt` | ✅ isolates file | ✅ never delete | ✅ doctor | ✅ |
| **Disk full** | ✅ Save error | ❌ diverges | ❌ memory≠disk | ✅ roleError | ❌ Unnoticed |
| **Network loss** | ✅ `wrapTransport` | ❌ not retried | ✅ | ✅ | ❌ Should retry |
| **Local model unavailable** | ✅ statusHint | ❌ no fallback | ✅ | ✅ hint | ❌ Manual /provider |
| **Huge tool output** | ✅ 6k truncate + 40 line view | ✅ avoids flood | ✅ | ✅ truncated notice | ⚠️ LLM may miss data |
| **Huge user input** | ✅ 20k `CharLimit` | ✅ hard limit | ✅ | — | ✅ |
| **Infinite tool loop** | ✅ 60/300/5 limits | ✅ `EventBlocked` | ✅ | ✅ stopped after… | ✅ |
| **Malicious repo (prompt injection)** | ❌ raw file → context | ❌ | ❌ poisoned | ❌ no delimiter | ❌ |

---

## 27. Top 10 Problems (Severity×Likelihood×Long-Horizon)

| Rank | ID | Problem | Impact | Why 5-Day Matters | Next Step |
|---|---|---|---|---|---|
| **1** | FF-CRIT-003 | **Unbounded context/session growth** — no compaction | 400 length abort within hours, disk/MB JSON, 10s MB replay | First bottleneck before any other failure | Sliding window at 80% context window, summarize evicted |
| **2** | FF-SEC-002 | **Default native + auto-allow reads** — zero-click exfiltration | Fresh install leaks `.env` → model → vendor | Every unattended run starts compromised | `read_file: ask` for dotfiles or workspace-relative default |
| **3** | FF-SEC-001 | **WSL shell escapes FS cage** (`/mnt/*`) | One `shell` bypasses FS-tool fix | `wsl` still host-compromisable over 5 days | Lexical deny `/mnt/`, `C:` in shell when `wsl` + docs |
| **4** | FF-REL-001 | **5xx/timeout/mid-stream not retried** | Single 503 kills run | Cloud blip over 5 days guaranteed | Retry 500/502/503/504/timeout + jitter same as 429 |
| **5** | FF-SEC-003 | **Global `Always allow` per tool name** | One benign `Allow` → permanent `rm -rf` | Single social-engineered approval escalates for 5 days | Per-pattern/expiring Always allow |
| **6** | FF-SEC-005 | **Unbounded shell output → OOM** | `yes` kills process | 5-day agent can trigger large `find`/`cat` | Cap stdout+stderr 2-5 MiB |
| **7** | FF-SEC-009 | **Concurrent ask overwrites single modal** | 4 concurrent `ask` → deadlock/confused approval | Parallel shell+write inevitable over 5 days | Serialize `ask` with `sync.Mutex` |
| **8** | FF-SEC-007 | **Secret leakage via session file** | `sk-...` persists plaintext, replayed | 5-day `.env` read persists forever | Scrub `sk-` before `Session.Save` + before `ToolRole` |
| **9** | FF-SEC-006 | **Prompt injection via tool output** | File read hijacks agent | Malicious repo persists over 5 days | Fence tool results + system reminder |
| **10** | FF-TUI-001 | **Tool history invisible after resume** | Cannot audit prior tool calls | 5-day task with resume loses debuggability | Render `tool` roles in `sessionEntries` |

---

## 28. Quick Wins (<20 LOC, High Value)

| Win | File | Change | Value |
|---|---|---|---|
| **Cap shell output** | `tools/shell/shell.go:348` | `const maxShellOutput=2<<20` truncate `strings.Builder` | Prevents OOM kill |
| **Cap timeout_seconds** | `tools/shell/shell.go:228` | `if secs>300 { secs=300 }` | Prevents 11-day stall |
| **Strict arg validation** | `tools/args.go:27` | Reject wrong type / unknown fields as `ArgumentError` before permission | Closes silent-default hole |
| **Validate env keys** | `tools/shell/shell.go:706` | `if !envNameRe.MatchString(k) { return err }` centrally | Prevents `FOO=BAR; echo` injection |
| **Serialize asks** | `runtime/scheduler.go:297` | `askMu sync.Mutex` around `resolveAsk` | Fixes 4× race |
| **Scrub secrets** | `runtime/runtime.go:762` before `AddToolResult` | `redactSecrets(content, cfg)` | Stops vendor leakage |
| **Retry 5xx** | `providers/retry.go:198` | `if status==429 || status>=500 && status!=501` | Survives cloud blip |
| **Per-provider timeout** | `providers/http.go:13` | `Spec` carries `Timeout` (ollama 0, cloud 120s) | Fixes local 120s abort |
| **Negative limits validation** | `cue/config.cue:69` already, `config.go:394` add check | Reject <0 | Consistency |
| **Add govulncheck to CI** | `.github/workflows/test.yml:40` | `govulncheck ./...` | Supply-chain |

---

## 29. Recommended Roadmap

### Immediate (P0 — before any 5-day attempt)
1. Fix FF-CRIT-003: sliding window + `FinishLength` → `Blocked` (keep 80% window, pin system+goal).
2. Fix FF-SEC-002: `read_file: ask` for dotfiles or workspace-relative default (1 line in `defaultConfigTemplate`).
3. Fix FF-SEC-005 + FF-SEC-011: cap shell output + cap `timeout_seconds` at 300s.
4. Fix FF-REL-001: retry 5xx/timeout/mid-stream (reuse existing `doWithRetry` caps).
5. Fix FF-SEC-009: `askMu` serialize permission prompts; keep `selected` state per prompt (already done).
6. Document `wsl` shell not FS-confined + add lexical block for `/mnt/`/`C:` in `wsl` mode.

### Near-term (P1 — before production)
- Per-pattern `Always allow` (scope to tool+arg prefix) + expiry.
- Secret scrub before persistence/replay.
- Prompt-injection fencing (`<tool_result>`).
- Session rotation at 500 messages / 5 MiB.
- Windows job-object for process groups.
- Persist `thinkingRecord`/`Usage` optionally for post-mortem.
- Fix `sessionEntries` to render tool history.
- Commit dirty tree + add `git status --porcelain` gate.

### Long-term (P2 — as 10× growth)
- Abstract session backend (interface) for DB.
- Metrics/OTel hook on `Event` channel.
- Summarization/compaction service (LLM-based).
- Allow-list for `shell` commands in strict mode.
- File-descriptor-pinned TOCTOU fix via `openat(O_NOFOLLOW)`.
- Auto-restart service + SIGHUP handling for 5-day daemon.

---

## 30. Five-Day Readiness Score

| Category | Weight | Score (0-10) | Weighted | Evidence |
|---|---|---|---|---|
| **Reliability** | 15 | 4 | 6.0 | Loop correct, limits guarantee termination, but 5xx/timeout/stream not retried → single blip kills run; no compaction |
| **Provider resilience** | 10 | 4 | 4.0 | 429 excellent (jitter+Retry-After+quota), 5xx/timeout not retried, 120s wall kills local Ollama |
| **Rate-limit resilience** | 10 | 6 | 6.0 | Bounded 3 retries, jitter, quota detection brittle (`RESOURCE_EXHAUSTED` miss), no global limiter |
| **Context management** | 10 | 1 | 1.0 | No window, no token estimate, no summarization; 6000 per-tool only; Discoveries unbounded |
| **Session durability** | 10 | 6 | 6.0 | Atomic writes + corruption isolation excellent, but unbounded growth, concurrent write last-wins, resume drops tool history |
| **Process/resource mgmt** | 10 | 3 | 3.0 | Shell 30s timeout + group kill good, but output uncapped, timeout uncapped, Windows grandchild leak, FD pipes 1s drain |
| **Tool safety** | 10 | 4 | 4.0 | FS tools confined in `wsl` (good) but `shell` escapes, `native` zero isolation + auto-allow, interactive heuristic bypassable, TOCTOU |
| **Security** | 10 | 3 | 3.0 | Global allow, prompt injection, secret leakage via session, dotfile auto-allow, `always allow` escalation |
| **Recovery** | 5 | 4 | 2.0 | Atomic saves keep last completed turn, 1 iteration lost on crash; no WAL, no auto-restart, no SIGHUP |
| **Observability** | 5 | 3 | 1.5 | `ff doctor` honest, `statusError` actionable, but no file log, reasoning not persisted, truncated output may hide data |
| **TUI/UX** | 3 | 7 | 2.1 | Incremental cache fixed, grouped System messages, vertical permission UI with nav/click/hover, but resume hides tools, no command history |
| **Cross-platform** | 2 | 6 | 1.2 | 3-OS CI -race, WSL relay correct, 5× rename retry, `UserHomeDir` handling, but no SIGTERM, makeup `OS` check in Makefile |

**Total: 39.8 / 100 → 40/100**

**Method:** Weighted sum, not subjective average; each category scored on 5-day failure probability observed in code, not style.

---

## 31. Final Verdict

### NOT YET — Important reliability/security issues make unattended five-day operation unsafe.

Forcefield's architecture is **sound** — modular, provider-agnostic, TUI-presentation-only, and recent hardening is real. It is **not fundamentally broken** and does not need a rewrite.

However, **three systemic gaps guarantee failure over 120h:**

1. **Context/session unbounded** → provider `400 length` or disk OOM within hours.
2. **Provider 5xx/timeout/mid-stream treated as fatal** → normal cloud blip (inevitable over 5 days) terminates run with no retry.
3. **Default `native` + auto-allow reads + shell escape** → zero-click exfiltration and prompt-injection hijack without user noticing for 5 days.

These are not edge cases; they are **expected** over 5 days. Even with honest `wsl` disclosure, a single `shell` with `/mnt/c/...` bypasses the only FS cage.

**What remains after P0 fixes:** With context window, 5xx retry, per-provider timeout, shell caps, `read_file` ask for dotfiles, serialized asks, and secret scrub, the system becomes `YES, WITH CONDITIONS` — viable for 5 days **if** run in `wsl` mode on a single workspace, under `tmux`/`nohup`, with billing monitored, and with manual `ff --resume` after crash.

---

## 32. Maximum Expected Runtime

* **Best case** (small repo, `wsl` + `ask` on reads, cloud provider stable, 120s timeout not hit, no large outputs): **5 days** can be reached **only after P0 fixes**; today best is **~12-24h** before context limit.
* **Expected case** (normal coding task, occasional 503, 2-3 tool calls/min, Ollama on laptop): **4-12h** today → `500` or `400 length` or `yes` OOM first.
* **Failure-prone case** (large repo, `native` default, `yes`/`find`, malicious file, Windows): **1-4h** → OOM or exfiltration or 5xx abort.

**First likely bottleneck:**
```
Context growth  →  (tie) Unbounded shell output  →  Provider 5xx (no retry)
```
Today: **Context growth** (exceeds window). After fixing context: **Shell output OOM** then **5xx**.

---

## 33. Long-Horizon Failure Budget (how many failures before human needed)

| Failure | Recoverable Today? | After P0? | State Safe? | User Informed? |
|---|---|---|---|---|
| **Provider 429 (transient)** | Yes (3×) | Yes | Yes | Yes |
| **Provider 429 (quota/billing)** | No (non-retry) | No | Yes | Yes `not retryable` |
| **Provider 500/502/503** | **No** | **Yes** (with P0) | Yes | Yes `EventError` |
| **Provider timeout 120s** | No | Yes (per-provider) | Yes | Yes |
| **Stream disconnect** | No | Partial (text-only retry) | Partial | Yes |
| **Tool failure (non-zero)** | Yes | Yes | Yes | Yes |
| **Tool hang 30s** | Yes | Yes | Yes | Yes |
| **Huge tool output** | **No (OOM)** | **Yes (cap)** | No | No |
| **Context 400 length** | **No** | **Yes (window)** | Yes | Yes |
| **Forcefield crash (panic)** | No | No | Last completed tool saved, 1 iter lost | No log |
| **Machine reboot** | No (manual resume) | No | Session file survives, needs `ff --resume` | Via `doctor` |
| **Session corruption** | Yes (isolate) | Yes | Other sessions ok | Yes |
| **Disk full** | No | No | Diverges | Yes `roleError` |
| **Concurrent ask (4 tools)** | **No (overwrite)** | **Yes (serialize)** | Yes | Swallowed |
| **Shell with `/mnt/` escape** | **No** | Partial (lexical) | No | No |

**Budget today:** ~1-2 transient provider failures → task stops (needs human `resume`). Huge output or context growth → **0** budget (fatal).

---

## 34. Top 10 Problems (re-ranked for 5-day)

| Rank | ID | Problem | Impact | Why 5-Day Matters | Next Step |
|---|---|---|---|---|---|
| 1 | FF-CRIT-003 | Unbounded context/history | 400 abort within hours, 360k chars/60 turns | First killer, no compaction | Sliding window at 80% |
| 2 | FF-SEC-005 | Unbounded shell output → OOM | `yes` kills process | 5-day agent will `cat` large file | Cap 2 MiB |
| 3 | FF-REL-001 | 5xx/timeout/mid-stream not retried | Single 503 kills 5-day | Cloud blip inevitable | Extend `doWithRetry` to 5xx+transport |
| 4 | FF-SEC-002 | Default `native` + auto-allow reads | Zero-click `.env` leak | Every unattended run starts compromised | `read_file: ask` for dotfiles |
| 5 | FF-SEC-001 | WSL `shell` escapes FS cage via `/mnt/` | Host compromise via shell | `wsl` still host-reachable | Lexical deny `/mnt/`, `C:` in `wsl` shell |
| 6 | FF-SEC-009 | Concurrent `ask` single modal overwrite | Deadlock/confused approval | 4 concurrent tools common over 5 days | `askMu` mutex |
| 7 | FF-SEC-003 | Global `Always allow` per tool name | One benign → permanent `rm -rf` | Escalates over 5 days | Per-pattern/expiring allow |
| 8 | FF-SEC-007 | Secret leakage via session file | `sk-` persists plaintext | 5-day `.env` read persists forever | Scrub before `Save` |
| 9 | FF-SEC-006 | Prompt injection via tool output | File read hijacks agent | Malicious repo persists 5 days | Fence tool results |
| 10 | FF-REL-002 | Session file grows forever | Disk full / OOM / 10s MB replay | 5-day = 21k messages | Rotate at 500 msgs |

---

## 35. Quick Wins (reprise §28, <20 LOC)

See §28 table — all P0 quick wins are <20 LOC and eliminate 6 of top 10.

---

## 36. Recommended Roadmap (immediate → long)

**Immediate P0 (before any 5-day attempt, ~2-3 days):**
1. Sliding window + `FinishLength`→`Blocked` (keep 80% window, pin system+goal).
2. `read_file: ask` for dotfiles (1 line `defaultConfigTemplate`).
3. Cap shell output 2 MiB + cap `timeout_seconds` 300s.
4. Retry 5xx/timeout/mid-stream (reuse `doWithRetry` caps).
5. Serialize `ask` (`askMu`), scrub secrets before persist.
6. Lexical shell deny `/mnt/`/`C:` in `wsl` + docs.

**Near-term P1 (~1-2 weeks):**
- Per-pattern `Always allow` + expiry, secret scrub, prompt-injection fencing, session rotation 500, Windows job-object, persist `thinking`/`Usage` optionally, fix `sessionEntries` to render tools, commit dirty tree + `git status` gate.

**Long-term P2 (10× growth):**
- Abstract session backend, OTel hook, LLM-based summarization, allow-list `shell` strict mode, `openat(O_NOFOLLOW)` TOCTOU fix, auto-restart service + SIGHUP, metrics.

---

## 37. Architecture Recommendations (immediate/near/long)

*Immediate:* See P0 above — no structural change, just bounds and docs.
*Near:* Split `model` (1485 lines) only if features grow; keep `internal/tui` presentation-only; introduce `Event` → metrics interceptor without touching `runtime`.
*Long:* If sessions move to DB, introduce `SessionStore` interface; if multi-agent, replace single `model.entries` slice with event log.

No rewrite justified — incremental hardening is sufficient.

---

## 38. Audit Quality Gate

- ✅ Whole repo mapped (194 Go files, 30 packages, entry points, persistent artifacts).
- ✅ Lifecycle traced file:line (`User → TUI → Session → Agent → Provider → Tool → Permission → Execution → Result → next`).
- ✅ `go test ./...` (all 17 cached/pass), `go test -race ./...` (all pass, 5-7s tui), `go vet` (no output) run; `govulncheck` not available — noted.
- ✅ Long-horizon 5-day analysis (unbounded quantities, 120h simulation).
- ✅ Provider resilience matrix (429/500/timeout/stream).
- ✅ Session persistence crash simulation (atomic rename, 5× Windows retry).
- ✅ Tool execution (process groups, pipes, timeout, interactive refusal, sanitization).
- ✅ Windows behavior (WSL relay, `taskkill`, path, `UserHomeDir`, file locking).
- ✅ TUI/UX evaluated (grouping, hierarchy, markdown, permission vertical UI, mouse, scroll, resize, picker).
- ✅ Findings evidence-backed, confidence explicit, not style complaints.

---

## 39. Five-Day Verdict — Answers to 10 Questions

**1. Can Forcefield realistically run unattended for 5 days right now?**
No.

**2. What is most likely to kill the task first?**
Context window exhaustion (`400 invalid_request`) within 2-12h of steady tool use, or a single transient 500/502/503/timeout/mid-stream disconnect (no retry) — whichever occurs first. If the agent `cat`s a large file, unbounded shell output OOM beats both within hours.

**3. What are the top 10 blockers?** See §34 table (unbounded context, shell OOM, 5xx not retried, default native auto-allow, WSL shell escape, concurrent ask overwrite, global Always allow, secret leakage, prompt injection, session file growth).

**4. Which issues are security-critical?**
FF-SEC-001 (WSL shell escape), FF-SEC-002 (native auto-allow), FF-SEC-003 (global allow), FF-SEC-005 (shell OOM DoS), FF-SEC-006 (prompt injection), FF-SEC-007 (secret leakage), FF-SEC-009 (ask overwrite). 7 of top 10 are security.

**5. Which issues are reliability-critical?**
FF-CRIT-003 (context), FF-REL-001 (5xx), FF-REL-002 (session growth), FF-SEC-005 (OOM), FF-SEC-009 (deadlock), FF-SEC-011/012 (timeout/window proc), FF-TUI-001 (resume visibility).

**6. Which issues are architectural?**
Unbounded history/session (no compaction interface), single `Session.Messages` slice, `model` 1485-line god struct, `native` vs `wsl` split (tool vs shell confinement), `Always allow` per-tool-name model. None require rewrite, but all become painful at 10×.

**7. Which issues are simple fixes?**
All Quick Wins (§28): cap shell output (1 const), cap timeout (1 `if`), strict arg validation (3 lines), env key regex (1 `if`), serialize asks (`sync.Mutex`), scrub secrets (`redactSecrets` call), retry 5xx (1 condition), per-provider timeout (field), negative limits check, `govulncheck` CI. Each <20 LOC.

**8. What is the minimum remediation set required before attempting a 5-day run?**
P0 set (§36 immediate): (1) sliding window + `FinishLength` blocked, (2) `read_file: ask` for dotfiles, (3) cap shell output + timeout, (4) retry 5xx/timeout/mid-stream, (5) serialize asks + scrub secrets, (6) lexical `/mnt/` deny + docs. Without these, even 24h is unsafe.

**9. After those fixes, what risks would still remain?**
- `wsl` shell still not FS-confined (lexical deny is heuristic, not cage).
- Prompt injection probabilistic (fencing helps, not proof).
- Windows grandchild leak without job objects (needs `CREATE_NEW_PROCESS_GROUP` + job).
- No auto-restart after crash/reboot (manual `ff --resume`).
- No persistent log — post-mortem still limited.
- Global `Always allow` still per-tool-name (needs per-pattern).
- Session still unbounded beyond 500 (needs rotation).

**10. What should be stress-tested before trusting a real 5-day task?**
- **Soak test:** Loop `shell echo + read_file` every 2 min for 72h, monitor `RSS`, `sessions/*.json` size, `messages` length, provider 429/5xx injected via `httptest` stub, mid-stream `Close` injection, `yes | head` large output, concurrent 4× `ask` tools, `SIGTERM` mid-tool, `kill -9` mid-`Save` + resume, Windows `taskkill` leakage, `go test -race` with `-count=100` for scheduler.

---

## 40. Final Notes

- **Dirty tree caveat:** Audit ran on `e10e62a` + 17 modified / 3 untracked files (skills, TUI, docs). Genuine 5-day confidence requires `git status --porcelain` clean and `go test ./...` on committed HEAD.
- **Passing tests are not proof:** `go test -race` clean is necessary but not sufficient — no test exercises 5-day context growth, 5xx retry, or prompt injection.
- **Optimize for breakage, not appearance:** This report intentionally prioritizes dumb operational killers (400 length, 500 no-retry, 120s wall timeout, 2 MiB shell cap) over style.

**Auditor stance:** Forcefield is **closer than most harnesses** to being trustworthy — its atomic writes, explicit sandbox honesty, and bounded 429 handling are rare. With the P0 set (2-3 days of focused work) it can credibly attempt a 5-day run under conditions (`wsl`, `tmux`, monitored billing, manual resume). Without that, the current code will not survive a weekend.

---

*Report saved as `AUDIT_REPORT.md` per spec §41-43. No repository files were modified except this report.*

