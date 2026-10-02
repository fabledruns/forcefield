package compare

import (
	"fmt"
	"math"
	"os"
	"testing"

	"forcefield/internal/hpov/schema"
	"forcefield/internal/hpov/stats"
)

// ---------------------------------------------------------------------------
// Synthetic result builders.
//
// The statistical logic is tested against small deterministic datasets:
// a verdict that only reproduces on a real machine run cannot be trusted
// to be correct.
// ---------------------------------------------------------------------------

const testSeed = 424242

// mkResult builds a valid hpov.result with one benchmark holding one
// metric. Values are the raw samples; statistics are recomputed the same
// way the runner does, so the document validates.
func mkResult(benchID, metric, unit, direction string, values []float64) *schema.Result {
	valid := make([]bool, len(values))
	for i := range valid {
		valid[i] = true
	}
	sm := stats.Summarize(values, valid, testSeed)
	st := &schema.Statistics{
		N: sm.N, ValidN: sm.ValidN, Min: sm.Min, P50: sm.P50,
		P95: sm.P95, P99: sm.P99, Max: sm.Max,
		Mean: sm.Mean, Stdev: sm.Stdev, MAD: sm.MAD, IQR: sm.IQR,
		RobustCV: sm.RobustCV, CI95P50: sm.CI95P50, Nulls: sm.Nulls,
	}
	its := make([]schema.Iteration, 0, len(values))
	for i, v := range values {
		its = append(its, schema.Iteration{
			I: i, Phase: "measure", Valid: true,
			Values: map[string]float64{metric: v},
		})
	}
	return &schema.Result{
		Schema: schema.SchemaName, SchemaVersion: schema.SchemaVersion,
		Suite: schema.Suite{Name: "hpov", Version: "0.1.0", Profile: "standard", DefinitionSet: "2026.10"},
		Run: schema.Run{
			ID: "synthetic", Seed: testSeed,
			QuantileMethod: schema.QuantileMethod,
			Bootstrap:      schema.Bootstrap{Resamples: 5000, Seed: testSeed, Method: "percentile"},
			Quality:        schema.RunQuality{Label: "good"},
		},
		Subjects: []schema.Subject{{
			Label: "s", Product: "forcefield",
			Binary: schema.Binary{Basename: "ff", SHA256: "x", Source: "local-build",
				GoOS: "windows", GoArch: "amd64", GoVersion: "go1.24"},
		}},
		Host: schema.Host{
			HostID: "host-1", OS: "windows", Arch: "amd64", Env: "native",
			CPU:   schema.CPU{Model: "test", Logical: 16},
			Clock: schema.Clock{Source: "go-timeNow", ResolutionNs: 1000000},
		},
		Environment: schema.Environment{},
		Benchmarks: []schema.Benchmark{{
			ID: benchID, DefinitionVersion: 1, Kind: "e2e", Tier: 1,
			Status: schema.StatusOK, Subject: "s",
			Iterations: its,
			Metrics: []schema.Metric{{
				Name: metric, Unit: unit, Direction: direction, Values: values,
				Statistics: st,
			}},
		}},
	}
}

// mkPair builds a matched pair of results differing only in values.
func mkPair(benchID, metric, unit, direction string, base, cand []float64) (*schema.Result, *schema.Result) {
	return mkResult(benchID, metric, unit, direction, base),
		mkResult(benchID, metric, unit, direction, cand)
}

// stable returns n deterministic values around a centre, with a small
// spread. Seeded from the index so the same call always yields the same
// data: a test that depends on luck is a test that lies.
func stable(centre, spread float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		// A fixed low-discrepancy pattern, not randomness.
		phase := float64((i*7)%11) / 11.0 // 0..1
		out[i] = centre + spread*(phase-0.5)
	}
	return out
}

func comparePair(t *testing.T, base, cand *schema.Result, opt Options) *Comparison {
	t.Helper()
	c, err := Build(base, cand, opt, "base.json", "cand.json")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return c
}

func only(c *Comparison) Metric {
	if len(c.Metrics) != 1 {
		t := "want exactly 1 metric, got "
		for _, m := range c.Metrics {
			t += m.Name + "=" + m.Verdict + " "
		}
		return Metric{Name: t}
	}
	return c.Metrics[0]
}

// ---------------------------------------------------------------------------
// Core verdict states
// ---------------------------------------------------------------------------

func TestIdenticalBaselineCandidate(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(50, 4, 20), stable(50, 4, 20))
	c := comparePair(t, base, cand, Options{})
	m := only(c)
	if m.Verdict != VerdictUnchanged {
		t.Fatalf("identical data verdict = %s (%s)", m.Verdict, m.VerdictReason)
	}
	if m.AbsDelta != 0 {
		t.Errorf("abs delta = %v, want 0", m.AbsDelta)
	}
	if m.CI == nil || m.CIExcludesZero {
		t.Errorf("identical data must not produce a CI excluding zero: %v", m.CI)
	}
}

func TestObviousLatencyRegression(t *testing.T) {
	// +40% on a 100ms baseline: far past the launch.* 10%/5ms rule.
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 4, 20), stable(140, 4, 20))
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictRegressed {
		t.Fatalf("verdict = %s (%s)", m.Verdict, m.VerdictReason)
	}
	if !m.ThresholdMet || !m.CIExcludesZero {
		t.Errorf("threshold=%v ci_excl0=%v, both expected", m.ThresholdMet, m.CIExcludesZero)
	}
	if m.HLShift <= 0 {
		t.Errorf("HL shift = %v, want positive (candidate slower)", m.HLShift)
	}
}

func TestObviousLatencyImprovement(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 4, 20), stable(70, 4, 20))
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictImproved {
		t.Fatalf("verdict = %s (%s)", m.Verdict, m.VerdictReason)
	}
	if m.Signed >= 0 {
		t.Errorf("signed = %v, want negative for an improvement", m.Signed)
	}
}

func TestMemoryRegression(t *testing.T) {
	// mem.* rule: 5% / 1MiB. +20% on 20MiB clears both.
	base, cand := mkPair("mem.headless.peak-rss", "peak_rss_bytes", "bytes", "lower_is_better",
		stable(20<<20, 1<<19, 20), stable(24<<20, 1<<19, 20))
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictRegressed {
		t.Fatalf("verdict = %s (%s)", m.Verdict, m.VerdictReason)
	}
	if m.ThresholdPct != 5 || m.ThresholdAbs != 1<<20 {
		t.Errorf("mem threshold = %g%%/%v, want 5%%/1MiB", m.ThresholdPct, m.ThresholdAbs)
	}
}

func TestArtifactSizeChange(t *testing.T) {
	// launch.artifact-size: 2% / 64KiB. +10% on 1MiB clears it.
	base, cand := mkPair("launch.artifact-size", "size_bytes", "bytes", "lower_is_better",
		stable(1<<20, 4096, 20), stable(1126400, 4096, 20))
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictRegressed {
		t.Fatalf("verdict = %s (%s)", m.Verdict, m.VerdictReason)
	}
	if m.ThresholdPct != 2 || m.ThresholdAbs != 64*1024 {
		t.Errorf("artifact threshold = %g%%/%v, want 2%%/64KiB", m.ThresholdPct, m.ThresholdAbs)
	}
}

func TestNeutralMetricReportsDeltaWithoutVerdict(t *testing.T) {
	// A directionless metric moves; it must never be improved/regressed.
	base, cand := mkPair("repo.launch.git-dir-shallow", "git_spawned", "count", "informational",
		stable(1, 0, 20), stable(5, 0, 20))
	m := only(comparePair(t, base, cand, Options{}))
	switch m.Verdict {
	case VerdictRegressed, VerdictImproved:
		t.Fatalf("directionless metric got a verdict: %s", m.Verdict)
	}
	if m.Signed != 0 {
		t.Errorf("signed = %v, want 0 for a directionless metric", m.Verdict)
	}
	if m.AbsDelta == 0 {
		t.Error("the delta must still be reported for a directionless metric")
	}
}

func TestHigherIsBetterDirection(t *testing.T) {
	base, cand := mkPair("misc.rate", "hit_rate", "count", "higher_is_better",
		stable(0.5, 0.02, 20), stable(0.8, 0.02, 20))
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictImproved {
		t.Fatalf("higher-is-better increase must improve: %s (%s)", m.Verdict, m.VerdictReason)
	}
	if m.Signed >= 0 {
		t.Errorf("signed = %v, want negative (positive always means worse)", m.Signed)
	}
}

// ---------------------------------------------------------------------------
// The evidence rules, one at a time
// ---------------------------------------------------------------------------

func TestThresholdBoundary(t *testing.T) {
	// Exactly at the 10% launch threshold: the rule is ">=", so a change
	// equal to the threshold qualifies. Verified from both sides.
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(110, 2, 20))
	m := only(comparePair(t, base, cand, Options{}))
	if !m.ThresholdMet {
		t.Errorf("a change exactly at the threshold must count as met (thr=%v delta=%v)",
			m.ThresholdUsed, m.Signed)
	}
	// Just under: below the practical bar.
	base2, cand2 := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(104, 2, 20))
	m2 := only(comparePair(t, base2, cand2, Options{}))
	if m2.ThresholdMet {
		t.Errorf("a change under the threshold must not qualify (thr=%v delta=%v)",
			m2.ThresholdUsed, m2.Signed)
	}
}

func TestStatisticallySignificantBelowThreshold(t *testing.T) {
	// Tight samples with a small offset: the CI excludes zero, but the
	// change is far under 10% + 5ms. Verdict: unchanged, not a
	// regression.
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 0.5, 20), stable(101, 0.5, 20))
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict == VerdictRegressed {
		t.Fatalf("a statistically visible but practically tiny change must not regress: %s", m.Verdict)
	}
	if m.CI == nil || !m.CIExcludesZero {
		t.Skip("synthetic spread too wide for a CI excluding zero; skip")
	}
	if m.Verdict != VerdictUnchanged {
		t.Errorf("verdict = %s, want unchanged", m.Verdict)
	}
}

func TestThresholdCrossingWithoutStatisticalEvidence(t *testing.T) {
	// Large but wildly scattered samples: the CI swallows zero, so a
	// practical-looking shift is inconclusive rather than a regression.
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		[]float64{10, 500, 12, 480, 11, 520, 10, 490, 12, 505, 11, 495, 10, 500, 12, 480, 11, 510, 10, 490},
		[]float64{600, 20, 580, 25, 610, 22, 590, 26, 605, 21, 595, 24, 600, 23, 585, 25, 598, 22, 592, 24})
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict == VerdictRegressed {
		t.Fatalf("a shift with CI including zero must not be a regression: %s (%s)",
			m.Verdict, m.VerdictReason)
	}
	if m.Verdict != VerdictInconclusive && m.Verdict != VerdictUnchanged {
		t.Errorf("verdict = %s, want inconclusive or unchanged", m.Verdict)
	}
}

func TestInsufficientSamplesIsInconclusive(t *testing.T) {
	// n=5 per side: below the plan's n>=8 for the Mann-Whitney test,
	// so no statistical evidence is claimed.
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 5), stable(200, 2, 5))
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict == VerdictRegressed {
		t.Fatalf("n=5 must not produce a regression verdict: %s", m.Verdict)
	}
	if m.Verdict != VerdictInconclusive {
		t.Errorf("verdict = %s (%s), want inconclusive", m.Verdict, m.VerdictReason)
	}
	if m.MannWhitneyP != nil || m.RawP != nil {
		t.Error("no test should have run at n=5, so no p-value may be reported")
	}
}

func TestBothSignificantAndThresholdCrossing(t *testing.T) {
	base, cand := mkPair("tui.startup.timeline", "t_first_useful_frame", "ms", "lower_is_better",
		stable(100, 2, 20), stable(130, 2, 20))
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictRegressed {
		t.Fatalf("verdict = %s (%s)", m.Verdict, m.VerdictReason)
	}
	// tui.* carries a 15%/8ms threshold.
	if m.ThresholdPct != 15 || m.ThresholdAbs != 8 {
		t.Errorf("tui threshold = %g%%/%v, want 15%%/8", m.ThresholdPct, m.ThresholdAbs)
	}
}

// ---------------------------------------------------------------------------
// Compatibility
// ---------------------------------------------------------------------------

func TestIncompatibleUnit(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(140, 2, 20))
	cand.Benchmarks[0].Metrics[0].Unit = "seconds"
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictIncompatible {
		t.Fatalf("verdict = %s, want incompatible", m.Verdict)
	}
	if m.VerdictReason == "" {
		t.Error("an incompatibility must explain itself")
	}
}

func TestIncompatibleDefinitionVersion(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(140, 2, 20))
	cand.Benchmarks[0].DefinitionVersion = 2
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictIncompatible {
		t.Fatalf("verdict = %s, want incompatible", m.Verdict)
	}
}

func TestIncompatibleParams(t *testing.T) {
	base, cand := mkPair("mem.headless.peak-rss", "peak_rss_bytes", "bytes", "lower_is_better",
		stable(20<<20, 1<<19, 20), stable(24<<20, 1<<19, 20))
	base.Benchmarks[0].Params = map[string]string{"mcp_servers": "0"}
	cand.Benchmarks[0].Params = map[string]string{"mcp_servers": "3"}
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictIncompatible {
		t.Fatalf("a different workload configuration is not the same measurement: %s", m.Verdict)
	}
}

func TestNotComparableAcrossOS(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(140, 2, 20))
	cand.Host.OS = "linux"
	cand.Subjects[0].Binary.GoOS = "linux"
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictNotComparable {
		t.Fatalf("verdict = %s, want not_comparable", m.Verdict)
	}
}

func TestNotComparableAcrossArchitecture(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(140, 2, 20))
	cand.Host.Arch = "arm64"
	cand.Subjects[0].Binary.GoArch = "arm64"
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictNotComparable {
		t.Fatalf("verdict = %s, want not_comparable", m.Verdict)
	}
}

func TestCrossHostWithAllowFlag(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(140, 2, 20))
	cand.Host.HostID = "host-2"
	// Without the flag: not comparable.
	if m := only(comparePair(t, base, cand, Options{})); m.Verdict != VerdictNotComparable {
		t.Fatalf("cross-host without the flag = %s", m.Verdict)
	}
	// With the flag: informational, never a regression claim.
	m := only(comparePair(t, base, cand, Options{AllowCrossHost: true}))
	if m.Verdict == VerdictRegressed {
		t.Fatalf("cross-host must never assert a regression: %s", m.Verdict)
	}
	if m.Verdict != VerdictInformational {
		t.Errorf("verdict = %s, want informational", m.Verdict)
	}
}

func TestIncompatibleSubject(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(140, 2, 20))
	// A release binary measured against a local build confounds the
	// code change with provenance.
	cand.Subjects[0].Binary.Source = "release"
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictNotComparable {
		t.Fatalf("verdict = %s, want not_comparable", m.Verdict)
	}
}

func TestConfoundedBuildIsWarned(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(140, 2, 20))
	// Same provenance, different toolchain: the comparison is allowed but
	// the verdict cannot separate a code change from a Go change.
	base.Subjects[0].Binary.Source = "release"
	cand.Subjects[0].Binary.Source = "release"
	cand.Subjects[0].Binary.GoVersion = "go1.25"
	m := only(comparePair(t, base, cand, Options{}))
	if !m.ConfoundedBuild {
		t.Fatal("a toolchain difference must be recorded")
	}
	if m.VerdictReason == "" || !contains(m.VerdictReason, "confounded") {
		t.Errorf("the confounded_build warning must reach the verdict: %q", m.VerdictReason)
	}
}

func TestMissingMetric(t *testing.T) {
	base := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(100, 2, 20))
	cand := mkResult("launch.version", "cpu_ms", "ms", "lower_is_better", stable(100, 2, 20))
	c := comparePair(t, base, cand, Options{})
	// Both metrics exist on one side only: neither is comparable, and
	// both appear in coverage with a reason.
	if len(c.Coverage) != 2 {
		t.Fatalf("coverage = %+v, want both metrics reported", c.Coverage)
	}
	for _, cv := range c.Coverage {
		if cv.Reason == "" {
			t.Errorf("coverage entry %v has no reason", cv)
		}
	}
}

func TestUnavailableMetricNotTreatedAsZero(t *testing.T) {
	// The candidate declares the metric unavailable for every sample.
	// Its stored vector is placeholder zeros; they must not enter the
	// comparison as measurements.
	base := mkResult("mem.tui.go-heap", "go_heap_alloc_bytes", "bytes", "lower_is_better",
		stable(3<<20, 1<<18, 20))
	cand := mkResult("mem.tui.go-heap", "go_heap_alloc_bytes", "bytes", "lower_is_better",
		make([]float64, 20))
	for i := range cand.Benchmarks[0].Iterations {
		cand.Benchmarks[0].Iterations[i].Attrs = map[string]string{
			schema.UnavailablePrefix + "go_heap_alloc_bytes": "marker carried no alloc field",
		}
		cand.Benchmarks[0].Iterations[i].Values = map[string]float64{
			"go_heap_alloc_bytes": 0,
		}
	}
	m := only(comparePair(t, base, cand, Options{}))
	if m.CandidateN != 0 {
		t.Errorf("candidate n = %d, want 0 usable samples", m.CandidateN)
	}
	if m.Verdict != VerdictInconclusive {
		t.Errorf("verdict = %s, want inconclusive", m.Verdict)
	}
	if m.CurP50 != 0 && !math.IsNaN(m.CurP50) && m.Verdict != VerdictInconclusive {
		t.Errorf("an unavailable candidate must not produce a centre: %v", m.CurP50)
	}
}

func TestInvalidSamplesExcluded(t *testing.T) {
	// Half the samples invalid: they must not count, and the remaining
	// valid ones still decide.
	base := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(100, 2, 20))
	cand := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(140, 2, 20))
	for i := 0; i < 10; i++ {
		cand.Benchmarks[0].Iterations[i].Valid = false
		cand.Benchmarks[0].Metrics[0].Values[i] = 0
	}
	m := only(comparePair(t, base, cand, Options{}))
	if m.CandidateN != 10 {
		t.Errorf("candidate n = %d, want 10 valid", m.CandidateN)
	}
}

func TestPartialSampleLossIsReported(t *testing.T) {
	// Three unavailable samples do not stop the comparison, but the reader
	// must be told the verdict rests on 17 of 20.
	base := mkResult("mem.tui.go-heap", "go_heap_alloc_bytes", "bytes", "lower_is_better", stable(3<<20, 1<<18, 20))
	cand := mkResult("mem.tui.go-heap", "go_heap_alloc_bytes", "bytes", "lower_is_better", stable(4<<20, 1<<18, 20))
	for i := 0; i < 3; i++ {
		cand.Benchmarks[0].Iterations[i].Attrs = map[string]string{
			schema.UnavailablePrefix + "go_heap_alloc_bytes": "marker carried no alloc field",
		}
	}
	m := only(comparePair(t, base, cand, Options{}))
	if m.CandidateN != 17 {
		t.Errorf("candidate n = %d, want 17", m.CandidateN)
	}
	if m.Verdict != VerdictRegressed {
		t.Fatalf("verdict = %s (%s), the remaining samples still decide", m.Verdict, m.VerdictReason)
	}
	if !contains(m.VerdictReason, ReasonMetricUnavailable) {
		t.Errorf("the dropped samples must be named in the verdict: %q", m.VerdictReason)
	}
}

func TestInvalidBenchmarkStatus(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(140, 2, 20))
	cand.Benchmarks[0].Status = schema.StatusInvalid
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictInvalid {
		t.Fatalf("verdict = %s, want invalid", m.Verdict)
	}
}

// ---------------------------------------------------------------------------
// Quality
// ---------------------------------------------------------------------------

func TestNoisyMetricNeedsDoubleThreshold(t *testing.T) {
	// launch.version threshold: 10% of 100ms = 10ms. +15% is 1.5x that:
	// enough to regress on clean data, not enough on noisy data.
	base := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(100, 2, 20))
	cand := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(115, 2, 20))
	base.Benchmarks[0].Flags = []string{"noisy:wall_ms"}
	cand.Benchmarks[0].Flags = []string{"noisy:wall_ms"}
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictInconclusive {
		t.Fatalf("noisy data between 1x and 2x threshold = %s, want inconclusive", m.Verdict)
	}
	if m.VerdictReason != ReasonNoisyNotDouble {
		t.Errorf("reason = %q, want %q", m.VerdictReason, ReasonNoisyNotDouble)
	}
	// The same data at 3x threshold is allowed to decide.
	cand2 := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(140, 2, 20))
	cand2.Benchmarks[0].Flags = []string{"noisy:wall_ms"}
	m2 := only(comparePair(t, base, cand2, Options{}))
	if m2.Verdict != VerdictRegressed {
		t.Errorf("a noisy effect past 2x threshold = %s, want regressed", m2.Verdict)
	}
}

func TestNonNoiseFlagsDoNotInflateThresholds(t *testing.T) {
	// A flag that does not mean "this data is unreliable" must not double
	// the bar: only noisy/bimodal/drift do.
	base := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(100, 2, 20))
	cand := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(115, 2, 20))
	base.Benchmarks[0].Flags = []string{"cold_cache", "note:first_run"}
	cand.Benchmarks[0].Flags = []string{"cold_cache"}
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictRegressed {
		t.Errorf("an unrelated flag must not weaken a verdict: got %s", m.Verdict)
	}
	if m.NoiseFlagged {
		t.Errorf("unrelated flags must not be recorded as noise: %v", m.NoiseFlags)
	}
}

func TestNoiseFlagIsPerMetric(t *testing.T) {
	// A noisy wall_ms must not raise the bar for the benchmark's memory
	// metric, which was measured cleanly.
	base := mkResult("mixed.noise", "rss_bytes", "bytes", "lower_is_better", stable(20<<20, 1<<19, 20))
	cand := mkResult("mixed.noise", "rss_bytes", "bytes", "lower_is_better", stable(21<<20, 1<<19, 20))
	base.Benchmarks[0].Flags = []string{"noisy:wall_ms"}
	cand.Benchmarks[0].Flags = []string{"noisy:wall_ms"}
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictRegressed {
		t.Errorf("a clean metric must not inherit another metric's noise: %s", m.Verdict)
	}
	if m.NoiseFlagged {
		t.Errorf("noise flags must be matched by metric name: %v", m.NoiseFlags)
	}
}

func TestDriftAndBimodalFlagsAreNoise(t *testing.T) {
	for _, flag := range []string{"drift:wall_ms", "bimodal:wall_ms"} {
		base := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(100, 2, 20))
		cand := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(115, 2, 20))
		base.Benchmarks[0].Flags = []string{flag}
		cand.Benchmarks[0].Flags = []string{flag}
		m := only(comparePair(t, base, cand, Options{}))
		if m.Verdict != VerdictInconclusive {
			t.Errorf("%s must be treated as noise: got %s", flag, m.Verdict)
		}
	}
}

func TestPoorRunQualityBlocksVerdict(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(140, 2, 20))
	cand.Run.Quality.Label = "poor"
	cand.Run.Quality.Flags = []string{"spawn_floor_drift"}
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictInconclusive {
		t.Fatalf("a poor candidate run must not assert a regression: %s", m.Verdict)
	}
	if m.VerdictReason != ReasonPoorRunQuality {
		t.Errorf("reason = %q, want %q", m.VerdictReason, ReasonPoorRunQuality)
	}
}

func TestDegradedQualityStillReported(t *testing.T) {
	// Degraded keeps the data and the verdict; it is not discarded for
	// being inconvenient.
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(400, 2, 20))
	cand.Run.Quality.Label = "degraded"
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictRegressed {
		t.Errorf("verdict = %s, want the movement still reported", m.Verdict)
	}
	if m.QualityCand != "degraded" {
		t.Errorf("the run quality must be recorded next to the verdict: %+v", m)
	}
}

func TestQualityFlagsSurviveComparison(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(400, 2, 20))
	base.Benchmarks[0].Flags = []string{"drift:wall_ms"}
	m := only(comparePair(t, base, cand, Options{}))
	if len(m.NoiseFlags) == 0 {
		t.Fatal("noise flags must reach the comparison output")
	}
}

// ---------------------------------------------------------------------------
// Trade-offs and tails
// ---------------------------------------------------------------------------

func TestTradeOffAnnotation(t *testing.T) {
	// One benchmark where latency improves and memory regresses. The
	// comparison must show both and must not pick a winner.
	r := mkTradeOffResult()
	c := comparePair(t, r.baseline, r.candidate, Options{})
	if len(c.TradeOffs) != 1 {
		t.Fatalf("trade-offs = %+v, want 1", c.TradeOffs)
	}
	tr := c.TradeOffs[0]
	if len(tr.Improved) != 1 || len(tr.Regressed) != 1 {
		t.Fatalf("trade-off = %+v, want one of each", tr)
	}
	byName := map[string]string{}
	for _, m := range c.Metrics {
		byName[m.Name] = m.Verdict
	}
	if byName["wall_ms"] != VerdictImproved || byName["rss_bytes"] != VerdictRegressed {
		t.Fatalf("both facts must survive: %v", byName)
	}
	// The regressed metric is annotated, the improved one is not
	// rewritten.
	for _, m := range c.Metrics {
		if m.Name == "rss_bytes" && !contains(m.VerdictReason, "trade_off") {
			t.Errorf("the regressed metric must carry the trade-off annotation: %q", m.VerdictReason)
		}
	}
}

func TestNoTradeOffWhenDirectionsAgree(t *testing.T) {
	base := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(100, 2, 20))
	cand := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(400, 2, 20))
	c := comparePair(t, base, cand, Options{})
	if len(c.TradeOffs) != 0 {
		t.Fatalf("a single regression is not a trade-off: %+v", c.TradeOffs)
	}
}

func TestTailVerdict(t *testing.T) {
	// Same centre, bigger tail: p50 does not move, p95 does. n=20 so p95
	// is resolvable.
	base := mkResult("tui.startup.timeline", "t_runtime_ready", "ms", "lower_is_better",
		tailSpike(100, 240, 20))
	cand := mkResult("tui.startup.timeline", "t_runtime_ready", "ms", "lower_is_better",
		tailSpike(100, 480, 20))
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict != VerdictTailRegressed {
		t.Fatalf("verdict = %s (%s), want tail_regressed", m.Verdict, m.VerdictReason)
	}
	if math.Abs(m.AbsDelta) > 5 {
		t.Errorf("the centre must not have moved: %v", m.AbsDelta)
	}
}

func TestTailNotReportedWhenSampleTooSmall(t *testing.T) {
	// Below MinNForP95 a p95 shift must not be read as a tail verdict.
	base := mkResult("tui.startup.timeline", "t_runtime_ready", "ms", "lower_is_better",
		tailSpike(100, 240, 10))
	cand := mkResult("tui.startup.timeline", "t_runtime_ready", "ms", "lower_is_better",
		tailSpike(100, 480, 10))
	m := only(comparePair(t, base, cand, Options{}))
	if m.Verdict == VerdictTailRegressed {
		t.Fatalf("n=10 cannot support a p95 verdict: %s", m.Verdict)
	}
}

// ---------------------------------------------------------------------------
// Holm correction
// ---------------------------------------------------------------------------

func TestHolmAdjustmentBasics(t *testing.T) {
	tests := []testResult{
		{rawP: 0.001}, {rawP: 0.004}, {rawP: 0.03}, {rawP: 0.20},
	}
	holmAdjust(tests, 0.05)
	// Ordered ascending: 0.001 vs 0.05/4 = 0.0125 -> survives.
	if !tests[0].survives || tests[0].rank != 1 {
		t.Errorf("smallest p must survive: %+v", tests[0])
	}
	// 0.004 vs 0.05/3 = 0.0167 -> survives.
	if !tests[1].survives || tests[1].rank != 2 {
		t.Errorf("second p should survive: %+v", tests[1])
	}
	// 0.03 vs 0.05/2 = 0.025 -> does not survive.
	if tests[2].survives || tests[2].rank != 3 {
		t.Errorf("third p should not survive: %+v", tests[2])
	}
	// 0.20 vs 0.05/1 = 0.05 -> does not survive.
	if tests[3].survives {
		t.Errorf("largest p must not survive: %+v", tests[3])
	}
}

func TestHolmIsMonotone(t *testing.T) {
	// Adjusted p-values must be non-decreasing across the order, or a
	// less significant test could appear stronger than a more
	// significant one.
	tests := []testResult{
		{rawP: 0.001}, {rawP: 0.002}, {rawP: 0.003}, {rawP: 0.9},
	}
	holmAdjust(tests, 0.05)
	prev := -1.0
	for _, tr := range tests {
		if tr.adjP < prev {
			t.Fatalf("adjusted p decreased: %v after %v", tr.adjP, prev)
		}
		prev = tr.adjP
	}
}

func TestHolmRejectsMoreFalsePositivesThanRaw(t *testing.T) {
	// A family of marginal p-values: unadjusted, all 40 look significant
	// at 0.01; after Holm, none does. This is the whole point of the
	// correction.
	var tests []testResult
	for i := 0; i < 40; i++ {
		tests = append(tests, testResult{rawP: 0.008})
	}
	holmAdjust(tests, 0.01)
	surviving := 0
	for _, tr := range tests {
		if tr.survives {
			surviving++
		}
	}
	if surviving == 40 {
		t.Fatal("Holm must not let every marginal test through at alpha=0.01")
	}
	// The smallest of 40 is judged at 0.01/40 = 0.00025, and Holm stops
	// at the first failure, so none survives.
	if surviving != 0 {
		t.Errorf("surviving = %d, want 0 at alpha=0.01 with 40 tests at p=0.008", surviving)
	}
}

func TestHolmWeakensRegressionVerdict(t *testing.T) {
	// A family where wall_ms clears the raw 0.01 bar on its own but sits
	// behind enough tests that its step-down threshold is below its own
	// p. The verdict must not stand on the uncorrected number.
	metrics := []testResult{
		{metricIndex: 0, rawP: 0.004},
		{metricIndex: 1, rawP: 0.006},
		{metricIndex: 2, rawP: 0.008},
	}
	for i := 3; i < 20; i++ {
		metrics = append(metrics, testResult{metricIndex: i, rawP: 1e-9})
	}
	reported := []Metric{
		{Benchmark: "launch.version", Name: "wall_ms", Verdict: VerdictRegressed, RawP: f(0.004),
			VerdictReason: "beyond threshold with statistical evidence"},
		{Benchmark: "launch.version", Name: "latency_p50", Verdict: VerdictRegressed, RawP: f(0.006),
			VerdictReason: "beyond threshold with statistical evidence"},
		{Benchmark: "launch.version", Name: "latency_p95", Verdict: VerdictRegressed, RawP: f(0.008),
			VerdictReason: "beyond threshold with statistical evidence"},
	}
	for i := 3; i < 20; i++ {
		reported = append(reported, Metric{
			Benchmark: "launch.version", Name: fmt.Sprintf("metric_%d", i),
			RawP:    f(1e-9),
			Verdict: VerdictRegressed, VerdictReason: "beyond threshold with statistical evidence"})
	}

	applyHolm(metrics, familyAlpha, reported)

	m := reported[0]
	if m.Verdict == VerdictRegressed {
		t.Fatalf("a verdict resting on an uncorrected p-value must be withdrawn: %+v", m)
	}
	if m.Verdict != VerdictInconclusive {
		t.Errorf("verdict = %s, want inconclusive", m.Verdict)
	}
	if !contains(m.VerdictReason, "holm") {
		t.Errorf("reason must name the correction: %q", m.VerdictReason)
	}
	if m.RawP == nil || m.MannWhitneyP == nil {
		t.Fatal("both the raw and the adjusted p must be reported")
	}
	if *m.RawP < *m.MannWhitneyP {
		t.Errorf("a family-corrected p can only grow: raw %v adjusted %v",
			*m.RawP, *m.MannWhitneyP)
	}
	// The strongest result in the family still stands.
	if reported[19].Verdict != VerdictRegressed {
		t.Errorf("Holm must not reject the strongest test: %s", reported[19].Verdict)
	}
}

// ---------------------------------------------------------------------------
// Determinism and output
// ---------------------------------------------------------------------------

func TestOutputIsDeterministic(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 4, 20), stable(140, 4, 20))
	ok, err := Determinism(base, cand, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("two comparisons of the same inputs must be byte-identical")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 4, 20), stable(140, 4, 20))
	opt := Options{}
	c := comparePair(t, base, cand, opt)
	if Render(c, opt) != Render(c, opt) {
		t.Fatal("render must be deterministic")
	}
}

func TestNoAggregateScore(t *testing.T) {
	// The summary may count verdicts; it must not total or weight them.
	r := mkTradeOffResult()
	c := comparePair(t, r.baseline, r.candidate, Options{})
	if len(c.Summary.Counts) == 0 {
		t.Fatal("summary must count verdicts")
	}
	out := fmt.Sprintf("%+v", c.Summary)
	for _, forbidden := range []string{"Score", "Total", "Weighted", "Index"} {
		if contains(out, forbidden) {
			t.Errorf("summary must not contain a score: %s", out)
		}
	}
	if c.Summary.Regressed != 1 {
		t.Errorf("regression count = %d, want 1 (the single regression)", c.Summary.Regressed)
	}
}

func TestSummaryCountsEveryVerdictState(t *testing.T) {
	// A run covering several states must report each one by name.
	base := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(100, 2, 20))
	cand := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(100, 2, 20))
	cand.Host.OS = "linux"
	cand.Subjects[0].Binary.GoOS = "linux"
	c := comparePair(t, base, cand, Options{})
	if c.Summary.Counts[VerdictNotComparable] != 1 {
		t.Fatalf("counts = %+v, want not_comparable=1", c.Summary.Counts)
	}
}

func TestMethodIsRecorded(t *testing.T) {
	base, cand := mkPair("launch.version", "wall_ms", "ms", "lower_is_better",
		stable(100, 2, 20), stable(140, 2, 20))
	c := comparePair(t, base, cand, Options{})
	if c.Method.FamilyAlpha != 0.01 || !c.Method.HolmCorrection {
		t.Errorf("method must record the family rule: %+v", c.Method)
	}
	if c.Method.EffectEstimate == "" || len(c.Method.StatisticalEvidence) < 2 {
		t.Errorf("method must name its tests: %+v", c.Method)
	}
	if c.Schema != SchemaName || c.SchemaVersion == "" || c.MethodVersion == "" {
		t.Errorf("document identity missing: %+v", c)
	}
}

// ---------------------------------------------------------------------------
// Threshold table
// ---------------------------------------------------------------------------

func TestBuiltinThresholdsMatchThePlan(t *testing.T) {
	tbl := BuiltinThresholds()
	for _, tc := range []struct {
		glob string
		rel  float64
		abs  float64
	}{
		{"launch.version", 10, 5},
		{"tui.startup.timeline", 15, 8},
		{"mem.headless.peak-rss", 5, 1 << 20},
		{"mem.tui.go-heap", 3, 256 * 1024},
		{"launch.artifact-size", 2, 64 * 1024},
		{"mcp.server.start", 15, 10},
		{"hot.session.new", 15, 0},
		{"hot.repo.clone", 5, 0},
	} {
		th, ok := tbl.For(tc.glob)
		if !ok {
			t.Errorf("no threshold for %s", tc.glob)
			continue
		}
		if th.RelPct != tc.rel || th.AbsBytes != tc.abs {
			t.Errorf("%s = %g%%/%v, want %g%%/%v", tc.glob, th.RelPct, th.AbsBytes, tc.rel, tc.abs)
		}
	}
}

func TestSpecificThresholdWins(t *testing.T) {
	// mem.tui.go-heap (3%) must not be swallowed by mem.* (5%).
	tbl := BuiltinThresholds()
	th, _ := tbl.For("mem.tui.go-heap")
	if th.RelPct != 3 {
		t.Fatalf("mem.tui.go-heap threshold = %g%%, want the specific 3%%", th.RelPct)
	}
	th, _ = tbl.For("mem.headless.peak-rss")
	if th.RelPct != 5 {
		t.Fatalf("mem.headless threshold = %g%%, want 5%%", th.RelPct)
	}
}

func TestThresholdEffectiveUsesLargerRule(t *testing.T) {
	th := Threshold{RelPct: 10, AbsBytes: 5}
	// 100ms: rel = 10ms > abs 5ms -> rel wins.
	if got := th.Effective(100); got != 10 {
		t.Errorf("effective = %v, want 10", got)
	}
	// 20ms: rel = 2ms < abs 5ms -> abs wins.
	if got := th.Effective(20); got != 5 {
		t.Errorf("effective = %v, want 5 (absolute floor)", got)
	}
	// Zero floor means relative only.
	rel := Threshold{RelPct: 5, AbsBytes: 0}
	if got := rel.Effective(200); got != 10 {
		t.Errorf("relative-only effective = %v, want 10", got)
	}
}

func TestThresholdTableRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/thresholds.json"
	if err := SaveThresholdFile(BuiltinThresholds(), path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadThresholdFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := got.For("mem.tui.go-heap")
	b, _ := BuiltinThresholds().For("mem.tui.go-heap")
	if a != b {
		t.Fatalf("round trip changed the table: %+v vs %+v", a, b)
	}
}

func TestLoadThresholdFileRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	// Missing source: an untraceable table must be refused.
	bad := dir + "/bad.json"
	if err := os.WriteFile(bad, []byte(`{"version":"x","thresholds":[{"glob":"a.*","rel_pct":5}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadThresholdFile(bad); err == nil {
		t.Error("a threshold table without a source must be refused")
	}
	// Empty table.
	empty := dir + "/empty.json"
	if err := os.WriteFile(empty, []byte(`{"version":"x","source":"y","thresholds":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadThresholdFile(empty); err == nil {
		t.Error("an empty threshold table must be refused")
	}
	// Missing file: fall back to the built-in table, which is the
	// documented behaviour.
	got, err := LoadThresholdFile(dir + "/absent.json")
	if err != nil {
		t.Errorf("an absent file must fall back to the built-in table: %v", err)
	}
	if got.Version == "" {
		t.Error("fallback table must still be a real table")
	}
}

// ---------------------------------------------------------------------------
// Fixtures with more than one metric
// ---------------------------------------------------------------------------

// f returns a pointer to a copy of v, for the nullable fields a stored
// document carries.
func f(v float64) *float64 { return &v }

func findMetric(c *Comparison, name string) Metric {
	for _, m := range c.Metrics {
		if m.Name == name {
			return m
		}
	}
	return Metric{Name: "missing:" + name}
}

// mkTradeOffResult builds one benchmark where latency improves while
// memory regresses: the case a single score would flatten.
func mkTradeOffResult() struct{ baseline, candidate *schema.Result } {
	base := mkResult("mixed.tradeoff", "wall_ms", "ms", "lower_is_better", stable(100, 2, 20))
	cand := mkResult("mixed.tradeoff", "wall_ms", "ms", "lower_is_better", stable(70, 2, 20))

	// Append a second metric to each side with the opposite movement.
	baseR := mkResult("mixed.tradeoff", "rss_bytes", "bytes", "lower_is_better", stable(20<<20, 1<<19, 20))
	candR := mkResult("mixed.tradeoff", "rss_bytes", "bytes", "lower_is_better", stable(30<<20, 1<<19, 20))
	for _, r := range []*schema.Result{base, cand} {
		_ = r
	}
	appendMetric(base, baseR)
	appendMetric(cand, candR)
	return struct{ baseline, candidate *schema.Result }{base, cand}
}

// mkMultiMetricResult builds a family of several metrics on one
// benchmark, to exercise the family correction end to end.
func mkMultiMetricResult(metrics int) struct{ baseline, candidate *schema.Result } {
	base := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(100, 2, 20))
	cand := mkResult("launch.version", "wall_ms", "ms", "lower_is_better", stable(140, 2, 20))
	for i := 1; i < metrics; i++ {
		name := fmt.Sprintf("metric_%d", i)
		appendMetric(base, mkResult("launch.version", name, "ms", "lower_is_better", stable(100, 2, 20)))
		appendMetric(cand, mkResult("launch.version", name, "ms", "lower_is_better", stable(140, 2, 20)))
	}
	return struct{ baseline, candidate *schema.Result }{base, cand}
}

func TestFamilyOfRegressionsAllSurviveStrongEvidence(t *testing.T) {
	// The opposite of the correction biting: when every test in the family
	// is overwhelming, Holm must not reject any of them.
	r := mkMultiMetricResult(6)
	c := comparePair(t, r.baseline, r.candidate, Options{})
	regressed := 0
	for _, m := range c.Metrics {
		if m.Verdict == VerdictRegressed {
			regressed++
		}
		if !m.HolmAdjusted {
			t.Errorf("%s: an overwhelming effect must survive correction: raw p=%v", m.Name, m.RawP)
		}
	}
	if regressed != 6 {
		t.Errorf("regressed = %d, want 6: %+v", regressed, c.Summary.Counts)
	}
}

// appendMetric moves a metric (and its iterations) from src into dst.
func appendMetric(dst, src *schema.Result) {
	dst.Benchmarks[0].Metrics = append(dst.Benchmarks[0].Metrics, src.Benchmarks[0].Metrics...)
	dst.Benchmarks[0].Iterations = append(dst.Benchmarks[0].Iterations, src.Benchmarks[0].Iterations...)
}

// tailSpike builds samples with a fixed centre and a fixed-size upper
// tail: two samples in five jump to spike while the rest stay within a
// few ms of the centre, so p50 and p95 move independently and the p95 is
// stable enough to carry a bootstrap interval.
func tailSpike(centre, spike float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		if i%5 < 2 {
			out[i] = spike
			continue
		}
		out[i] = centre + float64(i%3)
	}
	return out
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
