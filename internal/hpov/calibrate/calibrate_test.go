package calibrate

import (
	"os"
	"testing"

	"forcefield/internal/hpov/compare"
)

// These tests exercise the campaign accounting with hand-built trial
// records. Nothing here measures a machine: a test that depended on real
// timing would test the host, not the accounting, and would fail on a
// quiet machine in exactly the way a regression gate should not.

func sample(benchmark, name, verdict, reason string) MetricSample {
	return MetricSample{
		Benchmark: benchmark, Name: name, Unit: "ms",
		Verdict: verdict, Reason: reason,
		BaselineN: 30, CandidateN: 30,
		BaseP50: 100, CurP50: 100,
		ThresholdPct: 10, ThresholdAbs: 5, ThresholdUsed: 10,
	}
}

func withRel(m MetricSample, pct float64) MetricSample {
	p := pct
	m.RelDeltaPct = &p
	return m
}

func trial(quality string, metrics ...MetricSample) Trial {
	return Trial{
		Index: 0, Seed: 424242, RunID: "run-0",
		Quality:         Quality{Label: quality},
		QualityRejected: quality == "poor",
		BenchmarkErrors: false,
		Identical:       true,
		SubjectHashes:   [2]string{"abc", "abc"},
		Metrics:         metrics,
	}
}

// An identical subject compared against itself must not manufacture a
// regression out of a zero difference: the interval contains zero and the
// p-value is 1, so nothing clears the threshold.
func TestIdenticalSubjectsProduceNoRegression(t *testing.T) {
	if isRegression(compare.VerdictUnchanged) {
		t.Fatal("unchanged must never be a regression")
	}
	if eligible(compare.VerdictUnchanged) != true {
		t.Fatal("unchanged is a decision, so it belongs in the denominator")
	}
	// The shape a self-comparison produces: zero delta, CI [0,0], p=1.
	m := sample("launch.version", "wall_ms", compare.VerdictUnchanged, "below practical threshold")
	m.AbsDelta = 0
	ci := [2]float64{0, 0}
	m.CI = &ci
	p := 1.0
	m.RawP, m.AdjustedP = &p, &p
	if m.CIExcludesZero {
		t.Fatal("a zero-width interval at zero must not count as excluding zero")
	}
	s := Summarize([]Trial{trial("good", m)})
	if s.FalsePositives.Total != 0 {
		t.Errorf("false positives = %d, want 0", s.FalsePositives.Total)
	}
	if s.EligibleDecisions != 1 {
		t.Errorf("eligible = %d, want 1", s.EligibleDecisions)
	}
}

// An inconclusive metric made no claim, so counting it as a
// successful non-regression would inflate the denominator.
func TestInconclusiveIsNotCountedAsAFalsePositive(t *testing.T) {
	cases := []struct {
		verdict string
		reason  string
	}{
		{compare.VerdictInconclusive, compare.ReasonNoisyNotDouble},
		{compare.VerdictInconclusive, compare.ReasonCIIncludesZero},
		{compare.VerdictInconclusive, compare.ReasonPoorRunQuality},
		{compare.VerdictInvalid, "samples failed validity"},
		{compare.VerdictNotComparable, "host fingerprint differs"},
		{compare.VerdictIncompatible, "definition_version differs"},
		{compare.VerdictInformational, "cross-host comparison: informational only"},
	}
	for _, tc := range cases {
		s := Summarize([]Trial{trial("good", sample("launch.version", "wall_ms", tc.verdict, tc.reason))})
		if s.EligibleDecisions != 0 {
			t.Errorf("%s: eligible = %d, want 0: a withheld decision is not a pass",
				tc.verdict, s.EligibleDecisions)
		}
		if s.FalsePositives.Total != 0 || s.FalsePositives.Denominator != 0 {
			t.Errorf("%s: counted as a false positive or given a denominator: %+v",
				tc.verdict, s.FalsePositives)
		}
		if s.WithheldReasons[tc.verdict] != 1 {
			t.Errorf("%s: withheld reasons = %v, want one", tc.verdict, s.WithheldReasons)
		}
	}
	if s := (Summary{}); s.Verdicts != nil {
		t.Fatal("a zero summary must have no maps")
	}
}

// A poor run's comparisons withhold the verdict by construction. Those
// decisions leave the denominator entirely: they can be neither a false
// positive nor a true negative.
func TestQualityRejectedTrialsAreExcludedFromTheDenominator(t *testing.T) {
	// The comparison did emit an "unchanged" verdict here; the run
	// quality still means the data cannot support a claim, so it must
	// not count as a decision that correctly avoided a regression.
	s := Summarize([]Trial{trial("poor", sample("launch.version", "wall_ms",
		compare.VerdictUnchanged, "below practical threshold"))})
	if s.EligibleDecisions != 0 {
		t.Errorf("eligible = %d, want 0 for a quality-rejected trial", s.EligibleDecisions)
	}
	if s.QualityRejectedDecisions != 1 {
		t.Errorf("quality-rejected decisions = %d, want 1", s.QualityRejectedDecisions)
	}
	if s.Decisions != 1 || s.TrialsQualityRejected != 1 {
		t.Errorf("the decision must still be recorded: %+v", s)
	}
	if s.FalsePositives.Rate != nil {
		t.Error("a rate must not be reported with no denominator")
	}
	if s.FalsePositives.Denominator != 0 {
		t.Errorf("denominator = %d, want 0", s.FalsePositives.Denominator)
	}
}

// Even a hypothetical regression verdict cannot be counted when the trial
// was quality-rejected.
func TestRegressionOnAQualityRejectedTrialIsNotAFalsePositive(t *testing.T) {
	s := Summarize([]Trial{trial("poor", sample("mem.tui.go-heap", "go_heap_alloc_bytes",
		compare.VerdictRegressed, "beyond threshold"))})
	if s.FalsePositives.Total != 0 {
		t.Errorf("a quality-rejected decision must not count as a false positive: %+v", s.FalsePositives)
	}
	if s.Verdicts[compare.VerdictRegressed] != 1 {
		t.Errorf("the verdict must still be counted for inspection: %v", s.Verdicts)
	}
}

// The denominator is the set of real decisions; the numerator is the
// regressions among them.
func TestFalsePositiveAccounting(t *testing.T) {
	trials := []Trial{
		trial("good",
			sample("launch.version", "wall_ms", compare.VerdictRegressed, "beyond threshold with statistical evidence"),
			sample("launch.version", "cpu_ms", compare.VerdictUnchanged, "below practical threshold"),
			sample("launch.help", "wall_ms", compare.VerdictInconclusive, compare.ReasonNoisyNotDouble),
		),
		trial("degraded",
			sample("launch.version", "wall_ms", compare.VerdictUnchanged, "below practical threshold"),
			sample("launch.version", "cpu_ms", compare.VerdictTailRegressed, "p95 beyond threshold"),
			sample("launch.help", "wall_ms", compare.VerdictUnchanged, "below practical threshold"),
		),
		trial("poor",
			sample("launch.version", "wall_ms", compare.VerdictUnchanged, "below practical threshold"),
		),
	}
	s := Summarize(trials)
	if s.Trials != 3 || s.TrialsGood != 1 || s.TrialsDegraded != 1 || s.TrialsQualityRejected != 1 {
		t.Errorf("trial quality counts wrong: %+v", s)
	}
	if s.Decisions != 7 {
		t.Errorf("decisions = %d, want 7", s.Decisions)
	}
	// Eligible: good trial 2 (regressed, unchanged) + degraded trial 3
	// (unchanged, tail_regressed, unchanged) = 5. The inconclusive
	// decision and the whole poor trial are excluded.
	if s.EligibleDecisions != 5 {
		t.Errorf("eligible = %d, want 5", s.EligibleDecisions)
	}
	if s.QualityRejectedDecisions != 1 {
		t.Errorf("quality-rejected decisions = %d, want 1", s.QualityRejectedDecisions)
	}
	if s.FalsePositives.Regressed != 1 || s.FalsePositives.TailRegressed != 1 {
		t.Errorf("false positives = %+v, want 1 regressed + 1 tail", s.FalsePositives)
	}
	if s.FalsePositives.Total != 2 || s.FalsePositives.Denominator != 5 {
		t.Errorf("totals = %+v, want 2 of 5", s.FalsePositives)
	}
	if r := s.FalsePositives.Rate; r == nil || *r != 0.4 {
		t.Errorf("rate = %v, want 0.4", r)
	}
	if s.WithheldReasons[compare.VerdictInconclusive] != 1 {
		t.Errorf("withheld = %v, want one inconclusive", s.WithheldReasons)
	}
	// Denominator accounting must add up exactly.
	if got := s.EligibleDecisions + s.QualityRejectedDecisions + sum(s.WithheldReasons); got != s.Decisions {
		t.Errorf("decisions %d = eligible %d + quality-rejected %d + withheld %d = %d",
			s.Decisions, s.EligibleDecisions, s.QualityRejectedDecisions, sum(s.WithheldReasons), got)
	}
}

func TestPerMetricSummaryTracksVariation(t *testing.T) {
	t1 := sample("launch.version", "wall_ms", compare.VerdictUnchanged, "below practical threshold")
	t1 = withRel(t1, 1.2)
	t2 := sample("launch.version", "wall_ms", compare.VerdictUnchanged, "below practical threshold")
	t2 = withRel(t2, 4.8)
	noisy := sample("launch.version", "wall_ms", compare.VerdictInconclusive, compare.ReasonNoisyNotDouble)
	noisy = withRel(noisy, 2.0)
	noisy.NoiseFlagged = true

	s := Summarize([]Trial{
		trial("good", t1, noisy),
		trial("degraded", t2),
	})
	if len(s.PerMetric) != 1 {
		t.Fatalf("per-metric rows = %d, want 1", len(s.PerMetric))
	}
	m := s.PerMetric[0]
	if m.Decisions != 3 || m.EligibleDecisions != 2 {
		t.Errorf("n/eligible = %d/%d, want 3/2", m.EligibleDecisions, m.Decisions)
	}
	// Median and max of |rel_delta| over the eligible trials only:
	// 1.2 and 4.8, not the 2.0 from the withheld trial.
	if m.MedianAbsRelDeltaPct == nil || *m.MedianAbsRelDeltaPct != 4.8 {
		t.Errorf("median abs delta = %v, want 4.8 (only eligible trials)", m.MedianAbsRelDeltaPct)
	}
	if m.MaxAbsRelDeltaPct == nil || *m.MaxAbsRelDeltaPct != 4.8 {
		t.Errorf("max abs delta = %v, want 4.8", m.MaxAbsRelDeltaPct)
	}
	if m.NoiseFlaggedTrials != 0 {
		t.Errorf("noise count = %d, want 0: the noisy trial was withheld", m.NoiseFlaggedTrials)
	}
	if m.MedianBaselineN != 30 || m.MedianCandidateN != 30 {
		t.Errorf("median n = %d/%d, want 30/30", m.MedianBaselineN, m.MedianCandidateN)
	}
}

func TestEvidenceCounters(t *testing.T) {
	sig := sample("launch.version", "wall_ms", compare.VerdictRegressed, "beyond threshold")
	sig.CIExcludesZero = true
	small := 0.001
	sig.RawP, sig.AdjustedP = &small, &small
	sig.HolmAdjusted = true
	sig.ThresholdMet = true
	sig.HolmRank = 1
	tail := sample("launch.version", "wall_ms", compare.VerdictTailRegressed, "p95")
	tail.TailCIExcludesZero = true
	plain := sample("launch.version", "wall_ms", compare.VerdictUnchanged, "below threshold")

	s := Summarize([]Trial{trial("good", sig, tail, plain)})
	m := s.PerMetric[0]
	if m.CIExcludesZeroTrials != 2 {
		t.Errorf("ci0 = %d, want 2 (median CI plus tail CI)", m.CIExcludesZeroTrials)
	}
	if m.TailEvidenceTrials != 1 {
		t.Errorf("tail evidence = %d, want 1", m.TailEvidenceTrials)
	}
	if m.RawPSignificantTrials != 1 {
		t.Errorf("raw p<alpha = %d, want 1", m.RawPSignificantTrials)
	}
	if m.HolmSurvivedTrials != 1 {
		t.Errorf("holm survived = %d, want 1", m.HolmSurvivedTrials)
	}
	if m.SuppressedByThreshold != 1 {
		t.Errorf("over threshold = %d, want 1", m.SuppressedByThreshold)
	}
}

func TestConclusionRefusesToClaimARateWithoutDecisions(t *testing.T) {
	empty := &Campaign{Schema: SchemaName, SchemaVersion: SchemaVersion}
	empty.Summary = Summarize([]Trial{trial("poor", sample("launch.version", "wall_ms",
		compare.VerdictInconclusive, compare.ReasonPoorRunQuality))})
	text := Conclusion(empty)
	for _, want := range []string{"establishes nothing", "no threshold conclusion may be drawn"} {
		if !contains(text, want) {
			t.Errorf("a campaign with no eligible decision must say %q: %s", want, text)
		}
	}

	// With decisions, it still refuses to call the result calibrated and
	// still says the thresholds are defaults.
	withDecisions := &Campaign{Schema: SchemaName, SchemaVersion: SchemaVersion}
	withDecisions.Summary = Summarize([]Trial{trial("good",
		sample("launch.version", "wall_ms", compare.VerdictUnchanged, "below practical threshold"))})
	text = Conclusion(withDecisions)
	for _, want := range []string{"not a calibrated rate", "engineering defaults", "unchanged by this campaign"} {
		if !contains(text, want) {
			t.Errorf("conclusion must say %q: %s", want, text)
		}
	}
}

func TestCampaignDocumentRoundTrip(t *testing.T) {
	c := &Campaign{
		Schema: SchemaName, SchemaVersion: SchemaVersion, Kind: "aa",
		MethodVersion: "1",
		Subjects: Subjects{
			BaseLabel: DefaultBaseLabel, HeadLabel: DefaultHeadLabel,
			Path: "bin/ff", SHA256: "deadbeef", IdenticalTrials: 1, Identical: true,
		},
		Plan:   Plan{Profile: "standard", Repeats: 1, Seeds: []int64{424242}},
		Trials: []Trial{trial("degraded", sample("launch.version", "wall_ms", compare.VerdictUnchanged, "below practical threshold"))},
	}
	c.Summary = Summarize(c.Trials)
	c.Conclusion = Conclusion(c)

	path := t.TempDir() + "/calibration.json"
	if err := c.Write(path); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subjects.SHA256 != "deadbeef" || !got.Subjects.Identical {
		t.Errorf("subject identity lost on the round trip: %+v", got.Subjects)
	}
	if len(got.Trials) != 1 || len(got.Trials[0].Metrics) != 1 {
		t.Fatalf("per-trial evidence lost: %+v", got.Trials)
	}
	if got.Summary.EligibleDecisions != c.Summary.EligibleDecisions {
		t.Errorf("summary changed on the round trip: %+v", got.Summary)
	}
	if got.Trials[0].Metrics[0].Verdict != compare.VerdictUnchanged {
		t.Errorf("per-metric verdict lost: %+v", got.Trials[0].Metrics[0])
	}
}

func TestReadRejectsForeignDocuments(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, body, wants string }{
		{"wrong-schema.json", `{"schema":"hpov.result","schema_version":"1.0.0"}`, "schema is"},
		{"wrong-version.json", `{"schema":"hpov.calibration","schema_version":"9.0.0"}`, "schema_version"},
	} {
		path := dir + "/" + tc.name
		if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(path); err == nil {
			t.Errorf("%s: must be refused", tc.name)
		} else if !contains(err.Error(), tc.wants) {
			t.Errorf("%s: error %q must mention %q", tc.name, err, tc.wants)
		}
	}
}

func TestRenderKeepsTheCaveat(t *testing.T) {
	// The absolute floor is in the metric's own unit: a 5 ms floor must
	// not be rendered as "5B".
	ms := MetricSummary{Unit: "ms", ThresholdPct: 10, ThresholdAbs: 5}
	if got := thresholdLabel(ms); got != "10%/5ms" {
		t.Errorf("ms threshold = %q, want 10%%/5ms", got)
	}
	bytesMetric := MetricSummary{Unit: "bytes", ThresholdPct: 2, ThresholdAbs: 64 * 1024}
	if got := thresholdLabel(bytesMetric); got != "2%/64KiB" {
		t.Errorf("bytes threshold = %q, want 2%%/64KiB", got)
	}
	if got := thresholdLabel(MetricSummary{Unit: "ms", ThresholdPct: 5}); got != "5%" {
		t.Errorf("relative-only threshold = %q, want 5%%", got)
	}

	c := &Campaign{
		Schema: SchemaName, SchemaVersion: SchemaVersion, Kind: "aa", MethodVersion: "1",
		Subjects: Subjects{BaseLabel: "a", HeadLabel: "b", Path: "bin/ff",
			SHA256: "abc", IdenticalTrials: 1, Identical: true},
		Suite: Suite{Version: "0.1.0", DefinitionSet: "2026.10"},
		Host:  Host{OS: "windows", Arch: "amd64", LogicalCPUs: 16, SingleHost: true},
		Plan:  Plan{Profile: "standard", Repeats: 1, Seeds: []int64{1}},
		Trials: []Trial{
			trial("degraded", withRel(sample("launch.version", "wall_ms", compare.VerdictUnchanged, "below practical threshold"), 1.5)),
			trial("poor", sample("launch.version", "wall_ms", compare.VerdictInconclusive, compare.ReasonPoorRunQuality)),
		},
	}
	c.Summary = Summarize(c.Trials)
	c.Conclusion = Conclusion(c)
	out := Render(c)
	for _, want := range []string{
		"A/A regressions", "A/A improvements", "eligible decisions", "quality-rejected",
		"engineering defaults", "identical subjects in every trial",
	} {
		if !contains(out, want) {
			t.Errorf("render must mention %q:\n%s", want, out)
		}
	}
	if Render(c) != out {
		t.Error("render must be deterministic")
	}
}

// A false improvement is the same defect in the other direction: the
// engine claiming an identical subject got better. It must be counted and
// reported, not folded away because it points the "good" way.
func TestFalseImprovementsAreCountedSymmetrically(t *testing.T) {
	s := Summarize([]Trial{trial("degraded",
		sample("launch.version", "wall_ms", compare.VerdictTailImproved, "p95 beyond threshold"),
		sample("launch.help", "wall_ms", compare.VerdictImproved, "beyond threshold with statistical evidence"),
		sample("launch.help", "cpu_ms", compare.VerdictUnchanged, "below practical threshold"),
	)})
	if s.FalsePositives.Total != 0 {
		t.Errorf("improvements must not count as regressions: %+v", s.FalsePositives)
	}
	if s.FalseImprovements.TailImproved != 1 || s.FalseImprovements.Improved != 1 {
		t.Errorf("false improvements = %+v, want 1 improved + 1 tail", s.FalseImprovements)
	}
	if s.FalseImprovements.Total != 2 || s.FalseImprovements.Denominator != 3 {
		t.Errorf("false improvement totals = %+v, want 2 of 3", s.FalseImprovements)
	}
	// Both directions share one denominator: the eligible decisions.
	if s.FalseImprovements.Denominator != s.FalsePositives.Denominator {
		t.Errorf("denominators must match: %d vs %d",
			s.FalseImprovements.Denominator, s.FalsePositives.Denominator)
	}
	if s.FalseImprovements.Definition == "" {
		t.Error("the count must carry its definition so it cannot be quoted bare")
	}
}

func sum(m map[string]int) int {
	t := 0
	for _, v := range m {
		t += v
	}
	return t
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
