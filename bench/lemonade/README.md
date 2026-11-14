# Forcefield × AMD Lemonade Comparison Benchmark Suite

This directory provides reproducible scripts for comparing Forcefield Babbage
coding-agent runs across three configurations on a **live local Lemonade
server** (no fabricated or simulated benchmark numbers):

1. **Existing Provider Baseline** (`openai-compatible` against the same OpenAI-compatible endpoint, or an explicitly configured baseline like `llama-cpp` / `ollama`).
2. **Lemonade without SlotGuard** (`type: lemonade`, `disable_slot_guard: true`) — includes `/api/v1/health` context discovery, cached token accounting, and `timings` throughput capture, without client-side slot serialization.
3. **Lemonade with SlotGuard** (`type: lemonade`, `disable_slot_guard: false`) — enables `SlotGuard` concurrency serialization aligned to Lemonade's `-np` slots, model-switch drain waiting, and `orchevents` / `ff bench` slot telemetry.

## Two-Stage Verification Discipline

- **Stage A — Deterministic Mock Plumbing Tests (no LLM inference):**
  Validates harness wiring, concurrency bounding, telemetry emission, and report formatting against local `httptest.NewServer` mocks. Does **not** measure real LLM speed or task quality:
  ```bash
  go test -v -run 'TestBenchmark_ThreeWayComparison_MockHarness|TestSlotGuard' ./internal/bench ./internal/providers
  ```
- **Stage B — Live Local Inference Comparison (real Lemonade runtime):**
  Uses `run_comparison.ps1` (Windows 11) or `run_comparison.sh` (Linux / WSL2) against a running `lemonade-server` instance. If the server is unreachable or has no loaded model, the script exits cleanly without fabricating numbers.

## Why `DELEG-001` and `DELEG-002` First?

Forcefield's parallel delegation tasks (`DELEG-001`, `DELEG-002`) fan out concurrent child sub-agents via `RunScopedProvider.CloneForRun()`. On a memory-constrained local machine (for example, Windows 11 with a 4 GB VRAM GPU and 16 GB RAM running `-np 1`), unguarded concurrent child turns collide on the single backend slot, causing slot contention, request timeouts, or KV-cache thrashing. Running all 15 tasks (`--tasks all`) or a focused subset measures both single-turn telemetry and multi-agent slot coordination under identical hardware, model, quantization, context size, and `-np`.

## Recorded Dimensions

Every JSON/Markdown comparison captures observed values directly from the host and `/api/v1/health`:
- Host OS, CPU, system RAM, and GPU summary
- Lemonade backend (`recipe`, e.g. `llamacpp`, `oga-ryzenai`, `flm`), execution device, model ID, quantization tag, context size (`ctx_size`), and parallel slots (`-np`)
- Task pass rate (`Passed / Total`)
- Latency (`total_ms`, `p50_ms`, `p95_ms`)
- Throughput (`avg_prompt_tps`, `avg_predicted_tps`)
- Token accounting (`prompt_tokens`, `completion_tokens`, `cached_tokens`)
- `SlotGuard` telemetry (`admitted`, `waits`, `wait_ms`, `degraded_fallbacks`)

## Usage

### Windows 11 (PowerShell)

```powershell
# Default: compare on DELEG-001 and DELEG-002 against http://localhost:13305/api/v1
.\bench\lemonade\run_comparison.ps1

# Explicit model and custom task set
.\bench\lemonade\run_comparison.ps1 -Model "Qwen2.5-Coder-3B-Instruct-GGUF" -Tasks "MOD-001,DEBUG-001,DELEG-001,DELEG-002"
```

### Linux / WSL2 (Bash)

```bash
# If lemonade-server runs on Windows host and WSL2 is in mirrored networking mode:
./bench/lemonade/run_comparison.sh

# If WSL2 NAT mode requires a Windows host-bridge URL:
LEMONADE_URL="http://172.24.0.1:13305/api/v1" MODEL="Qwen2.5-Coder-3B-Instruct-GGUF" ./bench/lemonade/run_comparison.sh
```

Outputs are written to a timestamped directory (default `bench/lemonade/results/<timestamp>/`) containing:
- `baseline.json` / `lemonade-unguarded.json` / `lemonade-slotguard.json`
- `comparison.md` (side-by-side summary table)
