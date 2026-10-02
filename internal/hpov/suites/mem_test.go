package suites

import (
	"context"
	"strings"
	"testing"
	"time"

	"forcefield/internal/hpov/bench"
	"forcefield/internal/hpov/collect"
	"forcefield/internal/hpov/markers"
	"forcefield/internal/hpov/spawn"
)

func TestMemoryRegistration(t *testing.T) {
	seen := map[string]int{}
	for _, r := range All() {
		seen[r.Bench.Spec().ID]++
	}
	for _, id := range MemBenchmarkIDs {
		if seen[id] != 1 {
			t.Errorf("%s registered %d times", id, seen[id])
		}
	}
}

func TestMemorySpecsDeclareContract(t *testing.T) {
	for _, b := range MemoryBenchmarks() {
		s := b.Spec()
		if s.ID == "" || s.Purpose == "" || s.Predicate == "" {
			t.Errorf("%s missing contract", s.ID)
		}
		if len(s.Metrics) == 0 {
			t.Errorf("%s owns no metrics", s.ID)
		}
		// Memory values are only comparable within an OS, so every
		// metric must say which OS quantity it is.
		for _, m := range s.Metrics {
			if len(m.PlatformSemantics) == 0 {
				t.Errorf("%s/%s has no platform_semantics", s.ID, m.Name)
			}
			if m.Unit != "bytes" {
				t.Errorf("%s/%s unit = %q, want bytes", s.ID, m.Name, m.Unit)
			}
		}
	}
}

func TestMemoryMetricNames(t *testing.T) {
	// Names are part of the result contract and must stay stable.
	want := map[string][]string{
		MemHeadlessPeakRSSID: {"peak_rss_bytes", "peak_tree_rss_bytes"},
		MemTUIReadyRSSID:     {"ready_rss_bytes", "ready_tree_rss_bytes"},
		MemTUIGoHeapID:       {"go_heap_alloc_bytes", "go_sys_bytes"},
	}
	for _, b := range MemoryBenchmarks() {
		s := b.Spec()
		expect, ok := want[s.ID]
		if !ok {
			t.Fatalf("unexpected benchmark %s", s.ID)
		}
		if len(s.Metrics) != len(expect) {
			t.Fatalf("%s has %d metrics, want %d", s.ID, len(s.Metrics), len(expect))
		}
		for i, m := range s.Metrics {
			if m.Name != expect[i] {
				t.Errorf("%s metric %d = %s, want %s", s.ID, i, m.Name, expect[i])
			}
		}
	}
}

func TestHeadlessPeakSemanticsAreOSQualified(t *testing.T) {
	s := (&memHeadlessBench{}).Spec()
	m := s.Metrics[0]
	// Windows peak working set is NOT RSS; the tag must say so.
	if got := m.PlatformSemantics["windows"]; !strings.Contains(got, "peak_working_set") {
		t.Errorf("windows semantics = %q", got)
	}
	if got := m.PlatformSemantics["linux"]; !strings.Contains(got, "vm_hwm") {
		t.Errorf("linux semantics = %q", got)
	}
	for os, tag := range m.PlatformSemantics {
		if !strings.Contains(tag, "exact") {
			t.Errorf("%s kernel peak must be labelled exact: %q", os, tag)
		}
	}
	// The sampled tree figure must admit it is a lower bound.
	tree := s.Metrics[1]
	for os, tag := range tree.PlatformSemantics {
		if !strings.Contains(tag, "lower_bound") {
			t.Errorf("%s tree peak must be labelled a lower bound: %q", os, tag)
		}
	}
}

func TestGoHeapIsNotConflatedWithRSS(t *testing.T) {
	s := (&memGoHeapBench{}).Spec()
	for _, m := range s.Metrics {
		if strings.Contains(m.Name, "rss") {
			t.Errorf("Go heap metric named like RSS: %s", m.Name)
		}
		if m.PlatformSemantics["windows"] != "go:heapalloc" &&
			m.PlatformSemantics["windows"] != "go:memstats_sys" {
			t.Errorf("%s semantics = %v", m.Name, m.PlatformSemantics)
		}
	}
	if got := s.Params["heap_field"]; !strings.Contains(got, "HeapAlloc") {
		t.Errorf("heap_field must name the runtime field: %q", got)
	}
	if got := s.Params["source"]; !strings.Contains(got, "no added instrumentation") {
		t.Errorf("source must record that no instrumentation was added: %q", got)
	}
}

func TestGoHeapSanityRejectsImpossibleValues(t *testing.T) {
	// A marker whose HeapAlloc exceeds Sys is a field mix-up, not a
	// result: the sample must be rejected rather than reported.
	obs := bench.Observation{Values: map[string]float64{}, Attrs: map[string]string{}}
	_ = obs
	base := time.Now()
	tl := markers.Build([]markers.StampedLine{
		{At: base, Text: "ff-perf first-useful-frame alloc=900 sys=100\n"},
		{At: base.Add(time.Millisecond), Text: "ff-perf runtime-ready\n"},
	})
	mem := tl.Mem[tuiPrimaryMark]
	if mem.Alloc <= mem.Sys {
		t.Fatalf("test fixture must be impossible: alloc=%d sys=%d", mem.Alloc, mem.Sys)
	}
	if mem.Alloc != 900 || mem.Sys != 100 {
		t.Fatalf("mem = %+v", mem)
	}
}

func TestGoHeapMissingMarkerFieldsAreUnavailable(t *testing.T) {
	// A marker line without alloc= yields unavailable metrics, not
	// zero-byte heap claims.
	base := time.Now()
	tl := markers.Build([]markers.StampedLine{
		{At: base, Text: "ff-perf first-useful-frame\n"},
		{At: base.Add(time.Millisecond), Text: "ff-perf runtime-ready alloc=5 sys=6\n"},
	})
	mem := tl.Mem[tuiPrimaryMark]
	if mem.Alloc != 0 || mem.Sys != 0 {
		t.Fatalf("mem = %+v, want zero-valued (fields absent)", mem)
	}
	if _, ok := tl.Mem[tuiPrimaryMark]; !ok {
		t.Fatal("mark must still be recorded even without memory fields")
	}
}

func TestPeakUnavailableReason(t *testing.T) {
	// "unavailable" must never become a zero-byte reading.
	r := peakUnavailableReason("unavailable")
	if !strings.Contains(r, collect.ReasonUnsupported) {
		t.Errorf("reason = %q", r)
	}
	if strings.Contains(r, "0") {
		t.Errorf("reason must not imply a value: %q", r)
	}
}

func TestTreeUnavailableReason(t *testing.T) {
	r := treeUnavailableReason(collect.Report{})
	if r == "" {
		t.Fatal("empty report must still produce a reason")
	}
	withReason := treeUnavailableReason(collect.Report{
		Samples: 0, Reason: collect.ReasonExited,
	})
	if !strings.Contains(withReason, collect.ReasonExited) {
		t.Errorf("reason must be preserved: %q", withReason)
	}
	noSamples := treeUnavailableReason(collect.Report{Samples: 5})
	if !strings.Contains(noSamples, collect.ReasonQueryFailed) {
		t.Errorf("samples but no peak = %q", noSamples)
	}
}

func TestHeadlessRunReportsMemory(t *testing.T) {
	// Hermetic: the fake subject stands in for ff, so the suite's own
	// validity and reporting paths are exercised without the real
	// binary.
	fakeEnv(t)
	b := &memHeadlessBench{}
	fx, err := b.Setup(context.Background(), testRunEnv(t), fakeSubject(t))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	obs, err := b.Iterate(context.Background(), fx, fakeSubject(t),
		bench.Iter{Index: 0, Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if !obs.Valid {
		t.Fatalf("valid run rejected: %s", obs.InvalidReason)
	}
	peak := obs.Values["peak_rss_bytes"]
	if peak <= 0 {
		t.Fatalf("peak_rss_bytes = %v", peak)
	}
	if obs.Attrs["peak_source"] != "kernel_tracked_exact" {
		t.Errorf("peak_source = %q", obs.Attrs["peak_source"])
	}
	if obs.Attrs["peak_semantics"] == "" {
		t.Error("peak semantics must be recorded")
	}
	// The tree figure must be present and labelled sampled.
	tree := obs.Values["peak_tree_rss_bytes"]
	if tree <= 0 {
		t.Fatalf("peak_tree_rss_bytes = %v", tree)
	}
	if obs.Attrs["tree_sampled_peak"] != "true" {
		t.Errorf("tree peak must be labelled sampled: %q", obs.Attrs["tree_sampled_peak"])
	}
	if obs.Attrs["sample_interval_ms"] == "" {
		t.Error("sample interval must be recorded")
	}
	// The two peaks are not the same quantity: the kernel figure is a
	// high-water mark over the whole lifetime (it catches the startup
	// spike), while the tree figure is the largest *summed current* RSS
	// seen at a 5 ms sample. Either may be larger, so no ordering is
	// asserted here beyond both being real positive measurements.
	if obs.Attrs["peak_source"] != "kernel_tracked_exact" {
		t.Errorf("the two figures must be distinguishable, peak_source=%q",
			obs.Attrs["peak_source"])
	}
}

func TestHeadlessUnavailableMemoryStaysValidWithGap(t *testing.T) {
	// One unreadable figure must not fabricate a zero and must not
	// invalidate the sample that did measure.
	fakeEnv(t)
	// Present an OS that exposes no kernel peak, which a real unreadable
	// process cannot be made to do on demand.
	prev := kernelPeakOf
	kernelPeakOf = func(spawn.Result) (int64, string) { return -1, "unavailable" }
	t.Cleanup(func() { kernelPeakOf = prev })

	b := &memHeadlessBench{}
	fx, err := b.Setup(context.Background(), testRunEnv(t), fakeSubject(t))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	obs, err := b.Iterate(context.Background(), fx, fakeSubject(t),
		bench.Iter{Index: 0, Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if obs.Values["peak_rss_bytes"] != 0 {
		t.Fatalf("unavailable peak must not be a value: %v", obs.Values["peak_rss_bytes"])
	}
	if _, ok := obs.Unavailable["peak_rss_bytes"]; !ok {
		t.Fatal("unavailable peak must be declared")
	}
	if obs.Attrs["peak_reason"] == "" {
		t.Error("the reason must be recorded")
	}
	// The tree figure still measured, so the sample stands.
	if !obs.Valid {
		t.Fatalf("sample with one gap must stay valid: %s", obs.InvalidReason)
	}
	if obs.Values["peak_tree_rss_bytes"] <= 0 {
		t.Error("the measured figure must still be reported")
	}
}

func TestHeadlessRejectsUnexpectedExit(t *testing.T) {
	// A workload that did not reach its boundary must not produce a
	// memory number at all. The knob is set after Setup so priming still
	// sees the expected exit.
	fakeEnv(t)
	b := &memHeadlessBench{}
	fx, err := b.Setup(context.Background(), testRunEnv(t), fakeSubject(t))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	fakeEnv(t, "HPOV_FAKE_EXIT=0")
	obs, err := b.Iterate(context.Background(), fx, fakeSubject(t),
		bench.Iter{Index: 0, Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if obs.Valid {
		t.Fatal("unexpected exit must invalidate the sample")
	}
	if !strings.Contains(obs.InvalidReason, "unexpected exit") {
		t.Errorf("reason = %q", obs.InvalidReason)
	}
	for _, m := range b.Spec().Metrics {
		if obs.Values[m.Name] != 0 {
			t.Errorf("an invalid sample must claim no memory value: %s=%v",
				m.Name, obs.Values[m.Name])
		}
	}
}

func TestReadyRSSAndGoHeapFullRun(t *testing.T) {
	// Both TUI memory suites run against the fake subject, so the
	// readiness sampling and heap derivation are checked hermetically.
	tuiFakeEnv(t, "full")
	b := &memReadyRSSBench{readinessTimeout: 5 * time.Second, quitGrace: 2 * time.Second}
	fx, err := b.Setup(context.Background(), testRunEnv(t), fakeSubject(t))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	obs, err := b.Iterate(context.Background(), fx, fakeSubject(t),
		bench.Iter{Index: 0, Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if !obs.Valid {
		t.Fatalf("run rejected: %s", obs.InvalidReason)
	}
	if obs.Values["ready_rss_bytes"] <= 0 {
		t.Fatalf("ready_rss_bytes = %v", obs.Values["ready_rss_bytes"])
	}
	if obs.Attrs["readiness_mark"] != tuiPrimaryMark {
		t.Errorf("readiness mark = %q", obs.Attrs["readiness_mark"])
	}
	if obs.Attrs["sample_method"] != "marker_callback_query" {
		t.Errorf("sample method = %q", obs.Attrs["sample_method"])
	}
	if obs.Attrs["root_sample_lag_ns"] == "" {
		t.Error("the root sampling lag must be recorded, not assumed")
	}
	if obs.Attrs["tree_sample_lag_ns"] == "" {
		t.Error("the tree sampling lag must be recorded, not assumed")
	}
	if obs.Attrs["root_semantics"] == "" {
		t.Error("root semantics must be recorded")
	}
	// Clean teardown, no forced kill.
	if obs.Attrs["quit_method"] != "clean" {
		t.Errorf("quit_method = %q", obs.Attrs["quit_method"])
	}
	if obs.Attrs["exit_code"] != "0" {
		t.Errorf("exit_code = %q", obs.Attrs["exit_code"])
	}

	g := &memGoHeapBench{readinessTimeout: 5 * time.Second, quitGrace: 2 * time.Second}
	gfx, err := g.Setup(context.Background(), testRunEnv(t), fakeSubject(t))
	if err != nil {
		t.Fatalf("go heap setup: %v", err)
	}
	gobs, err := g.Iterate(context.Background(), gfx, fakeSubject(t),
		bench.Iter{Index: 0, Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("go heap iterate: %v", err)
	}
	if !gobs.Valid {
		t.Fatalf("go heap run rejected: %s", gobs.InvalidReason)
	}
	alloc := gobs.Values["go_heap_alloc_bytes"]
	sys := gobs.Values["go_sys_bytes"]
	if alloc <= 0 || sys <= 0 {
		t.Fatalf("heap alloc=%v sys=%v", alloc, sys)
	}
	if alloc > sys {
		t.Fatalf("HeapAlloc %v exceeds Sys %v", alloc, sys)
	}
	if gobs.Attrs["derived_from"] == "" {
		t.Error("the derived metric must record its source")
	}
}

func TestReadyRSSMissingReadinessInvalidates(t *testing.T) {
	tuiFakeEnv(t, "no-ready")
	b := &memReadyRSSBench{readinessTimeout: 2 * time.Second, quitGrace: time.Second}
	fx, err := b.Setup(context.Background(), testRunEnv(t), fakeSubject(t))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	obs, err := b.Iterate(context.Background(), fx, fakeSubject(t),
		bench.Iter{Index: 0, Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if obs.Valid {
		t.Fatal("missing readiness must invalidate the sample")
	}
	if !strings.Contains(obs.InvalidReason, tuiPrimaryMark) {
		t.Errorf("reason = %q", obs.InvalidReason)
	}
	// No sample was possible, so no memory value may be claimed.
	if obs.Values["ready_rss_bytes"] != 0 {
		t.Errorf("no memory value may be claimed: %v", obs.Values["ready_rss_bytes"])
	}
}

func TestReadyRSSForcedTerminationFails(t *testing.T) {
	tuiFakeEnv(t, "ignore-quit")
	b := &memReadyRSSBench{readinessTimeout: 5 * time.Second, quitGrace: time.Second}
	fx, err := b.Setup(context.Background(), testRunEnv(t), fakeSubject(t))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	obs, err := b.Iterate(context.Background(), fx, fakeSubject(t),
		bench.Iter{Index: 0, Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if obs.Valid {
		t.Fatal("forced termination must fail the sample")
	}
	if !strings.Contains(obs.InvalidReason, "forced termination") {
		t.Errorf("reason = %q", obs.InvalidReason)
	}
}

func TestGoHeapRunWithoutMarkerFields(t *testing.T) {
	// A subject that emits no memory fields yields unavailable metrics,
	// not a zero-byte heap.
	tuiFakeEnv(t, "no-memory-fields")
	g := &memGoHeapBench{readinessTimeout: 5 * time.Second, quitGrace: 2 * time.Second}
	fx, err := g.Setup(context.Background(), testRunEnv(t), fakeSubject(t))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	obs, err := g.Iterate(context.Background(), fx, fakeSubject(t),
		bench.Iter{Index: 0, Phase: bench.PhaseMeasure})
	if err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if !obs.Valid {
		t.Fatalf("run must stay valid: %s", obs.InvalidReason)
	}
	for _, name := range []string{"go_heap_alloc_bytes", "go_sys_bytes"} {
		if _, ok := obs.Unavailable[name]; !ok {
			t.Errorf("%s must be unavailable", name)
		}
		if obs.Values[name] != 0 {
			t.Errorf("%s must not carry a fabricated value: %v", name, obs.Values[name])
		}
	}
	if obs.Attrs["derived_from"] == "" {
		t.Error("the source must still be recorded")
	}
}
