# Startup optimization — implementation notes

Target: time-to-first-useful-TUI-frame < 30ms (cold median), then
background initialization to runtime-ready. Correctness over benchmark.

## Baseline (measured 2026-09-20, Windows, Ryzen 7 7435HS)

Isolated home, n=15, spawn=0 (medians):

```text
spawn → tmp-init-end      32ms   process + Go runtime + all package inits
      → tmp-main-entry    +0ms
      → tmp-rune-start    +16ms  cobra parsing/dispatch
      → tmp-session-ready +0ms   session.New (in-memory)
      → config-loaded     +1.5ms config.Load
      → stage-skills      +1.6ms skills.New (0 skills, isolated home)
      → stage-memory      +59ms  memory.CurrentProjectStore → git rev-parse #1
      → stage-provider    +0ms   provider construction (offline, trivial)
      → stage-tools       +54ms  newPolicy→ResolveWorkspace → git rev-parse #2 + executor
      → stage-agents      +1ms   registry + overrides + filtered + permissions
      → runtime-ready     +0ms
      → stage-session     +2.7ms recovery.Heal + AlignAgent
      → tmp-model-built   +0ms   sessionEntries + model struct
      → first-frame       +0.2ms tea.NewProgram + first View
total ≈ 168ms
```

In-process micro-benchmarks (real home, warmed):

| component | ms/op |
|---|---|
| config.Load | 0.33 |
| config.Dir | 0.08 |
| skills.New (19 skills) | 8.05 |
| memory.ProjectRoot (git subprocess) | ~80 |
| CurrentProjectStore+Load | ~70 |
| newProvider | ~0 |
| newPolicy | ~62 (= ResolveWorkspace ~56 = git #2 + EvalSymlinks; split 2026-09-20) |
| sandbox.NewExecutor | ~0 (split 2026-09-20 — NOT the cost) |
| builtin.NewManager | ~0 |
| agent.DefaultRegistry | ~0 |
| permissions.NewManager | 0.46 |
| full newRuntime | ~165 |

Process/import floor experiments (spawn→exit):

| binary | median |
|---|---|
| hello (fmt+os, 2.4MB) | ~22–24ms |
| +cobra only (3.2MB) | ~24ms |
| 24MB blob, trivial init | ~24ms (size/paging innocent) |
| full tree, empty main | ~41ms |
| bubbletea / lipgloss / yaml+uuid alone | ~25–28ms each |
| **glamour alone** | **~39ms (+15ms init)** |
| ff --version | ~57ms |

## Conclusions (all measured, not guessed)

1. Two `git rev-parse` subprocesses (~70–80ms each, Windows process
   creation) dominate: `memory.ProjectRoot` (memory stage) and
   `ResolveWorkspace` (tools/policy stage), same cwd, computed twice.
2. `newPolicy + sandbox.NewExecutor` measured 143ms combined — needs a
   split (newPolicy vs NewExecutor) before changing.
3. Glamour package init costs ~15ms before main(); Go cannot defer
   static init. Kept for now; reassess after real-path optimization.
4. config.Load (~1.5ms end-to-end), session.New (~0ms), provider/tools/
   agents construction (~1ms total) are cheap and stay.
5. tea.NewProgram + first View ≈ 0.2–0.5ms — renderer is not the problem.
6. Pre-main floor ≈ 40ms (24 process + ~16 init, mostly glamour) already
   exceeds 30ms; reaching the target requires the glamour decision plus
   cobra-path slimming for bare-`ff`.

## Results (2026-09-20, after TUI-first + git dedupe, isolated home)

n=15+25 timeline runs, spawn=0 (medians):

```text
spawn → config-loaded      ~52ms  (unchanged: process+init+cobra+session+config)
      → first-frame        ~53ms  was ~179ms (minimal model + tea.Run + View ≈ 1ms)
      → stage-memory      ~117ms  git #1 remains, now in background
      → stage-tools       ~117ms  git #2 ELIMINATED (dedupe verified: tools stage ≈ 0ms)
      → runtime-ready     ~120ms  was ~172ms
first-frame < runtime-ready in 40/40 runs (frame max ~77ms < ready min ~113ms)
first-frame → runtime-ready delta ≈ 67ms median
```

process-launch (`ff run` headless, shares newRuntime): 190ms → **120ms
median** (n=20, p95 129ms) from the same dedupe. No benchmark gaming:
same binary, same invocation, same exit semantics.

## Remaining bottlenecks (measured)

1. spawn→config-loaded ~52ms: ~24ms process floor + ~15ms glamour
   init + ~10–16ms cobra + ~2ms session/config. The <30ms target
   requires the glamour decision (drop/replace import) plus cobra
   fast-path for bare-`ff`. Both explicitly deferred by user order.
2. stage-memory ~60–65ms: the one remaining git subprocess, now
   background-only. Further options: cache per-cwd root, or pure
   directory-walk fallback before shelling out (correctness risk:
   git worktrees/submodules — needs care).
3. skills.New ~8ms (real home): background-only now; lazy per-agent
   catalog already exists via load_skill.
4. `go test -race` unavailable on this machine (no gcc/cgo); race
   safety argued by construction (sess idle pre-ready, cfg read-only,
   result via tea.Cmd msg) and must be re-verified where a toolchain
   exists.
5. No `make check` target in this Makefile (only cue-check/lint, tools
   not installed); validated with go test/vet/build/gofmt instead.
