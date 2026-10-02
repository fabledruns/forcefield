// Memory benchmarks: what a Forcefield run costs in resident memory,
// and what its Go runtime heap held at the same moment.
//
// Three metrics, deliberately kept apart:
//
//	mem.headless.peak-rss  OS peak resident memory of the headless
//	                       process tree, from the kernel's own tracking
//	                       where it exists (Windows peak working set,
//	                       Linux VmHWM) and a sampled tree total
//	                       alongside it.
//	mem.tui.ready-rss      OS resident memory at the TUI's readiness
//	                       boundary (first-useful-frame), root and
//	                       tree, current rather than peak.
//	mem.tui.go-heap        Go runtime HeapAlloc at the same boundary,
//	                       derived from the ff-perf marker fields and
//	                       never conflated with OS memory.
//
// Peak RSS semantics (both metrics):
//
//   - Root process: always measured. The kernel-tracked peak is exact
//     (Windows PeakWorkingSetSize, Linux VmHWM, macOS rusage Maxrss
//     via spawn). Descendants are included only in the sampled tree
//     figure.
//   - Tree: root plus every descendant alive at sample time, summed.
//     Sampling interval 10 ms, so a descendant that exists entirely
//     between two samples is missed and the figure is a lower bound.
//     The interval was chosen by measurement (see
//     collect.DefaultInterval) and is recorded with every value. This
//     is stated in the metric's platform_semantics tag.
//   - After the root exits, no further sampling happens; the sampler's
//     last value and the kernel peak are what remain.
//
// The tree peak is reported next to the kernel peak rather than
// instead of it: they answer different questions, and collapsing them
// would hide a missed spike (tree) or pretend a working set is RSS
// (cross-OS).
package suites

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"forcefield/internal/hpov/bench"
	"forcefield/internal/hpov/collect"
	"forcefield/internal/hpov/envinfo"
	"forcefield/internal/hpov/fixture"
	"forcefield/internal/hpov/markers"
	"forcefield/internal/hpov/pty"
	"forcefield/internal/hpov/schema"
	"forcefield/internal/hpov/spawn"
)

// Benchmark ids.
const (
	MemHeadlessPeakRSSID = "mem.headless.peak-rss"
	MemTUIReadyRSSID     = "mem.tui.ready-rss"
	MemTUIGoHeapID       = "mem.tui.go-heap"
)

// MemBenchmarkIDs lists the memory benchmarks in registration order.
var MemBenchmarkIDs = []string{MemHeadlessPeakRSSID, MemTUIReadyRSSID, MemTUIGoHeapID}

// MemoryBenchmarks returns the memory suite constructors.
//
// MemTUIGoHeapID is derived from the same marker pass as
// MemTUIReadyRSSID but is registered separately because it answers a
// different question and is cross-platform comparable, while the RSS
// metrics are per-OS. It runs its own pty pass rather than sharing
// iterations: a benchmark's samples must be independently repeatable.
func MemoryBenchmarks() []bench.Benchmark {
	return []bench.Benchmark{
		&memHeadlessBench{},
		&memReadyRSSBench{},
		&memGoHeapBench{},
	}
}

type memHeadlessBench struct{}

func (b *memHeadlessBench) Spec() bench.Spec {
	return bench.Spec{
		ID:                MemHeadlessPeakRSSID,
		DefinitionVersion: 1,
		Title:             "Headless run peak resident memory",
		Purpose: "Peak resident memory of the `ff run --agent <unknown>` workload, " +
			"the same operation launch.headless-init.steady measures for wall time. " +
			"Separate iterations so memory collection never shares a run with a " +
			"latency sample. Kernel-tracked root peak plus a sampled process-tree " +
			"total; the two are distinct metrics and never combined.",
		Kind: bench.KindE2E,
		Tier: 1,
		Metrics: []bench.MetricSpec{
			{
				Name:      "peak_rss_bytes",
				Unit:      "bytes",
				Direction: bench.LowerIsBetter,
				// Windows peak working set is not RSS (it includes
				// shared and mapped pages); Linux VmHWM is RSS. Values
				// are comparable within one OS only.
				PlatformSemantics: map[string]string{
					"windows": "windows:peak_working_set_exact",
					"linux":   "linux:vm_hwm_exact",
					"darwin":  "darwin:rusage_maxrss_exact",
				},
			},
			{
				Name:      "peak_tree_rss_bytes",
				Unit:      "bytes",
				Direction: bench.LowerIsBetter,
				PlatformSemantics: map[string]string{
					"windows": "windows:sum_working_set_sampled_lower_bound",
					"linux":   "linux:sum_vm_rss_sampled_lower_bound",
					"darwin":  "darwin:sum_resident_size_sampled_lower_bound",
				},
			},
		},
		Requires: []string{"memory"},
		Params: map[string]string{
			"workload": "ff run --agent <unknown> (headless, no pty)",
			"profile":  "steady", "repo": "none", "mcp_servers": "0",
			"sample_interval_ms": "10",
			"peak_source":        "kernel-tracked (exact); tree total is sampled",
		},
		Predicate: "exit_code==1 (the unknown-agent exit) and a kernel-tracked or " +
			"sampled peak is available; unavailable memory is reported as such, never as zero",
		Plans: map[string]bench.Plan{
			// Memory is low-variance: fewer iterations than the latency
			// plans need.
			"quick":    {Warmup: 1, N: 8, TimeoutSec: 60},
			"standard": {Warmup: 2, N: 15, TimeoutSec: 60},
			"full":     {Warmup: 3, N: 30, TimeoutSec: 120},
		},
		CVThreshold: 0.03,
	}
}

func (b *memHeadlessBench) Setup(ctx context.Context, env *bench.RunEnv, subj bench.Subject) (bench.Fixture, error) {
	parent, err := launchParent(env.Root, MemHeadlessPeakRSSID, subj)
	if err != nil {
		return bench.Fixture{}, err
	}
	home, work, err := freshDirs(parent, "shared")
	if err != nil {
		return bench.Fixture{}, err
	}
	timeout := planTimeout(b.Spec(), env.Profile)
	// Primed identically to launch.headless-init so the peak reflects a
	// steady-state run, not first-run config creation.
	if err := primeHeadlessHome(ctx, subj.Path, home, work, timeout); err != nil {
		return bench.Fixture{}, err
	}
	return bench.Fixture{HomeDir: home, WorkDir: work, Timeout: timeout}, nil
}

// kernelPeakOf yields the kernel-tracked peak for a finished spawn. It
// is a seam so a hermetic test can present an OS that exposes no peak,
// which cannot be arranged by making a real process unreadable.
var kernelPeakOf = func(res spawn.Result) (int64, string) {
	return res.PeakRSSBytes, res.PeakSemantics
}

func (b *memHeadlessBench) Iterate(ctx context.Context, fx bench.Fixture, subj bench.Subject, it bench.Iter) (bench.Observation, error) {
	obs := bench.Observation{
		Values:      map[string]float64{},
		Attrs:       map[string]string{},
		Unavailable: map[string]string{},
	}
	fail := func(reason string) (bench.Observation, error) {
		obs.Valid = false
		obs.InvalidReason = reason
		for _, m := range b.Spec().Metrics {
			obs.Values[m.Name] = 0
		}
		return obs, nil
	}

	// Fresh workdir per iteration, created untimed.
	_, work, err := freshDirs(fx.WorkDir, fmt.Sprintf("w-%d", it.Index))
	if err != nil {
		return fail(err.Error())
	}

	sampler := collect.NewSampler()
	res, err := spawn.Run(ctx, spawn.Options{
		Path: subj.Path, Args: headlessArgs(),
		Env: launchEnv(fx.HomeDir, false), Dir: work, Timeout: fx.Timeout,
		SampleInterval: collect.DefaultInterval,
		Sample:         func(pid int) { sampler.Sample(pid) },
	})
	if err != nil {
		return fail("spawn: " + err.Error())
	}
	if res.TimedOut {
		return fail("headless run timed out; peak memory not measured")
	}
	// The workload's unknown-agent exit 1 is its normal completion.
	if res.ExitCode != 1 {
		return fail(fmt.Sprintf("unexpected exit %d, want 1 (unknown-agent exit)", res.ExitCode))
	}

	obs.Attrs["exit_code"] = itoa(uint64(res.ExitCode))
	obs.Attrs["wall_ms"] = fmt.Sprintf("%.3f", res.WallMS)
	obs.Attrs["sample_interval_ms"] = itoa(uint64(sampler.Interval() / time.Millisecond))
	obs.Attrs["sample_failures"] = itoa(uint64(sampler.Report().Failures))

	rep := sampler.Report()
	obs.Attrs["tree_samples"] = itoa(uint64(rep.Samples))
	obs.Attrs["tree_max_descendants"] = itoa(uint64(rep.DescMax))
	obs.Attrs["tree_sampled_peak"] = boolAttr(rep.Sampled)

	// Kernel-tracked root peak: exact, post-exit readable.
	kernelPeak, peakSemantics := kernelPeakOf(res)
	switch {
	case kernelPeak > 0:
		obs.Values["peak_rss_bytes"] = float64(kernelPeak)
		obs.Attrs["peak_semantics"] = peakSemantics
		obs.Attrs["peak_source"] = "kernel_tracked_exact"
	default:
		obs.Unavailable["peak_rss_bytes"] = peakUnavailableReason(peakSemantics)
		obs.Attrs["peak_source"] = "unavailable"
		obs.Attrs["peak_reason"] = peakUnavailableReason(peakSemantics)
	}

	// Sampled tree total: a lower bound, labelled as such.
	switch {
	case rep.PeakBytes > 0:
		obs.Values["peak_tree_rss_bytes"] = float64(rep.PeakBytes)
		obs.Attrs["tree_semantics"] = rep.Semantics
		if rep.Samples == 0 {
			obs.Attrs["tree_peak_may_be_missed"] = "true"
		}
	default:
		obs.Unavailable["peak_tree_rss_bytes"] = treeUnavailableReason(rep)
		obs.Attrs["tree_reason"] = treeUnavailableReason(rep)
		obs.Attrs["tree_peak_may_be_missed"] = "true"
	}

	// A run whose memory could not be read at all is invalid; a run
	// missing only one of the two figures stays valid with the other
	// reported and the gap named.
	if len(obs.Unavailable) == len(b.Spec().Metrics) {
		return fail("no memory measurement available: " +
			obs.Unavailable["peak_rss_bytes"] + "; " +
			obs.Unavailable["peak_tree_rss_bytes"])
	}
	obs.Valid = true
	return obs, nil
}

func (b *memHeadlessBench) Teardown(_ context.Context, _ bench.Fixture) error { return nil }

func peakUnavailableReason(semantics string) string {
	if semantics == "" || semantics == "unavailable" {
		return collect.ReasonUnsupported + ": the OS exposed no kernel peak for this process"
	}
	return collect.ReasonQueryFailed + ": kernel peak unreadable"
}

func treeUnavailableReason(rep collect.Report) string {
	switch {
	case rep.Samples == 0 && rep.Reason != "":
		return rep.Reason + ": no process-tree sample succeeded"
	case rep.Samples == 0:
		return collect.ReasonNoProcess + ": sampler never ran"
	default:
		return collect.ReasonQueryFailed + ": no usable tree sample"
	}
}

func boolAttr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// readyRSSSample is the memory reading taken at the readiness boundary.
type readyRSSSample struct {
	at   time.Time
	tree collect.Tree
	// rootRSS is the root process's own resident size, read
	// separately from the tree so the two figures never blur.
	rootRSS       int64
	rootSemantics string
	// rootLag is marker receipt -> root query done. It bounds the
	// timing uncertainty of ready_rss_bytes, the primary metric.
	rootLag time.Duration
	// treeLag is marker receipt -> tree query done. It is larger
	// because enumerating the process table (Toolhelp on Windows,
	// /proc on Linux) costs more than one query.
	treeLag time.Duration
	reason  string
	attempt bool
}

type memReadyRSSBench struct {
	// readiness and quit bounds mirror the timeline benchmark so both
	// suites observe the same session shape.
	readinessTimeout time.Duration
	quitGrace        time.Duration
}

func (b *memReadyRSSBench) readiness() time.Duration {
	if b.readinessTimeout > 0 {
		return b.readinessTimeout
	}
	return DefaultTUIReadiness
}

func (b *memReadyRSSBench) grace() time.Duration {
	if b.quitGrace > 0 {
		return b.quitGrace
	}
	return DefaultTUIQuitGrace
}

func (b *memReadyRSSBench) Spec() bench.Spec {
	return bench.Spec{
		ID:                MemTUIReadyRSSID,
		DefinitionVersion: 1,
		Title:             "Interactive idle resident memory at readiness",
		Purpose: "What an interactive Forcefield session costs in resident memory " +
			"once it is ready and idle. Sampled at first-useful-frame, the same " +
			"primary readiness boundary tui.startup.timeline uses, on the same " +
			"fixed 120x40 pty. Current resident memory, not peak: the question is " +
			"the resting footprint, and a peak would answer a different one.",
		Kind: bench.KindE2E,
		Tier: 1,
		Metrics: []bench.MetricSpec{
			{
				Name:      "ready_rss_bytes",
				Unit:      "bytes",
				Direction: bench.LowerIsBetter,
				PlatformSemantics: map[string]string{
					"windows": "windows:working_set_at_ready_root_only",
					"linux":   "linux:vm_rss_at_ready_root_only",
					"darwin":  "darwin:resident_size_at_ready_root_only",
				},
			},
			{
				Name:      "ready_tree_rss_bytes",
				Unit:      "bytes",
				Direction: bench.LowerIsBetter,
				PlatformSemantics: map[string]string{
					"windows": "windows:sum_working_set_at_ready_lower_bound",
					"linux":   "linux:sum_vm_rss_at_ready_lower_bound",
					"darwin":  "darwin:sum_resident_size_at_ready_lower_bound",
				},
			},
		},
		Requires: []string{"pty", "memory"},
		Params: map[string]string{
			"workload": "ff (interactive, pty 120x40, TERM=xterm-256color)",
			"profile":  "steady", "repo": "none", "mcp_servers": "0",
			"readiness_mark":     tuiPrimaryMark,
			"sample_method":      "single query in the marker callback, no polling",
			"timing_uncertainty": "root_sample_lag_ns / tree_sample_lag_ns bound the gap from the readiness marker to each query; a lag below clock_resolution_ns reads as 0",
			"root_vs_tree":       "ready_rss_bytes is the root process alone; the tree figure is reported separately",
		},
		Predicate: "first-useful-frame observed, memory query completed, and clean " +
			"/exit quit with exit_code==0; an unavailable query is reported as such",
		Plans: map[string]bench.Plan{
			"quick":    {Warmup: 1, N: 8, TimeoutSec: 120},
			"standard": {Warmup: 3, N: 20, TimeoutSec: 120},
			"full":     {Warmup: 3, N: 50, TimeoutSec: 120},
		},
		CVThreshold: 0.05,
	}
}

func (b *memReadyRSSBench) Setup(ctx context.Context, env *bench.RunEnv, subj bench.Subject) (bench.Fixture, error) {
	return setupTUISession(ctx, env, subj, MemTUIReadyRSSID, b.Spec(), env.Profile)
}

func (b *memReadyRSSBench) Iterate(ctx context.Context, fx bench.Fixture, subj bench.Subject, it bench.Iter) (bench.Observation, error) {
	obs, sample, tl, exit, quit, bytes, fail := b.runSession(ctx, fx, subj, it, true)
	if fail != nil {
		return obs, fail
	}

	obs.Attrs["exit_code"] = itoa(uint64(exit))
	obs.Attrs["quit_method"] = quit
	obs.Attrs["pty_bytes"] = itoa(uint64(bytes.Load()))
	obs.Attrs["readiness_mark"] = tuiPrimaryMark
	if missing := tl.Missing(tuiMarks); len(missing) > 0 {
		obs.Attrs["missing_marks"] = strings.Join(missing, ",")
	}

	// Document the sampling window instead of asserting an exact
	// instant: the query runs on the marker callback, so the gap is the
	// marker-receipt-to-query-completion time, recorded per query.
	if sample.attempt {
		obs.Attrs["root_sample_lag_ns"] = itoa(uint64(sample.rootLag.Nanoseconds()))
		obs.Attrs["tree_sample_lag_ns"] = itoa(uint64(sample.treeLag.Nanoseconds()))
		// A lag below the host clock's resolution reads as 0. Record the
		// resolution so 0 is not mistaken for "instantaneous": it means
		// "shorter than this host can measure".
		_, resNs := envinfo.CalibrateClock()
		obs.Attrs["clock_resolution_ns"] = itoa(uint64(resNs))
		obs.Attrs["sample_method"] = "marker_callback_query"
	} else {
		obs.Attrs["sample_method"] = "not_attempted"
	}
	if sample.reason != "" {
		obs.Attrs["sample_reason"] = sample.reason
	}

	if _, ready := tl.Ms(tuiPrimaryMark, time.Now()); !ready && !sample.attempt {
		// The primary mark never arrived, so no sample was possible.
		return obs, nil
	}

	if sample.tree.RSSBytes > 0 && sample.tree.Semantics != "" {
		obs.Values["ready_tree_rss_bytes"] = float64(sample.tree.RSSBytes)
		obs.Attrs["tree_semantics"] = sample.tree.Semantics
		obs.Attrs["tree_descendants"] = itoa(uint64(sample.tree.Descendants))
		obs.Attrs["tree_is_lower_bound"] = "true"
	} else {
		obs.Unavailable["ready_tree_rss_bytes"] = treeReadReason(sample)
	}
	// Root alone, from the same reading.
	if sample.tree.PeakBytes >= 0 && sample.tree.PeakSemantics != "" {
		// Peak at readiness is reported as context, not as this metric:
		// the metric is current resident memory.
		obs.Attrs["ready_root_peak_rss_bytes"] = itoa(uint64(sample.tree.PeakBytes))
		obs.Attrs["ready_root_peak_semantics"] = sample.tree.PeakSemantics
	}
	if sample.rootRSS > 0 {
		obs.Values["ready_rss_bytes"] = float64(sample.rootRSS)
		obs.Attrs["root_semantics"] = sample.rootSemantics
	} else {
		obs.Unavailable["ready_rss_bytes"] = rootReadReason(sample)
	}
	return obs, nil
}

// runSession runs one TUI session, reusing the timeline benchmark's pty
// setup, marker collection, readiness detection and teardown.
//
// wantMemory installs the marker callback that samples memory at
// readiness; mem.tui.go-heap leaves it off because it reads the Go heap
// from the marker fields themselves.
func (b *memReadyRSSBench) runSession(ctx context.Context, fx bench.Fixture, subj bench.Subject, it bench.Iter, wantMemory bool) (bench.Observation, *readyRSSSample, markers.Timeline, int, string, *atomic.Int64, error) {
	obs := bench.Observation{
		Values:      map[string]float64{},
		Attrs:       map[string]string{},
		Unavailable: map[string]string{},
	}
	var bytes atomic.Int64
	sample := &readyRSSSample{}

	_, work, err := freshDirs(fx.WorkDir, fmt.Sprintf("w-%d", it.Index))
	if err != nil {
		return obs, sample, markers.Timeline{}, -1, "", &bytes, err
	}

	// Same terminal contract as the timeline benchmark: fixed 120x40,
	// same environment. The pty setup and cleanup live in package pty;
	// no second launcher exists.
	child, err := pty.Start(pty.Options{
		Path: subj.Path, Env: tuiEnv(fx.HomeDir), Dir: work,
		Cols: pty.DefaultCols, Rows: pty.DefaultRows,
	})
	if err != nil {
		return obs, sample, markers.Timeline{}, -1, "", &bytes, err
	}
	defer func() { _ = child.Close() }()
	go drainToNull(child.Output(), &bytes)

	var hook func(string, time.Time)
	if wantMemory {
		pid := child.Pid()
		hook = func(ev string, at time.Time) {
			if ev != tuiPrimaryMark || sample.attempt {
				return
			}
			sample.attempt = true
			sample.at = at
			// Root first, tree second. The tree sum includes the root,
			// so reading it last keeps the pair consistent on a growing
			// process: reading the root last could report a figure
			// larger than the tree it belongs to.
			if r, rerr := collect.ReadingOf(pid); rerr == nil && r.Ok() {
				sample.rootRSS = r.RSSBytes
				sample.rootSemantics = r.RSSSemantics
			}
			sample.rootLag = time.Since(at)
			tree, terr := collect.TreeOf(pid)
			sample.treeLag = time.Since(at)
			sample.tree = tree
			if terr != nil {
				sample.reason = tree.Reason
			}
		}
	}

	lines, eof, timedOut, readErr := collectTimelineHooked(
		child.Stderr(), b.readiness(), &bytes, hook)
	tl := markers.Build(lines)

	// Teardown is the timeline benchmark's: separate /exit and Enter
	// writes, then a bounded wait with a forced kill fallback.
	quit := "clean"
	_, _ = child.WriteInput([]byte("/exit"))
	time.Sleep(DefaultTUIKeySettle)
	_, _ = child.WriteInput([]byte("\r"))
	exit, ok := waitBounded(child, b.grace())
	if !ok {
		_ = child.Kill()
		quit = "kill-after-quit-timeout"
		exit, _ = waitBounded(child, 15*time.Second)
	}

	if ctx.Err() != nil {
		return obs, sample, tl, exit, quit, &bytes,
			&bench.SkipError{Status: schema.StatusInvalid,
				Code: schema.ErrTimeout, Detail: "run aborted: " + ctx.Err().Error()}
	}

	_, ready := tl.Ms(tuiPrimaryMark, sample.at)
	switch {
	case readErr != nil:
		obs.Attrs["read_error"] = readErr.Error()
		obs.Valid = false
		obs.InvalidReason = "marker stream read failed: " + readErr.Error()
		return obs, sample, tl, exit, quit, &bytes, nil
	case timedOut && !ready:
		obs.Valid = false
		obs.InvalidReason = "readiness timeout (" + b.readiness().String() +
			"); primary marker " + tuiPrimaryMark + " never observed" +
			eofSuffix(eof, tuiPrimaryMark)
		return obs, sample, tl, exit, quit, &bytes, nil
	case !ready:
		obs.Valid = false
		obs.InvalidReason = "primary readiness marker " + tuiPrimaryMark +
			" missing" + eofSuffix(eof, tuiPrimaryMark)
		return obs, sample, tl, exit, quit, &bytes, nil
	case quit != "clean":
		obs.Valid = false
		obs.InvalidReason = "forced termination after quit grace"
		return obs, sample, tl, exit, quit, &bytes, nil
	case exit != 0:
		obs.Valid = false
		obs.InvalidReason = fmt.Sprintf("unexpected exit %d after /exit quit", exit)
		return obs, sample, tl, exit, quit, &bytes, nil
	}
	obs.Valid = true
	return obs, sample, tl, exit, quit, &bytes, nil
}

func treeReadReason(s *readyRSSSample) string {
	if s.reason != "" {
		return s.reason + ": process tree unreadable at readiness"
	}
	return collect.ReasonQueryFailed + ": no tree reading at readiness"
}

func rootReadReason(s *readyRSSSample) string {
	if s.reason != "" {
		return s.reason + ": root process unreadable at readiness"
	}
	return collect.ReasonQueryFailed + ": no root reading at readiness"
}

func (b *memReadyRSSBench) Teardown(_ context.Context, _ bench.Fixture) error { return nil }

// memGoHeapBench reports the Go runtime's own view of memory at the
// readiness boundary.
//
// Definition, deliberately single: Go runtime HeapAlloc — bytes of
// allocated heap objects, including unreachable ones not yet freed.
// Not HeapInuse (span bytes) and not Sys (total obtained from the OS).
// Those are different quantities with different meanings and appear
// under their own names only.
//
// Source: the alloc= field of the ff-perf marker lines the child already
// writes at first-frame and runtime-ready (perfmark.EventMem). No extra
// product instrumentation was added: the value was already on the wire,
// so the derivation costs the harness nothing and cannot perturb the
// measurement beyond the ReadMemStats stop-the-world pause that emitting
// the marker already performed.
type memGoHeapBench struct {
	readinessTimeout time.Duration
	quitGrace        time.Duration
}

func (b *memGoHeapBench) readiness() time.Duration {
	if b.readinessTimeout > 0 {
		return b.readinessTimeout
	}
	return DefaultTUIReadiness
}

func (b *memGoHeapBench) grace() time.Duration {
	if b.quitGrace > 0 {
		return b.quitGrace
	}
	return DefaultTUIQuitGrace
}

func (b *memGoHeapBench) Spec() bench.Spec {
	return bench.Spec{
		ID:                MemTUIGoHeapID,
		DefinitionVersion: 1,
		Title:             "Go runtime heap at TUI readiness (derived)",
		Purpose: "The Go runtime's own accounting of heap at the interactive " +
			"readiness boundary, read from the ff-perf marker fields the subject " +
			"already emits. Explains RSS changes without pretending to be OS " +
			"memory: HeapAlloc is not RSS, and this metric is never compared " +
			"against one.",
		Kind: bench.KindE2E,
		Tier: 1,
		Metrics: []bench.MetricSpec{
			{
				Name:      "go_heap_alloc_bytes",
				Unit:      "bytes",
				Direction: bench.LowerIsBetter,
				// Go-level and OS-independent: comparable across hosts
				// for the same build.
				PlatformSemantics: map[string]string{
					"windows": "go:heapalloc",
					"linux":   "go:heapalloc",
					"darwin":  "go:heapalloc",
				},
			},
			{
				Name:      "go_sys_bytes",
				Unit:      "bytes",
				Direction: bench.LowerIsBetter,
				PlatformSemantics: map[string]string{
					"windows": "go:memstats_sys",
					"linux":   "go:memstats_sys",
					"darwin":  "go:memstats_sys",
				},
			},
		},
		Requires: []string{"pty", "markers"},
		Params: map[string]string{
			"workload": "ff (interactive, pty 120x40, TERM=xterm-256color)",
			"profile":  "steady", "repo": "none", "mcp_servers": "0",
			"readiness_mark": tuiPrimaryMark,
			"heap_field":     "runtime.MemStats.HeapAlloc at first-useful-frame",
			"sys_field":      "runtime.MemStats.Sys at first-useful-frame (separate metric, not RSS)",
			"source":         "ff-perf alloc=/sys= marker fields; no added instrumentation",
		},
		Predicate: "first-useful-frame observed with an alloc= field, and clean " +
			"/exit quit with exit_code==0; a marker without memory fields yields " +
			"unavailable metrics, never zeros",
		Plans: map[string]bench.Plan{
			"quick":    {Warmup: 1, N: 8, TimeoutSec: 120},
			"standard": {Warmup: 3, N: 20, TimeoutSec: 120},
			"full":     {Warmup: 3, N: 50, TimeoutSec: 120},
		},
		CVThreshold: 0.10,
	}
}

func (b *memGoHeapBench) Setup(ctx context.Context, env *bench.RunEnv, subj bench.Subject) (bench.Fixture, error) {
	return setupTUISession(ctx, env, subj, MemTUIGoHeapID, b.Spec(), env.Profile)
}

func (b *memGoHeapBench) Iterate(ctx context.Context, fx bench.Fixture, subj bench.Subject, it bench.Iter) (bench.Observation, error) {
	// wantMemory=false: no OS query is needed, so no sampling overhead
	// is added to a Go-level measurement.
	rb := &memReadyRSSBench{readinessTimeout: b.readiness(), quitGrace: b.grace()}
	obs, _, tl, exit, quit, bytes, err := rb.runSession(ctx, fx, subj, it, false)
	if err != nil {
		return obs, err
	}
	if !obs.Valid {
		for _, m := range b.Spec().Metrics {
			if _, ok := obs.Values[m.Name]; !ok {
				obs.Values[m.Name] = 0
			}
		}
		return obs, nil
	}

	obs.Attrs["exit_code"] = itoa(uint64(exit))
	obs.Attrs["quit_method"] = quit
	obs.Attrs["pty_bytes"] = itoa(uint64(bytes.Load()))
	obs.Attrs["readiness_mark"] = tuiPrimaryMark
	obs.Attrs["heap_field"] = "runtime.MemStats.HeapAlloc"
	obs.Attrs["derived_from"] = "ff-perf alloc= field on the first-useful-frame marker line"

	// Derived from the recorded marker values, so the metric is
	// reproducible from the raw iteration alone.
	mem, ok := tl.Mem[tuiPrimaryMark]
	if !ok || (mem.Alloc == 0 && mem.Sys == 0) {
		// A marker line without memory fields is a gap in the data,
		// not a heap of zero bytes.
		obs.Unavailable["go_heap_alloc_bytes"] =
			"marker " + tuiPrimaryMark + " carried no alloc field"
		obs.Unavailable["go_sys_bytes"] =
			"marker " + tuiPrimaryMark + " carried no sys field"
		obs.Valid = true
		return obs, nil
	}
	obs.Values["go_heap_alloc_bytes"] = float64(mem.Alloc)
	obs.Values["go_sys_bytes"] = float64(mem.Sys)

	// Sanity: HeapAlloc can never exceed Sys, and neither can be zero
	// for a process that has run. A violation means the fields were
	// mixed up somewhere upstream.
	if mem.Alloc > mem.Sys {
		obs.Valid = false
		obs.InvalidReason = fmt.Sprintf(
			"implausible Go heap: HeapAlloc %d exceeds Sys %d", mem.Alloc, mem.Sys)
		return obs, nil
	}
	obs.Valid = true
	return obs, nil
}

func (b *memGoHeapBench) Teardown(_ context.Context, _ bench.Fixture) error { return nil }

// setupTUISession primes an isolated home the same way the timeline
// benchmark does, so both suites observe the same steady state.
func setupTUISession(ctx context.Context, env *bench.RunEnv, subj bench.Subject, id string, spec bench.Spec, profile string) (bench.Fixture, error) {
	parent, err := launchParent(env.Root, id, subj)
	if err != nil {
		return bench.Fixture{}, err
	}
	home, _, err := freshDirs(parent, "shared-home")
	if err != nil {
		return bench.Fixture{}, err
	}
	primeWork, err := fixture.NewWorkDir(parent, "prime-work")
	if err != nil {
		return bench.Fixture{}, err
	}
	timeout := planTimeout(spec, profile)
	if err := primeHeadlessHome(ctx, subj.Path, home, primeWork, timeout); err != nil {
		return bench.Fixture{}, err
	}
	return bench.Fixture{HomeDir: home, WorkDir: parent, Timeout: timeout}, nil
}
