#!/usr/bin/env bash
set -euo pipefail

# Reproducible 3-way Forcefield x AMD Lemonade comparison benchmark (Linux / WSL2).
# Requires a live Lemonade server; never fabricates numbers.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

LEMONADE_URL="${LEMONADE_URL:-http://localhost:13305/api/v1}"
BASELINE_TYPE="${BASELINE_TYPE:-openai-compatible}"
BASELINE_URL="${BASELINE_URL:-${LEMONADE_URL}}"
MODEL="${MODEL:-}"
TASKS="${TASKS:-DELEG-001,DELEG-002}"
BENCH_TIMEOUT="${BENCH_TIMEOUT:-180s}"
FF_BIN="${FF_BIN:-}"

BASE_ORIGIN="${LEMONADE_URL%/}"
BASE_ORIGIN="${BASE_ORIGIN%/api/v1}"
BASE_ORIGIN="${BASE_ORIGIN%/v1}"

echo "Checking live Lemonade server at ${BASE_ORIGIN}..."
if ! curl -fsS --max-time 3 "${BASE_ORIGIN}/live" >/dev/null && ! curl -fsS --max-time 3 "${BASE_ORIGIN}/api/v1/live" >/dev/null; then
  echo "ERROR: Lemonade server not reachable at ${BASE_ORIGIN}/live." >&2
  echo "Start lemonade-server (or pass LEMONADE_URL=... for WSL2 NAT host bridge) before running live benchmarks." >&2
  exit 1
fi

HEALTH_JSON="$(curl -fsS --max-time 5 "${BASE_ORIGIN}/api/v1/health")"
if [[ -z "${MODEL}" ]]; then
  MODEL="$(python3 -c 'import json,sys; d=json.loads(sys.stdin.read()); print(d.get("model_loaded") or "")' <<<"${HEALTH_JSON}")"
fi
if [[ -z "${MODEL}" ]]; then
  echo "ERROR: No model loaded on Lemonade (/api/v1/health returned empty model_loaded) and MODEL is unset." >&2
  exit 1
fi

TS="$(date +%Y%m%d-%H%M%S)"
OUT_DIR="${OUT_DIR:-${SCRIPT_DIR}/results/${TS}}"
mkdir -p "${OUT_DIR}"

if [[ -z "${FF_BIN}" ]]; then
  FF_BIN="${OUT_DIR}/ff"
  echo "Building ff into ${FF_BIN}..."
  go -C "${REPO_ROOT}" build -o "${FF_BIN}" .
fi

run_mode() {
  local mode="$1"
  local ptype="$2"
  local purl="$3"
  local disable_guard="$4"

  local mode_home="${OUT_DIR}/home-${mode}"
  local cfg_dir="${mode_home}/.forcefield"
  local raw_dir="${OUT_DIR}/raw-${mode}"
  mkdir -p "${cfg_dir}" "${raw_dir}"

  {
    echo "model:"
    echo "  provider: target"
    echo "  name: ${MODEL}"
    echo "providers:"
    echo "  target:"
    echo "    type: ${ptype}"
    echo "    base_url: ${purl}"
    if [[ "${ptype}" == "lemonade" ]]; then
      echo "    disable_slot_guard: ${disable_guard}"
    fi
  } > "${cfg_dir}/config.yaml"

  if [[ "${TASKS}" == "all" ]]; then
    echo "[${mode}] Running full benchmark suite..."
    HOME="${mode_home}" XDG_CONFIG_HOME="${mode_home}" "${FF_BIN}" bench run --results "${raw_dir}" --timeout "${BENCH_TIMEOUT}" >/dev/null
  else
    IFS=',' read -ra TASK_ARR <<< "${TASKS}"
    for t in "${TASK_ARR[@]}"; do
      t_trim="$(echo "$t" | xargs)"
      [[ -z "${t_trim}" ]] && continue
      echo "[${mode}] Running task ${t_trim}..."
      mkdir -p "${raw_dir}/${t_trim}"
      HOME="${mode_home}" XDG_CONFIG_HOME="${mode_home}" "${FF_BIN}" bench run --task "${t_trim}" --results "${raw_dir}/${t_trim}" --timeout "${BENCH_TIMEOUT}" >/dev/null
    done
  fi
}

run_mode "baseline" "${BASELINE_TYPE}" "${BASELINE_URL}" "false"
run_mode "lemonade-unguarded" "lemonade" "${LEMONADE_URL}" "true"
run_mode "lemonade-slotguard" "lemonade" "${LEMONADE_URL}" "false"

SUMMARY_PATH="${OUT_DIR}/comparison.md"
python3 - "${OUT_DIR}" "${TS}" "${LEMONADE_URL}" "${MODEL}" "${TASKS}" > "${SUMMARY_PATH}" <<'PYEOF'
import glob, json, os, sys

out_dir, ts, url, model, tasks = sys.argv[1:6]
print("# Forcefield x Lemonade Live Comparison\n")
print(f"- Timestamp: {ts}")
print(f"- Endpoint: `{url}`")
print(f"- Model: `{model}`")
print(f"- Tasks: `{tasks}`")
print(f"- Raw JSON reports: `{out_dir}/raw-*`\n")
print("| Mode | Environment | Backend / Quant | Ctx / -np | Tasks | Passed | Total (ms) | p50 (ms) | p95 (ms) | Avg Gen tok/s | Cached Tokens | SlotGuard Waits | Slot Wait (ms) |")
print("| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")

for mode in ["baseline", "lemonade-unguarded", "lemonade-slotguard"]:
    files = sorted(glob.glob(os.path.join(out_dir, f"raw-{mode}", "**", "*.json"), recursive=True))
    total = passed = dur = cached = waits = wait_ms = 0
    p50s, p95s, tps_list = [], [], []
    env_str = "-"
    backend_str = "-"
    ctx_np = "-"
    for fp in files:
        with open(fp) as f:
            d = json.load(f)
        env = d.get("environment") or {}
        if env:
            env_str = f"{env.get('os','')}/{env.get('arch','')}"
            b = env.get("backend") or "-"
            q = env.get("quantization") or "-"
            backend_str = f"{b} / {q}"
            ctx_np = f"{env.get('context_size',0)} / {env.get('parallel_slots',0)}"
        s = d.get("summary") or {}
        total += int(s.get("total_tasks", 0))
        passed += int(s.get("passed_tasks", 0))
        dur += int(s.get("total_duration_ms", 0))
        if s.get("latency_p50_ms"): p50s.append(float(s["latency_p50_ms"]))
        if s.get("latency_p95_ms"): p95s.append(float(s["latency_p95_ms"]))
        if s.get("avg_predicted_per_second"): tps_list.append(float(s["avg_predicted_per_second"]))
        tok = s.get("tokens") or {}
        cached += int(tok.get("cached_tokens", 0))
        waits += int(s.get("slot_guard_waits", 0))
        wait_ms += int(s.get("slot_guard_wait_ms", 0))
    p50 = round(sum(p50s) / len(p50s)) if p50s else 0
    p95 = round(sum(p95s) / len(p95s)) if p95s else 0
    tps = round(sum(tps_list) / len(tps_list), 2) if tps_list else 0.0
    print(f"| {mode} | {env_str} | {backend_str} | {ctx_np} | {total} | {passed} | {dur} | {p50} | {p95} | {tps} | {cached} | {waits} | {wait_ms} |")
PYEOF

cat "${SUMMARY_PATH}"
echo ""
echo "Comparison saved to: ${SUMMARY_PATH}"
