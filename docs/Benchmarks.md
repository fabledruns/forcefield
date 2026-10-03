# Benchmarking Forcefield

Forcefield is measured by **HPOV**, a standalone benchmark suite that lives in
its own repository and its own Go module. HPOV does not import Forcefield, and
this repository does not contain HPOV's implementation.

Forcefield is one of HPOV's supported subjects, described by a **profile**: a
declarative contract saying how to invoke `ff` and what counts as reaching the
boundary HPOV intends to measure.

## Running the campaign

Build HPOV from its own repository, then point it at a Forcefield binary:

```bash
# once, in the HPOV checkout
make hpov                      # produces ./bin/hpov

# here
make build                     # produces ./bin/ff
../hpov/bin/hpov run --subject ff=./bin/ff --profile standard --out results/run.json
```

Or use the convenience target, which builds `ff` first and runs a quick pass:

```bash
make bench-quick
make bench-quick HPOV=/path/to/hpov BENCH_PROFILE=standard \
  BENCH_SELECT='launch.*,mem.*' BENCH_OUT=bench/results/std.json
```

`--subject ff=./bin/ff` is all that is required: HPOV's built-in Forcefield
profile is the default, so no profile file is passed. `hpov --subject-profile
/path/to/forcefield.json` selects a profile explicitly if you want to keep one
under version control.

The same suite measures any other harness — Forcefield is measured as an
external executable, by content hash, not linked into HPOV.

## What gets measured

| Benchmark | What it answers |
| --- | --- |
| `launch.version`, `launch.help` | CLI floor: process start plus runtime init, with no session work |
| `launch.headless-init.steady` | full headless initialization from an existing config |
| `launch.headless-init.first-run` | the same path, but paying first-run config creation |
| `launch.artifact-size` | exact executable size (subject-independent) |
| `tui.startup.timeline` | interactive startup phase by phase, to `first-useful-frame` |
| `mem.headless.peak-rss` | peak resident memory of the headless run |
| `mem.tui.ready-rss` | resident memory of an idle interactive session at readiness |
| `mem.tui.go-heap` | Go `HeapAlloc`/`Sys` at readiness (Go-specific, by design) |

Methodology, every metric definition, and how a verdict is decided live in
HPOV's own documentation: see its `docs/HPOV.md`. That document is canonical and
is not duplicated here.

## Comparing two builds

```bash
hpov run --subject base=./bin/ff-base --profile standard --out base.json
hpov run --subject head=./bin/ff-head --profile standard --out head.json
hpov compare base.json head.json --explain
```

`hpov calibrate` runs the A/A campaign that tells you how often the comparison
claims something when there is nothing to claim. Run it on a quiet machine
before trusting a regression verdict; Forcefield's own first campaign is
recorded in HPOV's documentation.

## Instrumentation

The interactive benchmarks need startup markers, which are already compiled into
`ff` and enabled by the `FF_PERF_MARKERS` environment variable. HPOV's
Forcefield profile sets it for the marker pass only, so marker overhead never
lands inside a headline latency sample. Nothing about normal `ff` behavior
changes.
