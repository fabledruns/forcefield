# hpov — Forcefield performance benchmark runner

`hpov` is the command-line runner for Forcefield's performance benchmark suite.
It measures the real `ff` binary on your machine, records every raw sample, and
compares two runs with stated statistical and practical rules.

**The methodology lives in one place: [`docs/HPOV.md`](../../docs/HPOV.md).**
That document defines what every benchmark measures, every metric means, how
comparison decides a verdict, and what HPOV does not claim. This file is only
the entry point.

## Build

```bash
go build -o bin/hpov ./bench/hpov
```

`hpov` is a separate binary from `ff` and shares no product code path with it.
It takes the subject binary as an argument; it never assumes a path.

## Five commands

```bash
./bin/hpov env                                   # is this machine quiet enough?
./bin/hpov list --here --long                    # what can run here, and what does it measure?
./bin/hpov run --ff head=./bin/ff --profile standard --out results/run.json
./bin/hpov validate results/run.json             # recompute statistics from raw samples
./bin/hpov compare results/baseline.json results/run.json
```

Same-machine A/B with paired statistics:

```bash
./bin/hpov compare-live --base base=./bin/ff-base --head head=./bin/ff-head \
  --profile standard --out results/live.json --comparison-out results/live-compare.json
```

Full flag reference: `docs/HPOV.md` §7, or run `hpov` with no arguments.

Full flag reference: `docs/HPOV.md` §7, or run `hpov` with no arguments.

## A/A calibration campaigns

```bash
./bin/hpov calibrate --ff aa=./bin/ff --profile standard --repeats 20 --out results/aa-campaign
```

Measures the same binary against itself, repeatedly, and reports how often the
comparison claims something when there is nothing to claim. It writes each
trial's `hpov.result` and `hpov.compare` (existing formats, still the source of
truth) plus one `calibration.json` (`hpov.calibration` v1, schema in
[`schema/hpov-calibration.v1.schema.json`](schema/hpov-calibration.v1.schema.json)).
Re-derive the report later without re-measuring:

```bash
./bin/hpov calibrate --report results/aa-campaign/calibration.json
```

It measures only. It never changes a threshold, never selects gating metrics
and never fails a build. Methodology and how to read the numbers:
`docs/HPOV.md` §14.

## What you get

- `hpov.result` documents with raw samples, derived statistics, host
  fingerprint, subject provenance, per-metric validity and quality flags.
  JSON Schema: [`schema/hpov-result.v1.schema.json`](schema/hpov-result.v1.schema.json)
- `hpov.compare` documents with per-metric verdicts, the evidence behind each
  one, coverage for what could not be compared, and the method that produced it.
  JSON Schema: [`schema/hpov-compare.v1.schema.json`](schema/hpov-compare.v1.schema.json)

## Before trusting a number

A result can be schema-valid and still be a bad basis for a regression claim.
Read the run's quality label and flags, validate the document, and check the
comparison's verdict reasons. `docs/HPOV.md` §9 covers this.

HPOV exits **0** on a regression unless you pass `--fail-on-regression`. It does
not gate CI on its own and has no selected "gating metric" list.
