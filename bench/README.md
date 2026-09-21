# Forcefield Benchmarks

Benchmarking and performance measurements for Forcefield.

## FF vs Other Harnesses

This benchmark compares Forcefield v1.3.1 with Claude Code 2.1.224, OpenCode 1.18.31, Codex CLI 0.153.4, and Grok CLI 1.0.34 using the same launch-profiling methodology.

### Scope

The campaign measures:

- Cold launch latency
- Warm launch latency
- Process-tree memory usage
- Process counts
- Startup CPU usage
- CLI command latency
- Executable / launcher size
- First visible TUI output
- Forcefield TUI startup and idle behavior

No inference is performed during launch measurements.

### Results

The full benchmark workbook is available in:

`bench1.xlsx`

Headline median results:

| Metric | Forcefield | Claude Code | OpenCode | Codex CLI | Grok CLI |
| --- | ---: | ---: | ---: | ---: | ---: |
| Cold launch p50 | 138 ms | 1720 ms | 2507 ms† | 152 ms | 1799 ms |
| Warm launch p50 | 134 ms | 1587 ms | 2268 ms | 73 ms | 303 ms |
| Tree peak RSS | 41.5 MB | 608.9 MB | 551.1 MB | 24.6 MB | 135.5 MB |
| Startup peak CPU, tree | 10.2% | 39.9% | 98.9% | 10.2% | 29.6% |

Forcefield's TUI reached its first-frame marker at 58.6 ms and
runtime-ready marker at 133.0 ms.

These results are measurements, not rankings or scores.

† OpenCode cold launch includes an approximately 4.7 MB model-catalog
download on first boot and is therefore network-dependent.

## Methodology

### Hardware and software

- OS: Windows 11 AMD64
- CPU: AMD Ryzen 7 7435HS, 16 cores
- Memory: 15.82 GB
- Go: 1.26.4
- Forcefield: v1.3.1, commit `019065a`
- benchd: dev specs v0.1.0

Harness versions:

| Harness | Version |
| --- | --- |
| Forcefield | v1.3.1 |
| Claude Code | 2.1.224 |
| OpenCode | 1.18.31 |
| Codex CLI | 0.153.4 |
| Grok CLI | 1.0.34 |

### Launch profiling

Each harness was launched using the same general methodology:

- `n=30` cold runs
- `n=30` warm runs
- isolated `HOME` and working directory
- sequential execution
- unconnected stdin
- deterministic local-exit workload
- no model inference
- 10 ms process-tree RSS/CPU sampling

Cold runs use a fresh isolated HOME and working directory for each
replication.

Warm runs reuse the isolated HOME and working directory, with five
initial runs discarded.

Launch boundaries necessarily differ between harnesses based on their
startup behavior:

- Forcefield: agent validation
- Claude Code: local authentication
- Grok CLI: local authentication failure
- Codex CLI: trust-check failure
- OpenCode: server boot and local model-resolution failure

The benchmark therefore measures each CLI's startup path under a
common harnessing methodology rather than claiming that every tool
performs an identical internal operation.

### Memory

RSS is sampled for every live process in the process tree.

Two memory measurements are reported:

- **Main:** spawned root process only
- **Tree:** root process plus live descendants

Peak process count is the maximum number of observed live processes
during a run.

Launchers are not treated as equivalent to the full process tree.

### CPU

CPU percentage is calculated as:

`process CPU / wall time / 16 cores × 100`

Startup CPU values report the p50 of per-run peak CPU usage across
`n=30` runs.

### CLI measurements

CLI command latency uses benchd with `n=50` measurements.

Measured commands include:

- `--version`
- `--help`

Executable and launcher sizes are measured in exact bytes.

### TUI measurements

Forcefield's TUI uses a dedicated marker harness to measure:

- First frame
- Runtime ready
- Idle memory
- Startup CPU
- Idle CPU

Forcefield's first-frame marker was measured at 58.6 ms p50
with an 84.3 ms p95.

Other harnesses did not expose a comparable marker protocol, so
their TUI startup measurements are not included.

## Limitations

1. OpenCode cold launch includes a network-dependent model-catalog
   download of approximately 4.7 MB.
2. OpenCode warm launch still includes its full Node server startup.
3. benchd TUI specifications could not run on Windows. A smoke test
   confirmed this with Forcefield v1.3.1, so Forcefield's TUI was
   measured through a separate marker harness.
4. xprof wall-clock measurements include approximately 10–20 ms of
   sampler overhead. benchd measurements were used as a cross-check.
5. Idle CPU comparisons are not made between CLI launchers because the
   benchmarked launch commands terminate by design.
6. Forcefield has a dedicated TUI marker protocol; the other harnesses
   do not expose an equivalent boundary.
7. `go test -race` was unavailable in this environment because GCC
   was not installed.
8. Absolute latency can vary with system load. Comparisons in this
   campaign use the same machine, methodology, and measurement window.

## Reproducing

The benchmark tooling and raw campaign artifacts are kept separately
from the Forcefield source tree.

Campaign outputs referenced by this report include:

- `results/perf-campaign-004-*`
- `reports/perf-campaign-004-*`

The full benchmark workbook contains the detailed measurements and
comparison tables.

## Raw measurements

The launch standard deviations for the campaign were:

| Harness | Cold SD | Warm SD |
| --- | ---: | ---: |
| Forcefield | 16.3 ms | 12.6 ms |
| Claude Code | 131.0 ms | 27.5 ms |
| OpenCode | 41.3 ms | 88.2 ms |
| Codex CLI | 22.5 ms | 9.9 ms |
| Grok CLI | 15.1 ms | 3.8 ms |

Raw JSONL measurements and aggregate reports contain the individual
observations used to derive the reported statistics.

## Notes

This benchmark is intended to document Forcefield's performance
characteristics and provide reproducible measurements across agent
harnesses.

It does not attempt to establish an overall winner, ranking, or
quality score. Performance characteristics should be interpreted in
the context of each harness's architecture and startup behavior.