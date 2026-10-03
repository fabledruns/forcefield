# HPOV — Forcefield Performance Benchmark Methodology

HPOV is Forcefield's own performance benchmark suite. It measures what
Forcefield actually does on a real machine, records every raw sample, and
decides — with stated evidence rules — whether a candidate build moved relative
to a baseline.

This document is the canonical methodology reference. Everything else that
mentions HPOV links here.

---

## Table of contents

1. [What HPOV is](#1-what-hpov-is)
2. [Benchmark philosophy](#2-benchmark-philosophy)
3. [Benchmark catalog](#3-benchmark-catalog)
4. [TUI startup methodology](#4-tui-startup-methodology)
5. [Memory methodology](#5-memory-methodology)
6. [Comparison methodology](#6-comparison-methodology)
7. [CLI usage](#7-cli-usage)
8. [Result format](#8-result-format)
9. [Reproducibility](#9-reproducibility-what-makes-a-result-trustworthy)
10. [Cross-platform limitations](#10-cross-platform-limitations)
11. [What HPOV does not claim](#11-what-hpov-does-not-claim)
12. [Developer quickstart](#12-developer-quickstart)
13. [Known deviations from the design plan](#13-known-deviations-from-the-design-plan)
14. [A/A calibration campaign](#14-aa-calibration-campaign)

---

## 1. What HPOV is

HPOV is a **local measurement and comparison harness**, not a public or
industry-standard benchmark. There is no leaderboard, no cross-vendor ranking,
and no published result corpus other than what you produce on your own
hardware.

It does five distinct things, and keeping them separate matters when reading
any output:

| Stage | What happens | Where it shows up |
| --- | --- | --- |
| **Measurement** | Run a defined workload N times, record every raw sample, the environment, and the quality signals. | `hpov run` → `hpov.result` |
| **Comparison** | Decide, per metric, whether a candidate moved relative to a baseline, using stated statistical and practical rules. | `hpov compare`, `hpov compare-live` → `hpov.compare` |
| **Calibration** | Measure how often the comparison claims something about identical inputs, before anyone trusts it with a gate. | `hpov calibrate` → `hpov.calibration` ([§14](#14-aa-calibration-campaign)) |
| **Interpretation** | A human reads the verdicts, the coverage section, the quality flags and the limitations before drawing any conclusion. | `hpov show`, `hpov compare` output |
| **CI gating (not in v1)** | Automatically failing a build on a regression. | **Not implemented.** See [§11](#11-what-hpov-does-not-claim). |

HPOV v1 ships the first four. It deliberately does **not** ship gating: no
CI integration, no selected "gating metric" list, no `accepted_changes`
allowlist. `hpov compare` exits 0 on a regression unless you pass
`--fail-on-regression`, and that flag is yours to add, not HPOV's to impose.

### Why it exists

Startup and memory work in Forcefield is easy to make worse and hard to notice.
A local-first agent harness spends most of its perceived speed in a handful of
places: process creation, Go runtime and package init, config load, and the TUI
reaching a frame a user can act on. HPOV exists to make those boundaries
measurable with the same discipline used for any other performance claim:

- the workload and its stop condition are named in the result;
- the raw samples are kept, so any statistic can be recomputed;
- the environment that produced them is recorded and fingerprinted;
- a change is only called a regression when a practical threshold **and**
  statistical evidence **and** noise checks all agree.

---

## 2. Benchmark philosophy

These are the rules the implementation is built around. They are the reason
HPOV's numbers can be argued with.

### 2.1 Measure real Forcefield behavior

Every benchmark spawns the real `ff` binary (or, for the TUI, the real
interactive binary on a real pty). There is no in-process shortcut, no mocked
runtime, and no proxy workload. HPOV lives in the same Go module as `ff` but
shares no product code path: `go build .` for `ff` never compiles the runner.

The one exception is `mem.tui.go-heap`, which is *derived* — see
[§5.3](#53-go-heap-memtuigo-heap). It is labelled `kind=e2e` still, because it
observes the real process, but its value comes from marker fields rather than
from an OS query.

### 2.2 Explicit workload boundaries

Every benchmark records, in its `params` and `predicate`:

- the exact command and arguments (`params.workload`);
- which boundary the clock stops at, stated in the benchmark's purpose text;
- what makes a sample valid (`predicate`, plus per-sample checks);
- what makes the whole benchmark `skipped`, `unsupported` or `invalid`.

A boundary nobody can name is a boundary nobody can audit. For example
`launch.headless-init.*` ends at **agent validation** — the process exits 1 by
design — not at the first prompt and not at inference. The boundary is also
enforced, not just described: a marker-on probe run before and after the timed
pass confirms the workload still reaches it.

### 2.3 First-run and steady-state are separate benchmarks

`launch.headless-init.first-run` and `launch.headless-init.steady` run the
same command with different preconditions, because they answer different
questions. "First run" means a **fresh isolated home directory**, not a cold
file cache: HPOV does not attempt to control the OS page cache, and says so in
the benchmark's own `purpose` text.

Mixing the two would make both numbers meaningless, so they are never mixed.

### 2.4 Reproducible fixtures

Every measured iteration gets an isolated `HOME`/`USERPROFILE` and working
directory, created **outside** the timed region (`spawn.Run` starts its clock at
process start, not at `Setup`). The child environment is built from an
allowlist scrub, and the scrubbed and overridden variables are recorded in the
result's `environment.env_overrides`.

Steady-state benchmarks prime the isolated home once, untimed, by running the
real workload so that `config.yaml` exists. What generates the file is not what
those benchmarks measure.

### 2.5 Raw samples are first-class

Every metric stores its full value vector, plus every iteration's validity and
attributes. Derived statistics (min/p50/p90/p95/p99/max, mean, stdev, MAD, IQR,
robust CV, bootstrap CI) are stored alongside, never instead.

`hpov validate` recomputes the statistics from the raw samples and refuses a
document whose stored numbers disagree. If a future change alters the
statistics, old documents become invalid rather than quietly wrong.

### 2.6 Invalid and unavailable are not zero

Three different states, kept distinct on purpose:

| State | Meaning | Effect |
| --- | --- | --- |
| invalid sample | the workload did not reach its boundary, timed out, or produced an implausible value | excluded from all statistics for that metric; `validity` records why |
| unavailable metric | the suite measured everything else, but this one endpoint was not observable (e.g. a missing marker) | excluded from *that metric's* statistics only; the iteration stays valid; the reason is recorded per iteration |
| missing metric | the metric does not exist on one side of a comparison | appears in `coverage` with a reason |

The rule is: **a zero is a measurement; a gap is not.** Filling a gap with `0`
would silently bias every statistic downward, so HPOV stores a placeholder in
the aligned vector (to keep index alignment) and excludes it, and reports the
reason. `hpov.compare` repeats this rule when reading stored documents: it
rebuilds each metric's sample list from the iteration records and drops
unavailable and invalid samples rather than trusting the stored vector.

### 2.7 No aggregate score

There is no total, index, grade, or weighted composite anywhere in HPOV — not
in the result schema, not in the comparison, not in the report. Every metric is
decided on its own evidence.

The reason is concrete rather than aesthetic: the metrics do not share a unit
(ms, bytes, bytes of Go heap, bytes of executable). Any weighting across them
would be an invented number, and an invented number is what people quote when
they want to claim a change was "20% faster".

The only roll-ups HPOV produces are **counts by verdict state**, which name
every state (`regressed=1 unchanged=3 inconclusive=2`) and are used for
reading, not for scoring.

### 2.8 Statistical evidence *and* practical evidence

A verdict needs both:

- **practical**: the change reaches the threshold for that benchmark family
  (`max(rel_thr × base_p50, abs_floor)`), so a 3% move on a noisy 20 ms
  baseline is not a regression;
- **statistical**: the 95% bootstrap CI of the difference of medians excludes
  zero **and** a two-sided Mann-Whitney U test gives p < 0.01 with n ≥ 8 per
  side.

Neither alone is a verdict. See [§6](#6-comparison-methodology) for the exact
rules, including the noise multiplier and the family correction.

### 2.9 Quality is part of the result

Every run carries a quality label and flags derived from the environment, not
from opinion:

| Flag | Condition |
| --- | --- |
| `high_idle_cpu` | whole-system busy CPU above 10% during the idle sample |
| `spawn_floor_drift` | the spawn-floor calibration moved more than 25% between the start and end of the run |
| `many_noisy_benchmarks` | more than 30% of OK benchmarks carry a `noisy:` flag |
| `<benchmark-id>:drift:<metric>`, `<benchmark-id>:bimodal:<metric>` | a per-metric detector fired (see [§2.10](#210-per-metric-noise-detection)); copied into the run-level flags so one bad metric is visible without opening each benchmark |

The label is `poor` when `spawn_floor_drift` or `many_noisy_benchmarks` is
present, `degraded` when any other flag or any benchmark flag is present, and
`good` otherwise.

Poor runs are recorded, not discarded: `hpov compare` downgrades any
regression or improvement verdict resting on a poor run to `inconclusive`
([§6.8](#68-quality-and-noise-handling)).

### 2.10 Per-metric noise detection

Three detectors run per metric, and their output is stored as
`noisy:<metric>`, `bimodal:<metric>`, `drift:<metric>` flags on the benchmark:

| Detector | Rule |
| --- | --- |
| noisy | robust CV (`1.4826 × MAD / median`) above the benchmark's `CVThreshold` (default 0.10; 0.03 for headless memory, 0.05 for TUI ready RSS, 0.15 for the TUI timeline) |
| bimodal | a gap in the sorted samples wider than `max(5 × MAD, 0.2 × median)` with at least 3 samples on each side |
| drift | Spearman ρ against iteration index with `|ρ| > 0.5` and `p < 0.01` — a systematic trend across the run |

### 2.11 Inconclusive is a valid outcome

`inconclusive` is a first-class answer, not a failure to produce output. So are
`incompatible`, `not_comparable` and `invalid`. HPOV would rather report that
it cannot decide than report a number it cannot defend.

### 2.12 Platform semantics are explicit

Where the OS's notion of "resident memory" differs, the metric carries a
`platform_semantics` tag naming exactly what was read:

```
peak_rss_bytes       windows:peak_working_set_exact
                     linux:vm_hwm_exact
                     darwin:rusage_maxrss_exact
```

Values are comparable **within one OS only** unless the tag says otherwise.
`mem.tui.go-heap` is the exception that proves the rule: it is Go-level, so its
tag is `go:heapalloc` on every platform and it is comparable across hosts for
the same build.

---

## 3. Benchmark catalog

Nine benchmarks are implemented. `calibration.noop` is infrastructure (tier 0),
not a measurement of Forcefield, and is documented separately in
[§3.11](#311-calibrationnoop).

### 3.1 launch.version

| | |
| --- | --- |
| **Kind** | latency (`e2e`, tier 1) |
| **Workload** | `ff --version` |
| **Metrics** | `wall_ms` (ms, lower is better), `cpu_ms` (ms, lower is better) |
| **Plan** | quick 2/10, standard 5/30, full 5/100 |
| **Validity** | `exit_code==0 and stdout starts with 'ff version'` |

**Purpose.** The floor of `ff` startup. It is OS process creation plus Go
runtime and package init plus one cobra parse. It is *not* headline startup
speed — it exists so you can subtract a known constant from the other launch
numbers.

**Boundaries.** `wall_ms` starts immediately before process start and ends when
`Wait` returns. `cpu_ms` is kernel user + system time for that process.

**Fixtures.** One isolated home and workdir per (benchmark, subject), created
untimed. `--version` never touches config, so the home is isolated for
consistency rather than because it is required.

**Recorded attributes.** `exit_code`, `stdout_bytes`, and the truncated first
stdout line (`version`), so a different binary can be detected from the result
alone.

**Limitations.** Includes process creation cost, which differs by OS by design
and is *not* normalized away. `stdout_bytes` grows when commands are added;
that shows up here as a latency change that is really a content change.

### 3.2 launch.help

| | |
| --- | --- |
| **Kind** | latency (`e2e`, tier 1) |
| **Workload** | `ff --help` |
| **Metrics** | `wall_ms`, `cpu_ms` |
| **Plan** | quick 2/10, standard 5/30, full 5/100 |
| **Validity** | `exit_code==0 and stdout contains 'Usage:'` |

**Purpose.** The same floor plus cobra's help rendering, so help output size is
part of the measurement. Attributes record `exit_code` and `stdout_bytes`: when
the command set changes, help gets bigger and this benchmark legitimately gets
slower. That is why `stdout_bytes` is stored per iteration.

**Boundaries.** Identical to `launch.version`.

**Limitations.** Same as `launch.version`, plus: this number is sensitive to
terminal width when stdout is a pipe. HPOV always runs it with a non-tty
stdout, so the value is stable for a given command set.

### 3.3 launch.headless-init.steady

| | |
| --- | --- |
| **Kind** | latency (`e2e`, tier 1) |
| **Workload** | `ff run --agent __bench_bogus__ x` |
| **Metrics** | `wall_ms`, `cpu_ms` |
| **Plan** | quick 2/10, standard 5/30, full 5/100 |
| **Validity** | `exit_code==1`; per-run probe must see the unknown-agent error and `stage-agents` |

**Purpose.** Full runtime construction on the headless path: config load, skills
catalog, repository root, memory store, provider construction, tools,
policy/sandbox executor, agents — then a deliberate local exit at agent
validation.

**Exit 1 is the success case.** `__bench_bogus__` is not a registered agent, so
a fully built runtime rejects it. Exit 1 by design means the workload reached
agent validation; anything else means it stopped earlier and the sample is
invalid.

**Pre- and post-probes.** The benchmark implements `bench.Prober`, so HPOV runs
it once with `FF_PERF_MARKERS=1` before the timed pass and once after, checking
all three of: exit code 1, the unknown-agent error text, and the presence of the
`stage-agents` marker. Both results are stored in `validity.probe_pre` /
`probe_post`. This guards against a future release that moves agent validation
earlier — which would silently make this benchmark measure something else.

**Boundaries.** `wall_ms` spans process start to exit. Setup work (creating the
fresh workdir) happens outside the timed region.

**Limitations.** The workload stops at agent validation, so it does not include
a model round trip, MCP startup, or tool execution. It measures *construction*,
which is the part that regresses silently.

### 3.4 launch.headless-init.first-run

| | |
| --- | --- |
| **Kind** | latency (`e2e`, tier 1) |
| **Workload** | `ff run --agent __bench_bogus__ x` |
| **Metrics** | `wall_ms`, `cpu_ms` |
| **Plan** | **no warm-up** (0/10 quick, 0/30 standard, 0/100 full) |
| **Validity** | `exit_code==1`; pre/post probe as above |

**Purpose.** The same construction path from a **fresh** home: `config.Dir`
creates and chmods `~/.forcefield`, `config.Load` writes `config.yaml` and
prints `Created default config` to stderr.

**Steady vs first-run.** Warm-up is disabled for this benchmark on purpose. A
warm-up iteration would create the config, and every measured iteration would
then be measuring the steady path under a first-run benchmark's name.

**What "first run" is not.** It is **not** an OS-cold measurement. HPOV does not
flush the page cache, and cannot: "fresh home" is a statement about Forcefield's
own state, not about the filesystem cache. The benchmark's recorded
`purpose` text says this in as many words.

### 3.5 launch.artifact-size

| | |
| --- | --- |
| **Kind** | artifact size (`static`, tier 1) |
| **Workload** | none — `os.Stat` + SHA-256 of the subject binary |
| **Metrics** | `size_bytes` (bytes, lower is better) |
| **Plan** | quick 0/1, standard 0/3, full 0/3 |
| **Validity** | `stat` succeeds; `sha256` recorded per sample |

**Purpose.** The deterministic complement to timing. Executable size is not
memory usage, but it is exact, so it is the one metric in HPOV that does not
need statistics to interpret.

**Why repeated.** Three identical samples exist to make "the value changed"
visible in the result itself rather than inferred. All three carry the same
`sha256` attribute; a mismatch would mean the binary changed mid-run.

**Limitations.** Comparable across releases only for like-for-like builds. A
`-ldflags "-s -w"` build and a default build of identical source are different
artifacts, and this metric will say so with a large number. `hpov compare`
records a `confounded_build` warning when the recorded build metadata differs
([§6.7](#67-subject-and-build-constraints)).

### 3.6 tui.startup.timeline

| | |
| --- | --- |
| **Kind** | latency, many derived metrics (`e2e`, tier 1) |
| **Workload** | `ff` interactive, on a fixed 120×40 pty, `TERM=xterm-256color` |
| **Metrics** | 11 `t_*` marker metrics + 7 `seg_*` deltas (all ms, lower is better) |
| **Plan** | quick 1/4, standard 3/20, full 3/50 |
| **Validity** | `first-useful-frame` observed **and** clean `/exit` quit with `exit_code==0` |
| **Requires** | `pty` |

Documented in full in [§4](#4-tui-startup-methodology). In summary: a
reader-timestamped marker timeline from process spawn to runtime readiness,
with `first-useful-frame` as the primary readiness boundary.

**Recorded attributes.** `exit_code`, `quit_method` (`clean` or
`kill-after-quit-timeout`), `pty_bytes`, and `missing_marks` listing any mark
that did not arrive.

### 3.7 mem.headless.peak-rss

| | |
| --- | --- |
| **Kind** | memory (`e2e`, tier 1) |
| **Workload** | `ff run --agent __bench_bogus__ x` (headless, no pty) |
| **Metrics** | `peak_rss_bytes`, `peak_tree_rss_bytes` (bytes, lower is better) |
| **Plan** | quick 1/8, standard 2/15, full 3/30 |
| **Validity** | `exit_code==1` and at least one memory figure available |
| **Requires** | `memory` |

The same operation `launch.headless-init.steady` measures for time, run in
**separate iterations** so memory collection never shares a run with a latency
sample. Documented in full in [§5.1](#51-headless-peak-rss-memheadlesspeak-rss).

### 3.8 mem.tui.ready-rss

| | |
| --- | --- |
| **Kind** | memory (`e2e`, tier 1) |
| **Workload** | `ff` interactive, 120×40 pty, same session shape as the timeline benchmark |
| **Metrics** | `ready_rss_bytes`, `ready_tree_rss_bytes` (bytes, lower is better) |
| **Plan** | quick 1/8, standard 3/20, full 3/50 |
| **Validity** | `first-useful-frame` observed, memory query completed, clean `/exit` |
| **Requires** | `pty`, `memory` |

A point-in-time reading at readiness — **not** a peak. Documented in
[§5.2](#52-tui-ready-rss-memtuiready-rss).

### 3.9 mem.tui.go-heap

| | |
| --- | --- |
| **Kind** | derived data (`e2e`, tier 1) |
| **Workload** | `ff` interactive, 120×40 pty |
| **Metrics** | `go_heap_alloc_bytes`, `go_sys_bytes` (bytes, lower is better) |
| **Plan** | quick 1/8, standard 3/20, full 3/50 |
| **Validity** | `first-useful-frame` observed with an `alloc=` field, clean `/exit` |
| **Requires** | `pty`, `markers` |

Go-level accounting read from marker fields the subject already emits. Runs its
own pty pass rather than sharing iterations with `mem.tui.ready-rss`, because a
benchmark's samples must be independently repeatable. Documented in
[§5.3](#53-go-heap-memtuigo-heap).

### 3.10 Iteration plans at a glance

| Benchmark | quick | standard | full |
| --- | --- | --- | --- |
| default (launch.version, launch.help, launch.headless-init.steady) | 2 + 10 | 5 + 30 | 5 + 100 |
| launch.headless-init.first-run | 0 + 10 | 0 + 30 | 0 + 100 |
| launch.artifact-size | 0 + 1 | 0 + 3 | 0 + 3 |
| tui.startup.timeline | 1 + 4 | 3 + 20 | 3 + 50 |
| mem.headless.peak-rss | 1 + 8 | 2 + 15 | 3 + 30 |
| mem.tui.ready-rss | 1 + 8 | 3 + 20 | 3 + 50 |
| mem.tui.go-heap | 1 + 8 | 3 + 20 | 3 + 50 |

Format is *warm-up + measured*. `--n` and `--warmup` override the measured and
warm-up counts for every selected benchmark.

Memory plans use fewer iterations because memory is low-variance: the detector
thresholds reflect that (0.03 robust CV for headless peak RSS).

### 3.11 calibration.noop

`tier=0`, `kind=e2e`, not a Forcefield measurement. It re-executes
`hpov __noop` — a trivial process that exits immediately — through the same
spawn path, 30 times, at the start and again at the end of every run.

This brackets the run with the harness's own floor. It is reported in
`environment.calibration.spawn_floor_ms` and is **never subtracted** from any
benchmark: the difference between start and end is a drift signal (see
`spawn_floor_drift`), not a correction term.

---

## 4. TUI startup methodology

`tui.startup.timeline` is the most intricate benchmark, because it measures a
terminal application and the only honest boundaries are inside the process.

### 4.1 The harness

- A **real** `ff` process runs interactively on a **fixed 120×40 pty**
  (`pty.DefaultCols`/`DefaultRows`, `TERM=xterm-256color`, `COLUMNS=120`,
  `LINES=40`).
- One isolated, primed home per (benchmark, subject); a **fresh workdir per
  iteration** so sessions and traces never accumulate.
- `FF_PERF_MARKERS=1` is set for this pass — it is the marker pass. The
  headless launch benchmarks run their timed pass with markers **off**, so the
  `ReadMemStats` stop-the-world pause and the stderr writes never contaminate a
  headline latency number.

### 4.2 first-frame is not first-useful-frame

This distinction is the core of the benchmark, and it is the difference between
a plausible number and a meaningful one.

```
first-frame           first-useful-frame
-------------         --------------------
first View() call     first View() call that renders real content,
after the model is    after the terminal size is known AND the runtime
constructed, so      is ready
the model may still
be "Starting
Forcefield."
```

In code, the first marker fires on the first `View()` call whatsoever; the
second fires only when `m.ready` is true (the `WindowSizeMsg` has installed real
dimensions) and the startup placeholder is not what is being drawn. Both are
emitted from `View()`, and both are recorded once, via a
compare-and-swap guard.

**Why `first-useful-frame` is the primary boundary.** A user cannot type into a
frame that says "Starting Forcefield." Reporting `first-frame` as "startup time"
would systematically under-report by however long background initialization
takes — which is exactly the number a startup optimization is supposed to move.
HPOV reports both, but only `first-useful-frame` gates validity and drives
teardown.

### 4.3 The marker set

Markers are emitted by `internal/perfmark` when `FF_PERF_MARKERS` is set, as
lines on **stderr**:

```
ff-perf <event>
ff-perf <event> alloc=<bytes> sys=<bytes>
```

HPOV reports on 11 marks, from two branches:

| Branch | Marks |
| --- | --- |
| UI | `main-entry`, `config-loaded`, `first-frame`, `first-useful-frame` |
| Runtime | `runtime-init-start`, `stage-skills`, `stage-memory`, `stage-provider`, `stage-tools`, `stage-agents`, `runtime-ready` |

Two emitted marks are deliberately excluded:

- `stage-mcp` — the fixture runs with zero MCP servers, so the mark would
  describe work that did not happen.
- `stage-session` — it measures the `EventMem` stop-the-world pause, not a
  startup stage.

### 4.4 Timing source: the reader clock

**Markers carry no timestamps.** The marker protocol is
`ff-perf <event>` on stderr, with no clock field, so HPOV's reader goroutine
stamps each line with `time.Now()` **on receipt**.

Consequences, stated plainly because they limit what the numbers mean:

- Every phase delta includes the child's stderr write plus the reader's wake-up
  latency. A `stage-skills → stage-memory` delta of 0.3 ms is at the edge of
  what this clock resolves.
- Marker ordering is exact (lines are read in order), but **timing is bounded
  by the clock**, not by the child's internal precision.
- The host clock's resolution is measured at run start (`envinfo.CalibrateClock`
  spins until the reading changes, 1000 times, and takes the median step) and
  stored in `host.clock`. On the development host this is Go's monotonic
  `time.Now` at ~1 ms granularity.

Do not read sub-millisecond differences in these metrics as real.

### 4.5 Why a dedicated marker pipe

On Windows, a ConPTY child writes both its console output and its stderr into
the **same** pseudoconsole stream. That makes marker ordering unreliable: the
TUI's own escape sequences interleave with the marker lines, and a marker can
be split or reordered relative to console writes.

HPOV therefore gives the child a **dedicated stderr pipe** for markers and
drains the console stream to `/dev/null` in a separate goroutine (counting
`pty_bytes` along the way). Two consequences:

- Marker lines arrive unmixed with console output, so `ff-perf` detection is a
  prefix match on clean input.
- TUI rendering can never stall on a full pty buffer, because the harness is
  not reading slowly for the console's sake.

On Windows the child is additionally **started suspended** and resumed only
after job assignment, so the process is inside its job object before any of its
own threads exist. Without that, a child that spawns threads early can escape
the job and the tree cleanup would miss it.

### 4.6 Readiness, teardown, and cleanup

- **Readiness deadline**: 60 s. This is a failure detector, not
  synchronization — marks normally arrive within a few hundred milliseconds.
- **Tail deadline**: once `first-useful-frame` is seen, collection continues
  for up to 10 s waiting for `runtime-ready`, so a release that never emits it
  does not stretch every iteration to the full readiness timeout.
- **Collection ends** when both `first-useful-frame` and `runtime-ready` have
  been seen, or on EOF, or at a deadline. Marks already observed are never
  discarded mid-run.
- **Teardown**: write `/exit`, wait 150 ms, write `\r` **as a separate write**.
  One combined write is delivered as a paste, where the CR is inserted as a
  newline instead of submitting — a real failure mode that produces a hung
  child rather than a wrong number. Then wait up to 10 s for a clean exit;
  otherwise force-kill and record `quit_method=kill-after-quit-timeout`.
- **A forced kill fails the sample.** Clean termination is part of the validity
  contract, because a killed process may never have reached steady state.
- **Cleanup is unconditional**: the child is closed via `defer` on every path,
  including timeouts and panics, so HPOV does not leave orphaned `ff`
  processes or ConPTY sessions behind.

### 4.7 Unavailable phase metrics

Optional marks may be absent — a release may reorder stages or skip one. HPOV's
rule: **only the absence of `first-useful-frame` invalidates the sample.**

For every other metric whose endpoint was not observed:

- the metric is recorded as **unavailable** with a reason naming the missing
  endpoint (`marker stage-mcp not observed`, `endpoints main-entry and
  config-loaded not observed`);
- the sample stays valid;
- the metric's statistics are computed over the iterations where it *was*
  available.

`missing_marks` is also recorded as an attribute so the gap is visible in the
human report.

A segment exists only when **both** endpoints exist. A segment anchored at
spawn (`seg_process_to_main_entry`) uses the spawn timestamp, not a marker.

### 4.8 The seven segments

| Segment | From | To |
| --- | --- | --- |
| `seg_process_to_main_entry` | spawn | `main-entry` |
| `seg_main_entry_to_config_loaded` | `main-entry` | `config-loaded` |
| `seg_config_loaded_to_runtime_init_start` | `config-loaded` | `runtime-init-start` |
| `seg_runtime_init_start_to_first_useful_frame` | `runtime-init-start` | `first-useful-frame` |
| `seg_process_to_first_useful_frame` | spawn | `first-useful-frame` |
| `seg_stage_agents_to_runtime_ready` | `stage-agents` | `runtime-ready` |
| `seg_first_frame_to_runtime_ready` | `first-frame` | `runtime-ready` |

The first and fifth are the two numbers most people actually want: total
time-to-usable, split by where the process was when it became usable.

### 4.9 Limitations of the TUI benchmarks

- Reader-clock timing includes scheduling and pipe latency; see
  [§4.4](#44-timing-source-the-reader-clock).
- One iteration is one process, one session. There is no in-session reuse
  measurement.
- The pty is fixed at 120×40. Rendering cost at other sizes is not measured.
- The fixture has no repository, no MCP servers and no configured provider, so
  `stage-provider` measures provider *construction*, not a discovery round trip.
- ConPTY vs `/dev/ptmx` differ in transport overhead. Absolute values are
  comparable within one OS and pty mechanism only.

---

## 5. Memory methodology

Three memory benchmarks, three different questions. They are deliberately kept
apart, because merging them hides exactly the information you need.

### 5.1 Headless peak RSS (`mem.headless.peak-rss`)

Runs the headless workload (`ff run --agent __bench_bogus__ x`) with the same
primed home as `launch.headless-init.steady`, in **separate iterations** from
any latency sample.

**Two metrics, never combined:**

| Metric | What it is | Exactness |
| --- | --- | --- |
| `peak_rss_bytes` | the **root process's** peak, as tracked by the kernel | **exact** — the kernel's own high-water mark, readable after the process exits |
| `peak_tree_rss_bytes` | root **plus every descendant alive at sample time**, summed | **lower bound** — sampled every 10 ms |

**Root peak (exact).** Read from the OS after exit:

| OS | Source | Tag |
| --- | --- | --- |
| Windows | `PeakWorkingSetSize` via a retained `PROCESS_MEMORY_COUNTERS_EX` query handle | `windows:peak_working_set_exact` |
| Linux | `VmHWM` | `linux:vm_hwm_exact` |
| macOS | `rusage.Maxrss` (bytes) via `wait4` | `darwin:rusage_maxrss_exact` |

The Windows handle is opened while the process is alive because the query
handle remains valid after exit; keeping it is what makes the peak readable at
all.

**Tree peak (sampled, lower bound).** A sampler runs every`collect.DefaultInterval` = **10 ms** and sums the working set of the root plus
every live descendant.

A descendant that exists entirely between two samples is **missed**. The tree
figure can therefore omit memory but never invent it, which is why it is labelled
a lower bound in `platform_semantics` on every platform. HPOV records
`tree_samples`, `tree_max_descendants`, `tree_sampled_peak`, and
`tree_peak_may_be_missed=true` when zero samples succeeded.

The 10 ms interval was chosen by measurement, not by convention; it is
recorded per run (`params.sample_interval_ms`) and again per iteration
(`sample_interval_ms`), so a future change of the interval is visible in old
documents.

**Root peak is read after the process exits**, so it needs no live query: on
Windows the harness keeps a query handle open from before start precisely
because that is what makes the post-exit peak readable.

**Validity.** A run is invalid only when **neither** figure could be read. A run
missing one of the two stays valid with the other reported and the gap named —
losing a tree reading on a platform that cannot enumerate processes must not
throw away an exact root peak.

**Limitations.**

- Windows peak working set is **not** RSS: it includes shared and mapped pages.
  Values are comparable within Windows only.
- The tree figure is a lower bound; a short-lived grandchild can be invisible.
- Sampling stops when the root exits, so the kernel peak and the last sample are
  what remain.
- Peak RSS of the tree is not comparable to `launch.artifact-size`; they measure
  different things.

### 5.2 TUI ready RSS (`mem.tui.ready-rss`)

The question is "what does a ready, idle Forcefield session cost right now?" —
the resting footprint, not the high-water mark.

**Semantics.**

- Sampled **once**, inside the marker callback, at `first-useful-frame` — the
  same primary readiness boundary the timeline benchmark uses, on the same
  120×40 pty, with the same session shape.
- **Current** resident memory, not peak. A peak would answer a different
  question and is reported separately as the attribute
  `ready_root_peak_rss_bytes` with its own `ready_root_peak_semantics` tag.
- `ready_rss_bytes` is the **root process alone**. `ready_tree_rss_bytes` is
  root plus descendants at that instant, and is a lower bound for the same
  enumeration reason as above.

**Tags:**

| Metric | Windows | Linux | macOS |
| --- | --- | --- | --- |
| `ready_rss_bytes` | `windows:working_set_at_ready_root_only` | `linux:vm_rss_at_ready_root_only` | `darwin:resident_size_at_ready_root_only` |
| `ready_tree_rss_bytes` | `windows:sum_working_set_at_ready_lower_bound` | `linux:sum_vm_rss_at_ready_lower_bound` | `darwin:sum_resident_size_at_ready_lower_bound` |

**Sampling uncertainty is recorded, not hidden.** The query runs in the marker
callback, so the reading corresponds to some instant *after* the readiness mark.
HPOV records `root_sample_lag_ns`, `tree_sample_lag_ns`, and the host's
`clock_resolution_ns` per iteration. **A lag of 0 means "shorter than this host
can measure", not "instantaneous".** The tree lag is larger than the root lag
because enumerating the process table (Toolhelp on Windows, `/proc` on Linux)
costs more than a single query.

**Recording order.** Root first, tree second. The tree sum includes the root, so
reading it last keeps the pair consistent on a growing process: reading the root
last could report a figure larger than the tree it belongs to.

The callback runs inline on the reader goroutine, so its own cost delays the
next read. That is why the lag is recorded rather than assumed away.

**Limitations.**

- One reading per iteration, no polling: this is a snapshot. A transient spike
  that happens entirely between the marker and the query is invisible.
- `WorkingSetSize` on Windows is the platform's memory measure and is **not**
  interchangeable with Linux `VmRSS` or macOS resident size. Do not compare the
  three columns of [§10](#10-cross-platform-limitations) as if they were the
  same quantity.
- Readiness is defined by `first-useful-frame`, so a change to when that fires
  changes this metric's meaning. `definition_version` guards the benchmark
  definition, but not the marker placement inside `View()`.

### 5.3 Go heap (`mem.tui.go-heap`)

The Go runtime's own accounting, at the same `first-useful-frame` boundary.

| Metric | Meaning |
| --- | --- |
| `go_heap_alloc_bytes` | `runtime.MemStats.HeapAlloc` — bytes of allocated heap objects, **including** unreachable objects not yet freed |
| `go_sys_bytes` | `runtime.MemStats.Sys` — total memory obtained from the OS for the Go runtime |

**Why these are two metrics.** They answer different questions and are kept
apart on purpose. `HeapAlloc` is live-ish heap; `Sys` is what the runtime
obtained from the OS and rarely gives back. A program can hold a small
`HeapAlloc` and a large `Sys` (the runtime keeps the arena), and reporting only
one of them hides that.

**Why Go heap is not RSS.** `HeapAlloc` is a Go-level accounting of Go objects.
It excludes runtime overhead, stacks, and anything allocated outside the Go
heap, and it is not a statement about operating-system memory. HPOV never
compares `go_heap_alloc_bytes` against `ready_rss_bytes`, and the two carry
different `platform_semantics` tags for exactly that reason.

**Why `HeapInuse` is not silently substituted.** `HeapInuse` is *span* bytes —
address space the runtime has mapped for the heap, including free slots inside
those spans. It is a legitimate metric, but it is a different quantity. Swapping
it in would change what the number means without changing its name, which is
the failure mode HPOV is built to avoid. If `HeapInuse` is ever wanted, it must
be a new, separately named metric.

**Source.** The `alloc=` and `sys=` fields of the `ff-perf` marker line the
subject **already emits** at `first-frame` and `runtime-ready`
(`perfmark.EventMem`). No extra product instrumentation was added: the value was
already on the wire, so the derivation costs the harness nothing and cannot
perturb the measurement beyond the `ReadMemStats` stop-the-world pause that
emitting the marker already performed.

**Validity.** A marker line without memory fields yields **unavailable**
metrics with a reason — never zeros. The benchmark also rejects an implausible
reading (`HeapAlloc > Sys`), which would mean the fields were mixed up
upstream.

**Cross-platform.** `go:heapalloc` and `go:memstats_sys` on every platform. This
is the one memory metric in HPOV that is comparable across hosts for the same
build.

---

## 6. Comparison methodology

`hpov compare` and `hpov compare-live` both produce the same decision, from the
same rules, over the same verdicts. They differ in how the two sides are
obtained.

| | `hpov compare base.json cand.json` | `hpov compare-live --base … --head …` |
| --- | --- | --- |
| Inputs | two previously collected `hpov.result` documents | two binaries, run **now** |
| Statistics | unpaired bootstrap per metric | **paired** bootstrap on per-round differences |
| Machine | may differ (see `--allow-cross-host`) | identical by construction |
| Use for | a candidate checked against a stored baseline | CI-style A/B on one machine |

`compare-live` interleaves both subjects round-robin inside a single run on one
host, which is what makes machine drift cancel and the absolute thresholds
portable. Both subjects must run the same benchmark set, and each benchmark's
samples stay independently repeatable.

### 6.1 Compatibility checks (what must match)

A metric pair is comparable only when **all** of these hold:

| Check | Failure verdict |
| --- | --- |
| same `benchmark.id` | coverage entry (`removed` / `new`), never a silent pass |
| same `metric.name` | coverage entry (`missing` / `new`) — HPOV never compares different names |
| same `definition_version` | `incompatible` |
| identical `params` | `incompatible` (first mismatching key is named) |
| same `unit`, same `direction` | `incompatible` (each named separately) |
| same `platform_semantics` | `not_comparable` |
| same host OS and architecture | `not_comparable` |
| same subject OS/architecture and provenance (`release` vs `local-build`) | `not_comparable` |
| same host fingerprint | `not_comparable`, or informational with `--allow-cross-host` |
| `schema_version` MAJOR equal | refused at load time, with both versions named |

A benchmark present on one side only appears in the **`coverage`** section with a
reason: `unsupported`, `skipped`, `removed`, `new`, `missing` or `invalid`.
Coverage is how HPOV avoids a silent pass.

### 6.2 The practical threshold

```
effective = max(rel_thr × base_p50, abs_floor)
met       = |signed| ≥ effective
```

where `signed` is the direction-aware delta (positive always means worse, so
every threshold test reads the same way regardless of the metric's direction).

HPOV's built-in table — these are **HPOV's current engineering defaults**, not
scientifically universal constants, and not yet calibrated by an A/A campaign:

| Benchmark family | `rel_thr` | `abs_floor` |
| --- | ---: | ---: |
| `launch.*` `wall_ms` | 10% | 5 ms |
| `launch.artifact-size` | 2% | 64 KiB |
| `tui.*` marks/segments | 15% | 8 ms |
| `mcp.*` | 15% | 10 ms |
| `mem.*` peak/idle RSS | 5% | 1 MiB |
| `mem.tui.go-heap` | 3% | 256 KiB |
| `hot.*`, `*.micro` (ns/op) | 5% | none (use the CI) |
| `hot.session.*`, `hot.fs.*` | 15% | none (I/O-bound) |

The most specific glob wins, so `mem.tui.go-heap` (3%) is not swallowed by
`mem.*` (5%).

Supply your own table with `--thresholds FILE`. The file is
`{"version","source","thresholds":[{"glob","rel_pct","abs_floor","note"}]}`;
`source` is required so a verdict can be traced back to a table. The table's
`version` and `source` are recorded in every comparison document. A missing file
falls back to the built-in table; a malformed one is an error, because silently
comparing against thresholds nobody asked for is worse than refusing.

### 6.3 Statistical evidence

Both are required, on the **difference of medians**:

1. the **95% bootstrap CI** of the difference of medians excludes zero
   (5000 resamples, deterministic seed recorded in the run);
2. a **two-sided Mann-Whitney U** test with tie correction gives **p < 0.01**,
   which needs **n ≥ 8 per side**. Below that, no p-value is reported at all —
   the metric reads as `mann_whitney_needs_n>=8` rather than a weak number.

The reported effect estimate is the **Hodges-Lehmann shift** (median of pairwise
differences), which is reported whether or not a verdict follows.

### 6.4 Statistical significance is not practical significance

The two failures are deliberately distinguished:

- threshold met, evidence present → `regressed` or `improved`;
- threshold met, evidence absent → **`inconclusive`** ("beyond threshold but
  statistically unresolved"). A practically large but statistically unresolved
  movement is not a regression;
- evidence present, threshold not met → **`unchanged`** ("statistically
  significant but below practical threshold"). A statistically resolvable but
  practically irrelevant movement is not a regression either.

### 6.5 Holm correction

One comparison makes dozens of tests: a full standard run decides 33 metrics.
At a 1% per-test level that is roughly a 28% chance of at least one spurious
finding (`1 − 0.99³³`) even when nothing changed, so unadjusted results cannot
be read as a family.

Every test that ran is corrected with **Holm's step-down at α = 0.01**, without
assuming independence (these metrics are correlated). The step-down **stops at
the first failure**, so a family of marginal p-values fails together rather than
letting the largest one through on the full α.

A verdict that rested on the raw p-value is **withdrawn** to `inconclusive` if
its corrected p does not survive, with the reason naming both numbers:

```
not_significant_after_holm (raw p=0.008 vs adjusted 0.019)
```

Both the raw and the corrected p are stored in the comparison document, plus the
metric's rank in the family.

### 6.6 Verdicts

| Verdict | Meaning |
| --- | --- |
| `improved` | moved in the good direction, threshold met, evidence present, noise-clear |
| `regressed` | moved in the bad direction, same requirements |
| `unchanged` | below threshold, or the CI includes zero |
| `tail_regressed` / `tail_improved` | p50 unchanged while p95 moved past its threshold with a CI excluding zero |
| `inconclusive` | the data cannot support a verdict; the reason is recorded |
| `incompatible` | same name, different definition (unit, direction, params, `definition_version`) |
| `not_comparable` | different platform semantics, host, or subject OS/arch |
| `invalid` | samples failed the suite's own validity contract |
| `informational` | a directionless metric moved, or a verdict was degraded (cross-host, quick profile) |

`missing_base` and `missing_current` are defined verdict states for a metric
that exists on one side only, and they are reserved: v1 reports that situation
through the **`coverage`** section instead, because a coverage entry carries the
benchmark id, the metric name and the reason together. Read the coverage section
for absence; do not wait for those two verdicts to appear.

**Tail verdicts** apply the same evidence rule at p95 as at p50: the practical
bar is `max(rel_thr × base_p50, abs_floor)`, and the bootstrap CI of the p95
difference must exclude zero. A tail claim needs **n ≥ 20** per side; below that
the metric stays `unchanged` rather than implying a tail it cannot support.
The interval is the CI of `p95(candidate) - p95(baseline)` — the same quantity
the threshold is applied to ([§14.7](#147-tail-calibration-a-focused-experiment)
records a defect here that was fixed, and the residual sensitivity that remains).

The weakening gates in [§6.8](#68-quality-and-noise-handling) apply to tail
verdicts too, so a `tail_regressed` cannot escape a poor-quality run or a waived
cross-host comparison the way a median verdict cannot.

**Trade-offs** are annotated, never resolved. When metrics of one benchmark move
in opposite directions (latency improves while memory regresses), HPOV records a
`trade_offs` entry listing both sides, and appends a `trade_off:` note to the
regressed metric's reason. Both metrics keep their own verdict. There is no
mechanism that picks a winner, because any such mechanism would be a weighting
HPOV refuses to invent.

### 6.7 Subject and build constraints

- A **release** binary measured against a **local build** (or vice versa) is
  `not_comparable`: the difference cannot be separated from provenance.
- A toolchain difference (`go_version`, and any recorded `ldflags`, `cgo`,
  `trimpath`) does not block the comparison but marks the verdict
  **`confounded_build`** and appends the warning to the verdict reason, because
  a code change and a Go upgrade cannot be told apart.
- `--allow-cross-host` waives a **host fingerprint** difference only. It does not
  waive an OS or architecture difference: those measure different quantities,
  not the same quantity elsewhere. With the flag, every verdict becomes
  `informational`, because absolute values on another machine are not the same
  numbers.

### 6.8 Quality and noise handling

- A **poor** run on either side downgrades `regressed`/`improved` **and**
  `tail_regressed`/`tail_improved` to `inconclusive` with reason
  `poor_run_quality`. Degraded runs keep their verdicts; being inconvenient is
  not a reason to discard data.
- If a metric is flagged `noisy:`, `bimodal:` or `drift:` on **either** side, the
  required effect doubles: `|signed| ≥ 2 × threshold`, otherwise the verdict is
  `inconclusive` (`noisy_effect_below_2x_threshold`). A noisy metric therefore
  never reaches the tail path at all: every effect small enough for a tail claim
  is smaller than the doubled bar, so the movement cannot be re-reported as a
  tail to get around the rule.
- Noise flags are matched **per metric name**, so a noisy `wall_ms` does not
  double the bar for the same benchmark's memory metric.
- The **quick** profile is directional only: its plans are too small to support
  the evidence rule, so its verdicts — median or tail — are reported as
  `informational` with `quick_profile_directional_only`, and the document
  records `method.directional_only: true`.
- `--allow-cross-host` degrades **every** verdict, tail included, to
  `informational`, which is what makes `--fail-on-regression` unable to exit 3
  on a cross-host comparison.

### 6.9 What a comparison document contains

`hpov.compare` is a versioned document, not a dump. It records:

- `inputs`: both sides' role, path, run id, suite version, profile, quality,
  host id, OS/arch, and whether both sides came from one interleaved file;
- `method`: family α, that Holm was applied, both required statistical tests by
  name, the effect estimator, bootstrap resamples and seed, the noise
  multiplier, the threshold table's `version` and `source`, and
  `directional_only`;
- per metric: both p-values, the Holm rank and outcome, the CI, the
  Hodges-Lehmann shift, the threshold used, the effective threshold, the quality
  of each side, noise flags, `confounded_build`, and the verdict with its reason;
- `coverage`, `trade_offs`, `warnings`, and a `summary` that counts verdicts by
  name.

`hpov compare --out comparison.json` writes it; `hpov compare-live
--comparison-out comparison.json` writes the paired form. Reading one back
(`compare.Read`, and the schema file) refuses a document whose `schema`,
major `schema_version` or `method_version` differs, because a verdict whose
decision rules have moved must not be read as if it were current.

Exit codes: **0** always, unless you pass `--fail-on-regression`, in which case
any `regressed` or `tail_regressed` exits **3**. Unreadable or invalid inputs
exit **2**. `inconclusive`, `not_comparable` and `informational` never fail the
gate — folding them into a failure would make a missing benchmark
indistinguishable from a slow one.

---

## 7. CLI usage

Build the runner:

```bash
go build -o bin/hpov ./bench/hpov
```

`hpov` is a separate binary from `ff`. It takes the subject binary as an
argument; it never hard-codes a path to `ff`.

### 7.1 Commands

```
hpov list [--tier N] [--kind e2e|micro|static] [--here] [--long]
hpov env  [--json]
hpov run  --ff label=path [--ff …] [--select GLOB,…] [--exclude GLOB,…]
          [--tier N] [--kind K] [--profile quick|standard|full]
          [--n N] [--warmup N] [--seed S] [--source release|local-build]
          [--fail-fast] [--require-quality good] [--workroot DIR] --out result.json
hpov show [--metric GLOB] result.json
hpov validate result.json
hpov compare <baseline.json> <candidate.json> [--thresholds FILE]
             [--fail-on-regression] [--allow-cross-host] [--explain]
             [--out comparison.json]
hpov compare-live --base label=path --head label=path [--select GLOB,…]
             [--tier N] [--profile P] [--n N] [--warmup N] [--seed S]
             [--thresholds FILE] [--fail-on-regression] [--explain]
             --out result.json [--comparison-out comparison.json]
hpov calibrate --ff label=path [--repeats N] [--select GLOB,…] [--tier N]
             [--profile P] [--n N] [--warmup N] [--seed S]
             [--thresholds FILE] [--out DIR] [--report calibration.json]
```

### 7.2 `hpov list`

Lists registered benchmarks with tier, kind and runnability. `--here` filters
to what this host can actually run (ConPTY/pty and memory availability are
checked, not assumed). `--long` prints each benchmark's purpose text — the
authoritative one-line statement of what it measures.

```bash
hpov list --here --long
```

### 7.3 `hpov env`

Reports the host signals that a comparison will later check: OS/arch, env
label, logical CPUs, clock source and measured resolution, idle CPU percentage,
power source and plan, filesystem kind of the workroot, and `git`/`rg`/`go`
versions. `--json` emits the same as the `host` object of a result document.

Use it before a serious run: if idle CPU or the clock resolution looks wrong,
the run is not going to be worth much.

### 7.4 `hpov run`

Runs the selected benchmarks against one or more subjects.

- `--ff label=path` (repeatable) — the binary under measurement. Each subject
  is probed for SHA-256, size, Go build info and `--version` before any
  iteration, and that provenance is stored per run.
- `--select` / `--exclude` — dotted ID globs; `launch.*` matches the subtree.
- `--profile quick|standard|full` — iteration plans (see
  [§3.10](#310-iteration-plans-at-a-glance)).
- `--n`, `--warmup` — override the measured and warm-up counts.
- `--seed` — seeds the interleave order and the bootstrap (default 424242).
- `--source release|local-build` — provenance label recorded per subject.
- `--require-quality good` — exit **4** when the run quality is poor.
- `--fail-fast` — stop after the first errored/invalid benchmark.
- `--workroot DIR` — where fixtures are created (default: OS temp dir).
- `--out` — required. A streaming `<out>.partial.jsonl` progress log is written
  alongside, and the canonical result is written atomically at the end.

The human report is printed to stdout when the run completes, followed by the
path written.

### 7.5 `hpov show` / `hpov validate`

`hpov show` re-renders a result document (validating it first); `--metric GLOB`
restricts it to matching metrics. `hpov validate` recomputes every metric's
statistics from its raw samples and prints any problems; a non-empty problem list
exits **2**.

Both are how you check a result before trusting it.

### 7.6 Minimal workflow

```bash
# 1. Build the subject.
go build -o bin/ff .

# 2. Build the runner.
go build -o bin/hpov ./bench/hpov

# 3. Check the host is quiet enough to measure on.
./bin/hpov env

# 4. See what will run.
./bin/hpov list --here

# 5. Collect a baseline.
./bin/hpov run --ff base=./bin/ff \
  --select "launch.*,mem.*" \
  --profile standard \
  --out results/baseline.json

# 6. Validate it.
./bin/hpov validate results/baseline.json

# 7. Collect a candidate (after your change).
./bin/hpov run --ff head=./bin/ff \
  --select "launch.*,mem.*" \
  --profile standard \
  --out results/candidate.json

# 8. Compare.
./bin/hpov compare results/baseline.json results/candidate.json \
  --out results/comparison.json

# Optionally: fail the shell on a regression.
./bin/hpov compare results/baseline.json results/candidate.json --fail-on-regression
```

For a same-machine A/B, prefer the paired form — it removes machine drift:

```bash
./bin/hpov compare-live --base base=./bin/ff-base --head head=./bin/ff-head \
  --select "launch.*" --profile standard \
  --out results/live.json --comparison-out results/live-compare.json
```

### 7.7 Exit codes

| Code | Meaning |
| ---: | --- |
| 0 | completed (skips allowed); for `compare`, no gating regression |
| 1 | usage error |
| 2 | at least one benchmark errored/invalid, or an unreadable/invalid input document |
| 3 | regression, only with `--fail-on-regression` |
| 4 | run quality is poor and `--require-quality good` was passed |

---

## 8. Result format

Two versioned document schemas, both strict JSON, both shipped with the tool:

| Schema | Version | JSON Schema | Produced by |
| --- | --- | --- | --- |
| `hpov.result` | `1.0.0` | [`bench/hpov/schema/hpov-result.v1.schema.json`](../bench/hpov/schema/hpov-result.v1.schema.json) | `hpov run`, `hpov compare-live` |
| `hpov.compare` | `1.0.0` | [`bench/hpov/schema/hpov-compare.v1.schema.json`](../bench/hpov/schema/hpov-compare.v1.schema.json) | `hpov compare`, `hpov compare-live` |
| `hpov.calibration` | `1.0.0` | [`bench/hpov/schema/hpov-calibration.v1.schema.json`](../bench/hpov/schema/hpov-calibration.v1.schema.json) | `hpov calibrate` ([§14](#14-aa-calibration-campaign)) |

`schema_version` follows semver: MAJOR for removed or repurposed fields,
MINOR for additive changes, PATCH for clarifications. Readers must ignore
unknown fields, so a MINOR addition does not break an older reader. A comparison
refuses documents whose MAJOR differs, since that means the documents do not
describe the same thing.

### 8.1 `hpov.result`

| Section | Contents |
| --- | --- |
| `suite` | name, tool version, profile, `definition_set` |
| `run` | id, timestamps, command line, seed, quantile method, bootstrap config, quality label + flags |
| `subjects[]` | per binary: label, product, version string, git commit + dirty flag, and `binary` (basename, sha256, size_bytes, source, go_version, goos, goarch) |
| `host` | salted host fingerprint, os/arch, env label, CPU model/topology, memory, GOMAXPROCS, power, clock source + resolution, fs kind, tool versions |
| `environment` | home isolation, stdin, marker policy, env allowlist + overrides, spawn-floor calibration, environment quality |
| `benchmarks[]` | per (benchmark, subject): id, `definition_version`, tier, kind, status, params, resolved plan, validity predicate + probes, every iteration, every metric, flags, warnings, error |
| `warnings[]` | run-level warnings |

### 8.2 Raw samples, and why they are retained

Each metric keeps `values` — every stored sample, in iteration order — next to
`statistics`. Each iteration keeps `i`, `phase`, `valid`, `t_offset_ms`, `attrs`
(per-iteration facts such as `exit_code`, `sha256`, `quit_method`,
`sample_lag_ns`, `missing_marks`) and `values` (the per-metric reading).

Retaining them is what makes the following possible, none of which HPOV would
let you do from summary numbers alone:

- **Recomputation.** `hpov validate` recomputes every statistic from the raw
  samples and refuses a document whose stored numbers disagree. A result is
  self-checking.
- **Re-analysis under new statistics.** A new effect estimator or a new noise
  detector can be applied to an existing run without re-measuring. The
  bootstrap seed is recorded so intervals are reproducible.
- **Auditing a verdict.** Given a `regressed`, the reader can inspect the
  samples that produced it instead of trusting a number.
- **Explaining an invalid sample.** `valid: false` plus the reason says *which*
  iteration went wrong and why, rather than leaving a hole in a distribution.

Warm-up iterations are stored too, with `phase: "warmup"`, and never enter
statistics. That is deliberate: a reader can check that the warm-up did its job.

### 8.3 Unavailable data in the document

- `statistics.nulls` records, per statistic, why it is `null` — e.g.
  `p95: insufficient_n(<20)`. A percentile that could not be estimated is
  reported as a reason, never as `0`.
- An unavailable metric keeps its aligned `values` vector (placeholders keep the
  indices aligned) and records the reason on the iteration as
  `unavailable.<metric>`. Statistics are computed only from usable samples.
- `hpov compare` rebuilds each metric's usable sample list from the iteration
  records when reading a document, so the exclusion rule survives round-tripping
  through JSON.

### 8.4 Statuses and errors

`ok`, `skipped`, `unsupported`, `invalid`, `error`, with `status_detail` and a
structured `error` (`code`, `phase`, `message`). Error codes: `unsupported_platform`,
`requirement_unmet`, `setup_failed`, `spawn_failed`, `timeout`,
`invalid_workload`, `marker_protocol_error`, `internal`.

### 8.5 `hpov.compare`

Described in [§6.9](#69-what-a-comparison-document-contains). It is a document
in its own right: reading it back refuses a foreign `schema`, a different major
`schema_version`, or a different `method_version`, because a verdict whose
decision rules have moved must not be read as current.

---

## 9. Reproducibility: what makes a result trustworthy

A result can be **schema-valid and still unsuitable** for a strong regression
conclusion. Validity is a property of the document; trustworthiness is a
property of the run. HPOV records the signals; you have to read them.

### 9.1 Before you run

- **Use a quiet machine.** Close editors, IDEs, browsers, containers, build
  daemons and sync clients. HPOV measures idle CPU for 3 s and flags
  `high_idle_cpu` above 10%, but a flag is not a fix.
- **Stable power configuration.** On a laptop, plug in and select the
  performance power plan; a governor change mid-run moves every latency number.
  `host.power.source` and `plan` are recorded so a bad configuration is visible.
- **Avoid concurrent builds and test runs.** Go's own compile and test
  processes saturate cores and steal the file cache.
- **Run interactively, not over a shared VM.** A contended host inflates tails
  and adds variance that no sample count fixes.

### 9.2 During the run

- **Check the quality label and flags first.** `good` / `degraded` / `poor` with
  flags such as `high_idle_cpu`, `spawn_floor_drift`, `many_noisy_benchmarks`.
  A `poor` run is still a valid document; it just cannot support a regression
  claim, and `hpov compare` will refuse to assert one.
- **Read the spawn-floor calibration.** `environment.calibration.spawn_floor_ms`
  gives the p50 at the start and end of the run. A large gap means the machine
  changed underneath you.
- **Check per-benchmark flags.** `noisy:`, `bimodal:`, `drift:` on a metric mean
  its distribution was not trustworthy enough for a normal verdict.

### 9.3 After the run

- **Validate the document.** `hpov validate results/baseline.json` recomputes
  every statistic from the raw samples. A document that fails validation cannot
  be compared: `hpov compare` refuses it.
- **Inspect valid vs total counts.** `n/valid` on every metric line tells you
  how much data survived. `valid_n` far below `n` means the workload was flaky
  and the distribution describes only the lucky iterations.
- **Know your clock.** `host.clock.resolution_ns` bounds what any ms metric can
  resolve. Sub-millisecond differences in TUI phase metrics are not signal.

### 9.4 Choosing a profile

| Profile | When |
| --- | --- |
| `quick` | smoke-testing the harness; directional only — its verdicts are reported as informational |
| `standard` (default) | **serious comparisons**; n=30 measured for latency, n=15–20 for memory and TUI |
| `full` | when a small effect matters and you need n=100; slowest |

Two subjects measured in the same run are interleaved round-robin with a seeded
per-round order, so drift affects both equally.

### 9.5 A minimum standard for a regression claim

1. `hpov env` showed a quiet machine, and the run's quality is `good`.
2. `hpov validate` passes.
3. Baseline and candidate share a host fingerprint (or you passed
   `--allow-cross-host` and read the verdicts as informational).
4. Same suite version, same profile, same `definition_version`.
5. `--profile standard` or `full`.
6. The verdict is `regressed` (not `inconclusive`), its CI excludes zero, its
   Holm-corrected p survives, and neither side carries a noise flag for that
   metric.

If any of those fails, the honest statement is "no conclusion", not "no
regression".

---

## 10. Cross-platform limitations

**HPOV does not claim equivalence where it does not have it.** The table below
is the current implementation on Windows, Linux and macOS.

| Aspect | Windows | Linux | macOS | Comparable across OS? |
| --- | --- | --- | --- | --- |
| **Root peak memory** | `PeakWorkingSetSize` (peak **working set**: includes shared + mapped pages) | `VmHWM` (peak RSS) | `rusage.Maxrss` (peak RSS, bytes) | **No** — different quantities. Within one OS only. |
| **Current memory** | `WorkingSetSize` | `/proc/<pid>/statm` RSS | `task_info` resident size | **No** — same reason. |
| **Peak availability** | exact, via a retained query handle readable after exit | exact, from `/proc` before exit or `wait4` | exact, from `wait4` rusage | Mechanism yes; values no. |
| **Process-tree sampling** | Toolhelp32 process enumeration, working set summed | `/proc` walk, `VmRSS` summed | process enumeration, resident size summed | Mechanism yes; semantics no. |
| **Tree sampling cost** | enumeration is the expensive part (largest recorded lag) | `/proc` walk | enumeration | — |
| **Tree figure** | lower bound | lower bound | lower bound | Comparable **as lower bounds within one OS**; never across. |
| **PTY** | ConPTY (`CreatePseudoConsole`), 120×40 | `/dev/ptmx` + `openpty` | `/dev/ptmx` + `openpty` (no `TIOCSPTLCK` pair; name from `TIOCPTYGNAME`) | Mechanism no; absolute TUI values no. |
| **Marker transport** | dedicated stderr pipe, console drained separately (ConPTY merges console + stderr, which breaks marker ordering) | dedicated stderr pipe | dedicated stderr pipe | Yes, markers are the same stream either way. |
| **Clock** | Go monotonic `time.Now`; resolution measured per run (~1 ms on the dev host) | same | same | Yes, but resolution bounds every ms metric. |
| **Process creation cost** | highest of the three | lowest | middle | **No.** Absolute wall time for the same work differs by ~2–3×. Normalized? No. |
| **Go heap metrics** | `go:heapalloc`, `go:memstats_sys` | same | same | **Yes**, Go-level and OS-independent, for the same build. |
| **Executable size** | `os.Stat` | `os.Stat` | `os.Stat` | Only for the same (os, arch) target. |

### 10.1 The Windows working-set caveat, stated once

`WorkingSetSize` and `PeakWorkingSetSize` are **the platform's memory
measure**. They are not RSS. A Windows working set includes pages shared with
other processes and mapped file pages that a Linux `VmRSS` would not count. A
Windows memory number must never be placed in a table next to a Linux or macOS
number and compared as though the columns meant the same thing. That is why
every memory metric carries `platform_semantics`, why `hpov compare` refuses
cross-platform pairs outright, and why the tags name the exact source.

### 10.2 Other standing limitations

- **No CI gating in v1**, so there is no tracked regression history yet.
- **Thresholds are uncalibrated defaults.** They encode engineering judgement,
  not an A/A false-positive study. `--thresholds` exists so a calibrated table
  can replace them without touching the engine.
- **No `accepted_changes` allowlist.** An expected behavioral change currently
  has to be expressed by bumping `definition_version` (which makes the pair
  `incompatible` and reported in coverage) and reading the coverage section.
- **Micro and I/O families have no benchmarks yet.** `hot.*` and `*.micro`
  thresholds are defined in the table for completeness; no such benchmark is
  implemented.
- **`mem.tui.ready-rss` is a snapshot**, so a transient spike between the
  readiness marker and the query is invisible.
- **Build-flag provenance is partially recorded.** `ldflags`, `cgo` and
  `trimpath` are compared when present, but HPOV currently reads only
  `go_version`, `goos`, `goarch` and VCS info from the subject's embedded build
  info (see [§13](#13-known-deviations-from-the-design-plan)).

---

## 11. What HPOV does not claim

Explicit non-claims, so nobody has to infer them:

1. **HPOV is not a public or industry-standard benchmark.** There is no
   leaderboard, no published cross-vendor ranking, and no neutral third-party
   result. It is a tool for measuring *this* codebase on *your* hardware.
2. **Cross-machine numbers are not directly comparable.** Two results from
   different hosts measure the same quantity under different conditions. Use
   `compare-live` on one machine, or `--allow-cross-host` and read the verdicts
   as informational.
3. **Cross-OS memory numbers are not comparable.** Different OS accounting, as
   detailed above.
4. **No aggregate score exists**, and none will be added. Counts of verdicts are
   not a score.
5. **No metric is a "gating metric" in v1.** Nothing is selected, and nothing
   fails a build unless you pass `--fail-on-regression` yourself.
6. **A `good` quality label does not mean the result is trustworthy.** It means
   no detector fired. Human judgement is still required.
7. **`unchanged` does not mean "no change".** It means the change did not clear
   the threshold, or the CI included zero. Read the numbers, not just the state.
8. **HPOV does not measure model quality, agent correctness, cost, or
   usability.** It measures launch, startup and memory of the `ff` binary.
9. **Marker timestamps are not infinitely precise** and are not the child's own
   clock. See [§4.4](#44-timing-source-the-reader-clock).
10. **"First run" means a fresh home, never a cold OS cache.**

---

## 12. Developer quickstart

Five steps, from a clean checkout to a comparison.

```bash
# 1. Build the subject under test.
go build -o bin/ff .

# 2. Build the benchmark runner.
go build -o bin/hpov ./bench/hpov

# 3. See what exists on this host.
./bin/hpov list --here

# 4. Run one benchmark and validate the result.
./bin/hpov run --ff head=./bin/ff --select launch.version \
  --profile quick --out results/smoke.json
./bin/hpov validate results/smoke.json

# 5. Compare two results.
./bin/hpov compare results/baseline.json results/smoke.json
```

For anything you intend to act on, use `--profile standard` and read
[§9](#9-reproducibility-what-makes-a-result-trustworthy) first.

Where things live:

| Path | What it is |
| --- | --- |
| `bench/hpov/main.go` | the CLI |
| `bench/hpov/schema/*.json` | JSON Schemas for both documents |
| `internal/hpov/bench/` | benchmark contracts: `Spec`, plans, fixtures, validity |
| `internal/hpov/suites/` | the implemented benchmarks (`launch.go`, `tui.go`, `mem.go`) |
| `internal/hpov/runner/` | run orchestration, quality assessment, statistics collection |
| `internal/hpov/compare/` | comparison engine, thresholds, verdicts |
| `internal/hpov/spawn/`, `pty/`, `collect/` | process spawn, pty/ConPTY, memory collection |
| `internal/hpov/markers/` | marker parsing and timeline building |
| `internal/hpov/schema/` | document types and validation |
| `internal/perfmark/` | the product-side marker emitters (env-gated) |

Adding a benchmark means adding a `Spec` and an implementation in
`internal/hpov/suites`, then registering the constructor in `suites.All()`. There
are no `init()` side effects and no global registry.

---

## 13. Known deviations from the design plan

HPOV was implemented against `HPOV-Benchmark-Design-Plan.md`. Where v1 differs,
the **implementation** is what is documented above. The differences worth
knowing:

| Plan element | v1 implementation | Effect |
| --- | --- | --- |
| Store thresholds in `bench/hpov/thresholds.json` | thresholds are a built-in table in code, overridable with `--thresholds FILE` | same rules; the built-in table is not a checked-in data file yet |
| Compare `subject.binary.ldflags`, `cgo`, `trimpath` for `confounded_build` | `subject.Probe` reads `go_version`, `goos`, `goarch` and VCS info from the subject's embedded build info; `ldflags`/`cgo`/`trimpath` are schema fields that HPOV does not currently populate | a toolchain difference is still detected via `go_version`; a pure ldflags change is not |
| `definitions.json` `compat` entries for equivalent definition changes | not implemented; a `definition_version` difference is `incompatible` and appears in coverage | an intended-but-equivalent change must be read from coverage, not auto-accepted |
| `accepted.json` accepted-change allowlist | not implemented | an expected regression is still reported; suppress it by bumping `definition_version` or by reading the comparison yourself |
| `--require ID,…` turns a named missing benchmark into a failure | not implemented; coverage always reports missing benchmarks, never fails | missing benchmarks are visible but not gateable in v1 |
| Select ≈6 gating metrics; A/A-calibrate thresholds | A/A measurement tooling exists (`hpov calibrate`, [§14](#14-aa-calibration-campaign)) and one 20-trial campaign has been run on the development host; no threshold was changed and no gating set exists | thresholds remain uncalibrated engineering defaults ([§10.2](#102-other-standing-limitations), [§14.5](#145-what-the-first-campaign-did-and-did-not-establish)) |
| CI integration | not implemented; no workflow runs HPOV | no automatic regression history |
| `mcp.*`, `hot.*`, `*.micro` benchmark families | thresholds defined; no benchmarks implemented | those rows of the threshold table are unused |
| Spawn-floor calibration subtracted from results | never subtracted; start/end drift is reported as a quality signal | numbers include the harness floor, honestly, instead of a corrected figure |
| Noise doubling checked at p95 as well as p50 | checked at p50 only | no behaviour difference in practice: a noisy metric is `inconclusive` before the tail path is reached, because every effect small enough for a tail claim is below the doubled bar |

Nothing in this table changes a measured value. Each row is either a feature
that was deliberately deferred out of v1 scope, a documented limitation of what
provenance HPOV records today, or an equivalent-by-construction shortcut.

### 13.1 Correctness fixes made after the v1 features landed

Two defects were found while writing this document, in
`internal/hpov/compare/verdict.go`. Both were fixed with regression tests in
this commit:

| Defect | Consequence | Fix |
| --- | --- | --- |
| The poor-quality, quick-profile and `--allow-cross-host` gates were applied only to `regressed`/`improved`, before tail verdicts existed in that code path | A `tail_regressed` could be reported from a `poor` run, and `--allow-cross-host` — documented as degrading *every* verdict — did not degrade tail verdicts, so `--fail-on-regression` could exit 3 on a cross-host comparison | The gates moved into one shared `degradeVerdict` applied to median and tail paths alike |
| The noise-doubling rule appeared not to cover p95 | no behaviour difference: a noisy metric is already `inconclusive` before the tail path is reached | none needed; the interaction is now stated in the code and covered by `TestNoisyMetricCannotEscapeThroughTheTailPath` |
| The paired bootstrap of a *percentile* difference resampled the per-round differences and took their percentile, instead of differencing the two percentiles ([§14.7](#147-tail-calibration-a-focused-experiment)) | The tail interval estimated a different quantity from the one the threshold was applied to. On interleaved data at n = 30 the two can have opposite signs, so the interval excluded zero in **every** eligible A/A decision and tail claims were made on movements the interval never covered | `DifferencePercentileCI` now resamples rounds jointly and differences the percentiles inside each resample, so the interval brackets the tested quantity. `DifferenceCI50` keeps its paired estimator, which is correct for a location shift. Pinned by `TestDifferencePercentileCIPairedEstimatesThePercentileDifference` and `TestDifferenceCI50PairedStillUsesPairDifferences` |

---

## 14. A/A calibration campaign

### 14.1 What an A/A campaign is for

An A/A campaign measures the same binary against itself, repeatedly, and asks
one question: **how often does the comparison make a claim when there is
nothing to claim?**

Any verdict the comparison emits on identical inputs is, by construction, a
false claim. That makes the A/A behaviour the first thing to measure before
anyone trusts a regression gate, and the campaign exists to measure it, not to
quieten it. It does not change thresholds, does not select gating metrics, and
never fails a build.

The comparison logic is not involved. Every trial is an ordinary interleaved
run of the one binary under two labels (`aa-base`, `aa-head`), followed by the
ordinary paired comparison of those two subjects, with the same thresholds,
quality gates, noise handling and Holm correction a real A/B would use.

### 14.2 Running it

```bash
make build hpov

./bin/hpov calibrate \
  --ff aa=./bin/ff \
  --select "launch.*,mem.headless.peak-rss" \
  --profile standard \
  --repeats 20 \
  --out results/aa-campaign
```

| Flag | Meaning |
| --- | --- |
| `--ff aa=PATH` | the single binary measured as both subjects (exactly one) |
| `--repeats N` | A/A trials to run (default 10) |
| `--select`, `--exclude`, `--tier` | the usual benchmark selection |
| `--profile quick\|standard\|full` | iteration plan per trial |
| `--n`, `--warmup` | override measured and warm-up iterations |
| `--seed S` | base seed; trial *i* uses `S+i` |
| `--thresholds FILE` | the table every trial decides against |
| `--out DIR` | output directory (default `hpov-calibration`) |
| `--workroot DIR` | fixture root (default OS temp) |
| `--report FILE` | re-summarize a stored `calibration.json` and exit; measures nothing |

Trial *i* uses `seed+i` so each trial's interleaving order is reproducible
while the campaign as a whole samples more than one order. Use `--profile
standard` for anything you intend to act on: the quick profile is directional
only and would report every trial as `informational`.

Re-derive the report from stored evidence at any time, without re-measuring:

```bash
./bin/hpov calibrate --report results/aa-campaign/calibration.json
```

### 14.3 What a campaign writes

| File | Contents |
| --- | --- |
| `trial-NN.json` | the trial's `hpov.result` — raw samples, statistics, quality, provenance |
| `trial-NN-compare.json` | the trial's `hpov.compare` — per-metric verdicts with their evidence |
| `calibration.json` | the campaign document: `hpov.calibration` v1 |

The per-trial files are the existing formats and remain the source of truth.
`calibration.json` keeps every trial's every metric decision with the numbers
that produced it — sample counts, centres, p95s, observed deltas, both
intervals, both p-values, thresholds, quality, noise flags and the verdict
reason — so the roll-up can be recomputed and every claim checked against the
evidence behind it. JSON Schema:
[`bench/hpov/schema/hpov-calibration.v1.schema.json`](../bench/hpov/schema/hpov-calibration.v1.schema.json).

The A/A identity is verified, not assumed: every trial re-probes both subjects
and records their content hashes, and the campaign **fails** if a trial ever
saw two different hashes. A campaign whose two sides were not the same build
would be measuring something else, and its false-positive rate would be
meaningless.

### 14.4 How to read the numbers

The campaign counts verdicts and never scores. The accounting rules matter more
than the headline figure:

| Term | Meaning |
| --- | --- |
| **decision** | one metric's verdict in one trial |
| **quality-rejected decision** | a decision from a trial whose run quality is `poor`. The comparison withholds judgment on such data, so it can be neither a false positive nor a true negative. Counted and shown, excluded from the denominator. |
| **withheld** | a decision whose verdict is `inconclusive`, `invalid`, `not_comparable`, `incompatible` or `informational`. Nothing was claimed, so it is not a successful non-regression either. |
| **eligible decision** | everything else — a decision the comparison actually weighed and was free to call a regression. This is the denominator. |
| **A/A regression** | `regressed` or `tail_regressed` on an eligible decision |
| **A/A improvement** | `improved` or `tail_improved` on an eligible decision — the same defect in the other direction, reported for the same reason |

`decisions = eligible + quality-rejected + withheld`, and the campaign's own
tests assert that identity.

Improvements are counted alongside regressions on purpose. Reporting only the
regression direction would hide half of the engine's tendency to assert
something, and on the development host the first campaign's only claims were
improvements (see [§14.5](#145-what-the-first-campaign-did-and-did-not-establish)).

`rate` is omitted entirely when the denominator is zero. A rate over a handful
of decisions would read as precision the evidence cannot carry.

### 14.5 What the first campaign did and did not establish

One campaign was run on the development host (AMD Ryzen 7 7435HS, Windows 11,
16 logical CPUs, ~1 ms clock, AC power, `ff` built from the working tree) with
`--select "launch.*,mem.headless.peak-rss" --profile standard --repeats 20`,
giving 20 trials × 11 metrics = **220 decisions**, of which **93 were
eligible**.

**It did not establish a false-positive rate.** It established these
observations:

| Question | Observation |
| --- | --- |
| Any A/A regressions? | **0** of 93 eligible decisions (0 `regressed`, 0 `tail_regressed`) |
| Any A/A improvement claims? | **2**, both `tail_improved` — 2 of 93 eligible decisions |
| How much do metrics naturally vary? | median observed movement 0.27–0.79% of the baseline centre; worst single observation 1.85%, against practical thresholds of 10% (`launch.*`) and 5% (`mem.*`) |
| Does the practical threshold suppress normal noise? | Yes, decisively on this host: `threshold_met` was 0 in every eligible decision. Observed wobble was 5–25× below the bar |
| Does statistical significance misfire on identical subjects? | Barely on the median: the median CI excluded zero in 3 of 220 decisions and none produced a verdict. On the **p95 path** it fired often — 74 decisions carried tail evidence excluding zero |
| Which metrics are frequently withheld? | every `cpu_ms` metric: **0 eligible decisions out of 20 trials**, withheld every time by `noisy_effect_below_2x_threshold`. Windows process CPU time quantises, so the distribution is persistently bimodal and the noise rule fires every time. `cpu_ms` is not usable as a gating metric on this host as written |
| Do quality gates work? | Yes. 5 of 20 trials were `poor`, contributing 55 decisions that were excluded from the denominator rather than scored. None of those trials produced a verdict |
| Does Holm behave as intended? | No test survived correction in any eligible decision, and none needed withdrawing: no raw p-value reached α, so the correction had nothing to remove on this campaign |
| Do tail verdicts stay safe? | **This is where the one finding is.** Both A/A claims came from the tail path, on `launch.headless-init.first-run` and `launch.headless-init.steady` `wall_ms`. In each case p50 was unchanged, p95 moved 5–8 ms, and that cleared the 5 ms **absolute floor** because `max(10% × ~45 ms, 5 ms) = 5 ms` — the floor binds, not the percentage. The p95 bootstrap CI excluded zero, so the verdict was emitted exactly as [§6.6](#66-verdicts) specifies. The tail path has no p-value gate and no Holm correction: only the threshold plus the p95 interval |

The tail finding is a documented sensitivity observation, **not** a bug and
**not** something this milestone changed. What it suggests is that the tail path
is the loosest path in the comparison and the one most worth measuring again: a
candidate change would be to require a larger n for tail claims, or to require
the median evidence to agree. That decision needs its own campaign; changing a
threshold on one 20-trial run would be exactly the over-fitting this milestone
exists to avoid.

### 14.6 Interpreting a result you did not expect

- **A regression on identical subjects** is a genuine false positive. Check the
  trial's `quality_rejected`, its quality label, and that the two subject hashes
  matched, before counting it.
- **All-inconclusive** means the machine was too noisy or the samples too few
  for the comparison to decide. That is the quality gate working, and the
  campaign records it rather than hiding it.
- **One campaign is not a rate.** The plan's own target is ≤ 1% false positives
  over ≥ 100 independent A/A comparisons per benchmark per host class. One
  campaign of 20 trials over 11 metrics on one host is a first look.
- **Thresholds stay as they are.** Nothing in this milestone recalibrates a
  threshold, and the campaign document says so in its `conclusion` field so a
  copied-out report keeps the caveat.

### 14.7 Tail calibration: a focused experiment

§14.5 found that every A/A claim came from the tail path. That was followed up
with a focused experiment on the two benchmarks that produced it, plus one
control from the same threshold family:

```bash
./bin/hpov calibrate \
  --ff aa=./bin/ff \
  --select "launch.headless-init.first-run,launch.headless-init.steady,launch.version" \
  --profile standard \
  --seed 424320 --repeats 100 --out results/tail-campaign
```

`launch.version` is included only as a control: same `launch.*` threshold
(10% / 5 ms), same n, much cheaper, and it exercises the median path rather
than the tail path.

#### What was collected

| Run | Trials | Seeds | Eligible `wall_ms` decisions | p95 evidence fired | Tail verdicts |
| --- | ---: | --- | ---: | ---: | ---: |
| 1 (pre-fix) | 78 | 424242–424319 | 130 | **130 (100%)** | 18 |
| 2 (pre-fix) | 100 | 424320–424419 | 138 | **138 (100%)** | 5 |
| 3 (post-fix) | 60 | 424320–424379 | 78 | **7 (9%)** | 4 |

Runs 1 and 2 pre-fix combined: **178 trials, 268 eligible decisions, 23 tail
verdicts** (15 `tail_improved`, 8 `tail_regressed`), 0 ordinary verdicts.

Run 3 repeats seeds 424320–424379 — the first 60 trials of run 2 — with the
same binary, so it is a controlled before/after on the comparison code alone:
**68 eligible → 78 eligible, p95 evidence 68/68 → 7/78, tail verdicts 3 → 4.**

#### The experiment exposed an implementation bug

Every pre-fix eligible decision carried "p95 evidence", which is not how a
bootstrap interval behaves on data with no real difference. Inspecting one
claim in the stored documents showed why:

```
p95 difference  (cur - base)          = -5.08 ms     <- what the verdict tested
p95 of per-round differences (cur_i - base_i) = +2.60 ms
stored p95 CI                            = [+1.60, +4.83]   <- brackets the other one
verdict: tail_improved, claiming a 5.08 ms tail improvement
```

`DifferencePercentileCI` inherited `DifferenceCI50`'s paired estimator, which
resamples the *per-round differences* and takes their percentile. For a median
that is correct — the median of `cur_i - base_i` and the difference of the
medians are the same quantity — but it is a **different statistic** for a
tail: "the 95th percentile of the differences" is not "the difference of the
95th percentiles", and at n = 30 (where nearest-rank p95 is the *2nd largest*
of 30 samples) the two can have opposite signs. The verdict therefore compared
|p95 difference| against a threshold while testing an interval that never
covered that quantity.

Fixed in `internal/hpov/stats`: the paired percentile branch now resamples
rounds jointly (same iteration indices both sides, so drift still cancels) and
differences the two percentiles *inside* each resample, so the interval
brackets the quantity the caller tests. The median path keeps its own paired
estimator deliberately and is bit-identical. Two deterministic tests pin both
contracts; the new one fails against the old code with the mismatch above.
Recorded in [§13.1](#131-correctness-fixes-made-after-the-v1-features-landed).

After the fix, every tail claim's interval covers the movement it claims —
7 of 7 checked — and p95 evidence stopped being automatic.

#### What remains, and what it does not establish

The residual tail rate did not vanish, and this is the real finding:

| Observation (run 3, 78 eligible decisions) | Value |
| --- | --- |
| Tail verdicts | 4 (2 regressed, 2 improved) |
| Ordinary `improved`/`regressed` verdicts | **0** |
| p50 threshold met | **0 of 78** |
| Raw Mann-Whitney p below α | **0 of 78** |
| Decisions with p95 evidence excluding zero | 7 of 78 |
| \|p95 delta\| median / p90 / max | 2.28 / 8.93 / 13.84 ms |
| Decisions with \|p95 delta\| ≥ the 5 ms bar | 20 of 78 (26%) |
| Absolute floor bound (not the percentage) | in ~99% of eligible decisions |

Two mechanisms, cleanly separated:

1. **The bug** explained why *every* decision looked significant at p95. Fixed.
2. **A genuine sensitivity remains.** With a correct interval, the p95 at
   n = 30 is a single volatile order statistic, and a 5 ms bar on a ~45 ms
   baseline is cleared by about a quarter of identical-subject comparisons.
   Four of them also had an interval excluding zero, so four claims survived.
   All four had raw p-values between 0.17 and 0.82: the median path correctly
   said nothing happened, and only the tail moved — which is what a tail verdict
   is for, but at this n the tail is noisy enough to move on its own.

In every observed case the **absolute floor bound, not the percentage**:
`10% × ~45 ms ≈ 4.5 ms` sits just under the 5 ms floor, so the floor decides.

**This is not a threshold problem and has not been treated as one.** The
candidate responses are all design questions, not constants: require more
samples before a tail claim (n ≥ 100 rather than 20), use a tail statistic
that is less volatile at n = 30, or require the median evidence to agree.
Choosing among them needs a campaign with enough eligible decisions per
benchmark to tell them apart; 78 eligible decisions on one host cannot. The
thresholds, the p95 logic, the bootstrap and the quality gates are all
unchanged, and the tail rule should stay as it is until that evidence exists.

#### Host quality

Across all 238 trials of this experiment: **0 good, 128 degraded, 110 poor.**
Every poor trial came from `many_noisy_benchmarks`, which on this host is
driven entirely by `cpu_ms` — Windows process CPU time quantisation keeps that
distribution bimodal, so it is flagged noisy in essentially every trial and
pushes the run past the 30% mark. Consequence: `cpu_ms` recorded **0 eligible
decisions across all 238 trials**, and the poor-quality gate removed 240 of 534
`wall_ms` decisions from the denominators in runs 1–2.

So the eligible-decision count on this host is gated by a metric that cannot
currently be measured well, not by the benchmarks under study. That is worth
fixing as a measurement question before it is treated as a calibration limit.

---

*Canonical methodology for HPOV. Source of truth is the implementation in
`internal/hpov/`; if this document and the code disagree, the code is right and
this document is a bug.*
