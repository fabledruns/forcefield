# AMD Lemonade Integration (Forcefield Babbage)

Forcefield Babbage integrates **AMD Lemonade** (`lemonade-server`) as a
first-class, hardware-agnostic local inference provider. The integration
respects Forcefield's local-first architecture, security boundaries, and
multi-agent delegation model without requiring AMD-specific hardware or NPU
drivers.

> **Scope note:** This integration targets **Forcefield Babbage** only.
> Lemonade runs locally over HTTP (`http://localhost:13305/api/v1` by default)
> with zero cloud accounts or external telemetry.

---

## Configuration

Add a `lemonade` provider entry to `~/.forcefield/config.yaml` (or via
`ff provider add`):

```yaml
model:
  provider: lemonade
  name: Qwen2.5-Coder-3B-Instruct-GGUF

providers:
  lemonade:
    type: lemonade
    base_url: http://localhost:13305/api/v1
    # Optional: disable SlotGuard for baseline A/B comparisons (default: false)
    disable_slot_guard: false
```

### Configuration Fields

| Field | Default | Notes |
| --- | --- | --- |
| `type` | — | Must be `lemonade`. |
| `base_url` | `http://localhost:13305/api/v1` | Normalized automatically whether `/api/v1` or origin URL is supplied. |
| `disable_slot_guard` | `false` | Set to `true` only to bypass `SlotGuard` concurrency coordination (for benchmarking baselines or external schedulers). |

---

## Windows 11 & WSL2 Notes

- **Windows 11 (primary local setup):** Point `base_url` at
  `http://localhost:13305/api/v1` (or the port `lemonade-server` listens on).
  Run PowerShell verification and benchmark scripts directly with `ff.exe`.
- **WSL2 (Ubuntu / Linux client to Windows host server):**
  - If WSL2 uses `networkingMode=mirrored`, `127.0.0.1:13305` works directly.
  - Under default NAT networking, `127.0.0.1` inside WSL2 points to the WSL2
    VM rather than the Windows host. Use the Windows host-bridge address or
    run `ff doctor`, which detects WSL2 NAT loopback failures and suggests the
    exact host-bridge remediation.
- **Entry-Level GPU / RAM Guidance (e.g., 4 GB VRAM GPU, 16 GB RAM):**
  - Quantized 3B–7B GGUF models (`Q4_K_M`) typically run with `-np 1` (1
    parallel slot) and moderate `ctx_size` (e.g. `8192`–`16384`) to fit inside
    VRAM.
  - Because `-np 1` only services one inference request at a time, Forcefield's
    parallel `delegate` sub-agents rely on `SlotGuard` to coordinate turns cleanly
    instead of flooding the server.

---

## Diagnostics & Provider Verification

Run `ff doctor` and `ff provider test` to inspect Lemonade readiness before
running agents:

```bash
# Inspect active provider health, model residency, recipe/backend, -np, ctx_size,
# WSL2 reachability diagnostics, and local llama.cpp vs Lemonade port conflicts
ff doctor

# Probe /live, /health, /models, and optional /slots without generating tokens
ff provider test lemonade

# Run a single non-streaming chat completion smoke check
ff provider test lemonade --chat
```

`ff doctor` reports:
- Live vs unreachable server status (`/api/v1/live` and `/api/v1/health`)
- Loaded model, recipe/backend (`llamacpp`, `oga-ryzenai`, `flm`, etc.),
  execution device, VRAM/memory usage, checkpoint/quantization, `ctx_size`,
  and `-np` parallel slots
- Whether the configured `model.name` is currently loaded (read-only notice;
  Forcefield never triggers `POST /api/v1/load`)
- WSL2 loopback-to-host-bridge remediation hints when running inside WSL2

---

## Context Window Discovery

Lemonade's `/api/v1/models` endpoint omits loaded context limits, while
`/api/v1/health` exposes `all_models_loaded[].recipe_options.ctx_size`.

During startup and turn context estimation (`internal/runtime/runtime_models.go`
and `internal/providers/lemonade.go`):
1. If the user explicitly configured a context limit, that value wins.
2. Otherwise `LemonadeProvider.EffectiveContextWindow()` reads the live loaded
   model's `ctx_size` from `/api/v1/health` (falling back across aliases like
   `ctx_size`, `context_size`, `n_ctx`).
3. If the model is not yet loaded or `/health` is unavailable, Forcefield
   retains safe fallback context sizing without failing chat turns.

---

## SlotGuard Concurrency & Residency Coordinator

When Forcefield runs parallel child agents via `delegate`, each child receives
an isolated clone from `RunScopedProvider.CloneForRun()`:
- **Isolated per run:** Each clone has its own `requestGate` and reasoning
  configuration.
- **Shared across clones:** All clones from the same root `LemonadeProvider`
  share one `*lemonadeSlotGuard` coordinator.

### Guarantees

1. **Gate-before-lease & strict LIFO cleanup:** Every request acquires its
   per-clone `requestGate` before waiting on `SlotGuard`, and releases the shared
   `SlotGuard` lease strictly **before** releasing the per-clone `requestGate`
   across normal completion, mid-stream cancellation, and HTTP/SSE error paths.
2. **Full HTTP/SSE lease lifetime:** A `SlotGuard` lease remains held until the
   entire response body / SSE stream terminates, not merely until request
   submission starts.
3. **`/health` primary, `/slots` optional:** Concurrency capacity (`-np`) and
   model residency are derived from `/api/v1/health`; `/api/v1/slots` is queried
   only for the supported `llamacpp` recipe when checking external busy state.
4. **Deadlock-free model switch & drain:** When a request targets a model
   different from the currently resident model, `SlotGuard` waits for its own
   `localInFlight` requests to drain first (distinguishing Forcefield's own
   admitted turns via local state + `releaseGen`) before checking external
   `is_busy` activity, honoring `ctx.Done()` at every wait step.
5. **No silent model swapping (`POST /load`):** Forcefield never automatically
   loads/unloads models or modifies Lemonade's `-np` configuration. Pinned
   model mismatches when external slots remain saturated return a clear HTTP `409`
   conflict instead of deadlocking.
6. **Degraded fail-open:** If `/api/v1/health` is unreachable or returns malformed
   data, `SlotGuard` records a `degraded_fallbacks` counter and still admits the
   chat request rather than breaking a working inference endpoint.

---

## Orchestration & Benchmark Telemetry

### `orchevents` (`.forcefield/sessions/<id>.events.jsonl`)

Numeric-only, additive keys are emitted into `Meta` (zero response text, prompts,
file paths, or secrets):
- On `child_completed`: `slot_guard_admitted`, `slot_guard_waits`, `slot_wait_ms`,
  `cached_tokens`, `cache_n`, `predicted_tps`
- On terminal `run_completed` / `run_failed` / `run_cancelled` / `run_blocked`:
  `slot_guard_admitted`, `slot_guard_waits`, `slot_wait_ms`,
  `slot_guard_degraded`, `cached_tokens`, `cache_n`, `prompt_tps`, `predicted_tps`

### `ff bench`

`ff bench` reports:
- Observed `BenchEnvironment` metadata (`os`, `arch`, `cpu`, `ram_gb`, `gpus`,
  `provider_type`, `backend`, `device`, `quantization`, `context_size`,
  `parallel_slots`, `slot_guard_mode`, `lemonade_live`)
- `Latency p50 / p95` (`latency_p50_ms`, `latency_p95_ms`)
- Average `Prompt` and `Predicted` throughput (`tok/s`)
- `CachedTokens` and `SlotGuard` counters (`admitted`, `waits`, `wait_ms`,
  `degraded_fallbacks`)

---

## Practical Demo Walkthrough

### 1. Verify Server & Configuration

```bash
# Inspect diagnostics
ff doctor

# Probe Lemonade endpoints and confirm a model response
ff provider test lemonade --chat
```

### 2. Run a Multi-Agent Delegation Task & Inspect SlotGuard Telemetry

```bash
# Run a task that fans out concurrent coding/research sub-agents
ff run "Inspect crypto/hash.go and auth/token.go in parallel and summarize security issues."

# Inspect numeric SlotGuard and throughput metadata in the latest .events.jsonl log
ls -t ~/.forcefield/sessions/*.events.jsonl | head -n 1 | xargs tail -n 10
```

Look for `child_completed` and `run_completed` events carrying `"meta":{"slot_guard_admitted":...,"slot_guard_waits":...,"slot_wait_ms":...,"cached_tokens":...,"predicted_tps":...}` with zero leaked prompt or response text.

### 3. Run the 3-Way A/B/C Benchmark Comparison

Compare **Baseline (`openai-compatible`)**, **Lemonade (`disable_slot_guard: true`)**, and **Lemonade + `SlotGuard` (`disable_slot_guard: false`)** on your machine against a live Lemonade server:

```powershell
# Windows 11 PowerShell
.\bench\lemonade\run_comparison.ps1
```

```bash
# Linux / WSL2
./bench/lemonade/run_comparison.sh
```

See [bench/lemonade/README.md](../bench/lemonade/README.md) for details on isolating mock plumbing tests (`internal/bench/lemonade_compare_test.go`) from live inference measurements.
