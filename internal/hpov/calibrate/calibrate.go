// Package calibrate runs A/A calibration campaigns for HPOV and
// summarises what they observed.
//
// An A/A campaign answers one question: when the two subjects under
// comparison are the same binary, how often does the comparison emit a
// verdict, and which verdicts? A regression between a subject and
// itself is, by construction, an observed false positive — the campaign
// exists to measure that rate before anyone trusts a regression gate.
//
// It changes nothing about how a comparison is decided. Every trial is
// an ordinary interleaved run followed by an ordinary comparison of its
// two subjects, using the same runner, the same thresholds and the same
// verdict rules as `hpov compare-live`. This package only repeats that
// and keeps the evidence.
//
// The evidence is kept, not summarised away: every trial writes its own
// hpov.result and hpov.compare documents (the existing formats, still the
// source of truth) and every metric decision within every trial is
// recorded in the campaign document with the numbers that produced it —
// sample counts, centres, deltas, interval, both p-values, threshold,
// quality, noise flags and the verdict reason.
package calibrate

import (
	"math"
	"sort"

	"forcefield/internal/hpov/compare"
)

// Document identity. The campaign is a versioned document in its own
// right, like the result and the comparison, so a calibration claim can
// be traced to the evidence that produced it.
const (
	SchemaName    = "hpov.calibration"
	SchemaVersion = "1.0.0"
)

// Campaign is one A/A calibration campaign.
type Campaign struct {
	Schema        string     `json:"schema"`
	SchemaVersion string     `json:"schema_version"`
	Kind          string     `json:"kind"` // "aa"
	MethodVersion string     `json:"method_version"`
	Suite         Suite      `json:"suite"`
	Subjects      Subjects   `json:"subjects"`
	Plan          Plan       `json:"plan"`
	Thresholds    Thresholds `json:"thresholds"`
	Comparison    Comparison `json:"comparison"`
	Host          Host       `json:"host"`
	Trials        []Trial    `json:"trials"`
	Summary       Summary    `json:"summary"`
	// Conclusion states what the evidence does and does not license. It
	// is part of the document so a copied-out report cannot silently
	// drop the caveat.
	Conclusion string `json:"conclusion"`
}

// Suite identifies the tool and definition set that produced the
// campaign, from the first trial's result document.
type Suite struct {
	Version       string `json:"version"`
	DefinitionSet string `json:"definition_set"`
}

// Subjects records the A/A identity: one binary, presented twice under
// two labels so the comparison treats them as two subjects.
type Subjects struct {
	BaseLabel string `json:"base_label"`
	HeadLabel string `json:"head_label"`
	Path      string `json:"path"`
	// SHA256 is the subject's content hash as probed by the runner.
	// Every trial re-probes it and the campaign fails if a trial ever
	// saw two different hashes: an A/A campaign whose two sides were not
	// the same build would measure something else entirely.
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Version   string `json:"version,omitempty"`
	Source    string `json:"source,omitempty"`
	// IdenticalInEveryTrial records that check's outcome per trial
	// count: IdenticalTrials out of Trials.
	IdenticalTrials int  `json:"identical_trials"`
	Identical       bool `json:"identical_in_every_trial"`
}

// Plan is what each trial ran: the benchmark selection, the profile, and
// the iteration counts actually used.
type Plan struct {
	Benchmarks []string `json:"benchmarks"`
	Profile    string   `json:"profile"`
	Repeats    int      `json:"repeats"`
	Warmup     *int     `json:"warmup_override,omitempty"`
	N          *int     `json:"n_override,omitempty"`
	// SeedBase is the campaign seed; trial i uses SeedBase+i, so the
	// interleaving order is reproducible per trial while the campaign
	// still samples more than one order.
	SeedBase int64   `json:"seed_base"`
	Seeds    []int64 `json:"seeds"`
}

// Thresholds records the table every trial decided against, by identity
// rather than by value: a verdict is only interpretable next to the
// table that produced it.
type Thresholds struct {
	Version string `json:"version"`
	Source  string `json:"source"`
}

// Comparison records which comparison contract the trials used.
type Comparison struct {
	SchemaVersion string  `json:"schema_version"`
	MethodVersion string  `json:"method_version"`
	FamilyAlpha   float64 `json:"family_alpha"`
	Paired        bool    `json:"paired"`
}

// Host is the measurement host, taken from the first trial. Campaign
// HostIDs lists every fingerprint seen: a campaign whose trials landed
// on different hosts is reported as such rather than merged silently.
type Host struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	CPUModel    string `json:"cpu_model,omitempty"`
	LogicalCPUs int    `json:"logical_cpus"`
	ClockSource string `json:"clock_source"`
	ClockResNs  int64  `json:"clock_resolution_ns"`
	PowerSource string `json:"power_source,omitempty"`
	PowerPlan   string `json:"power_plan,omitempty"`
	// HostID is the salted host fingerprint, kept per host block rather
	// than duplicated into the top level.
	HostID     string   `json:"host_id,omitempty"`
	HostIDs    []string `json:"host_ids"`
	SingleHost bool     `json:"single_host"`
}

// Trial is one A/A repetition: one interleaved run of the same binary
// under two labels, and the comparison of those two subjects.
type Trial struct {
	Index    int     `json:"index"`
	Seed     int64   `json:"seed"`
	RunID    string  `json:"run_id"`
	Result   string  `json:"result_path"`
	Decision string  `json:"comparison_path"`
	Quality  Quality `json:"quality"`
	// QualityRejected marks a trial whose run quality is poor. The
	// comparison refuses to assert a verdict on such data, so its
	// decisions are inconclusive by construction and are excluded from
	// the false-positive denominator rather than counted as passes.
	QualityRejected bool `json:"quality_rejected"`
	// BenchmarkErrors marks a trial containing an errored or invalid
	// benchmark entry.
	BenchmarkErrors bool `json:"benchmark_errors"`
	Identical       bool `json:"identical_subjects"`
	// SubjectHashes holds the hash each side presented. Both must match.
	SubjectHashes [2]string          `json:"subject_hashes"`
	Metrics       []MetricSample     `json:"metrics"`
	Coverage      []compare.Coverage `json:"coverage,omitempty"`
}

// Quality is the run-level quality label, copied from the result so a
// reader never has to open the trial's result file to know it.
type Quality struct {
	Label string   `json:"label"`
	Flags []string `json:"flags,omitempty"`
}

// MetricSample is one metric decision from one trial, with the evidence
// that produced it. Fields whose absence is meaningful (no interval, no
// p-value) stay null rather than becoming zero.
type MetricSample struct {
	Benchmark string `json:"benchmark"`
	Name      string `json:"name"`
	Unit      string `json:"unit"`
	Verdict   string `json:"verdict"`
	Reason    string `json:"verdict_reason,omitempty"`

	BaselineN  int      `json:"baseline_n"`
	CandidateN int      `json:"candidate_n"`
	BaseP50    float64  `json:"base_p50"`
	CurP50     float64  `json:"cur_p50"`
	BaseP95    *float64 `json:"base_p95"`
	CurP95     *float64 `json:"cur_p95"`
	AbsDelta   float64  `json:"abs_delta"`
	// RelDeltaPct is the observed movement in percent of the baseline
	// centre: the raw evidence of how much a metric naturally varies.
	RelDeltaPct *float64 `json:"rel_delta_pct"`

	CI                 *[2]float64 `json:"ci95_median_diff,omitempty"`
	CIExcludesZero     bool        `json:"ci_excludes_zero"`
	TailCIExcludesZero bool        `json:"ci_excludes_zero_p95,omitempty"`
	RawP               *float64    `json:"mann_whitney_p_raw"`
	AdjustedP          *float64    `json:"mann_whitney_p_adjusted"`
	HolmRank           int         `json:"holm_rank,omitempty"`
	HolmAdjusted       bool        `json:"holm_adjusted"`

	ThresholdPct  float64 `json:"threshold_pct"`
	ThresholdAbs  float64 `json:"threshold_abs"`
	ThresholdUsed float64 `json:"threshold_used_bytes"`
	ThresholdMet  bool    `json:"threshold_met"`

	NoiseFlagged bool     `json:"noise_flagged"`
	NoiseFlags   []string `json:"noise_flags,omitempty"`
	QualityBase  string   `json:"baseline_quality,omitempty"`
	QualityCand  string   `json:"candidate_quality,omitempty"`
}

// Summary is the campaign roll-up. It counts verdicts by name and
// reports denominators; it deliberately produces no score.
type Summary struct {
	Trials                    int `json:"trials"`
	TrialsGood                int `json:"trials_quality_good"`
	TrialsDegraded            int `json:"trials_quality_degraded"`
	TrialsQualityRejected     int `json:"trials_quality_rejected"`
	TrialsWithBenchmarkErrors int `json:"trials_with_benchmark_errors"`

	// Decisions is every metric decision the campaign recorded.
	Decisions int `json:"decisions"`
	// QualityRejectedDecisions are the decisions taken on trials whose
	// run quality was poor. They are recorded and counted, but they
	// leave the denominator entirely: the comparison withheld judgment on
	// that data, so such a decision can be neither a false positive nor
	// a true negative.
	QualityRejectedDecisions int `json:"quality_rejected_decisions"`
	// EligibleDecisions is the denominator for a false positive: the
	// decisions where the comparison actually weighed the evidence and
	// was free to call a regression. Withheld decisions
	// (inconclusive, invalid, not_comparable, incompatible) and
	// informational ones are excluded, because they cannot be a false
	// positive however many of them there are.
	EligibleDecisions int            `json:"eligible_decisions"`
	Verdicts          map[string]int `json:"verdict_counts"`
	WithheldReasons   map[string]int `json:"withheld_reasons"`

	FalsePositives    FalseChanges    `json:"false_positives"`
	FalseImprovements FalseChanges    `json:"false_improvements"`
	PerMetric         []MetricSummary `json:"per_metric"`
}

// FalseChanges is a directional count of verdict-asymmetry over the
// eligible decisions: claims the comparison made about a subject against
// itself. Both directions are reported because a false improvement is
// the same defect as a false regression — an engine claiming something
// about identical inputs — and reporting only the regression direction
// would hide half of it.
type FalseChanges struct {
	Regressed     int      `json:"regressed"`
	TailRegressed int      `json:"tail_regressed"`
	Improved      int      `json:"improved"`
	TailImproved  int      `json:"tail_improved"`
	Total         int      `json:"total"`
	Denominator   int      `json:"denominator"`
	Rate          *float64 `json:"rate"`
	Definition    string   `json:"definition,omitempty"`
}

// MetricSummary is what one metric did across the campaign.
type MetricSummary struct {
	Benchmark    string  `json:"benchmark"`
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`
	ThresholdPct float64 `json:"threshold_pct"`
	ThresholdAbs float64 `json:"threshold_abs"`

	// Decisions is every trial in which this metric produced a verdict
	// record; Eligible applies the same withholding and
	// quality-rejection rule as the campaign total.
	Decisions                int            `json:"decisions"`
	QualityRejectedDecisions int            `json:"quality_rejected_decisions"`
	EligibleDecisions        int            `json:"eligible_decisions"`
	Verdicts                 map[string]int `json:"verdict_counts"`
	WithheldReasons          map[string]int `json:"withheld_reasons"`

	NoiseFlaggedTrials    int `json:"noise_flagged_trials"`
	ThresholdMetTrials    int `json:"threshold_met_trials"`
	CIExcludesZeroTrials  int `json:"ci_excludes_zero_trials"`
	RawPSignificantTrials int `json:"raw_p_below_alpha_trials"`
	HolmSurvivedTrials    int `json:"holm_survived_trials"`
	TailEvidenceTrials    int `json:"tail_evidence_trials"`

	// Observed variation in percent of the baseline centre. Median and
	// max of |rel_delta| across the metric's eligible trials: the answer
	// to "how much does this metric naturally vary".
	MedianAbsRelDeltaPct *float64 `json:"median_abs_rel_delta_pct"`
	MaxAbsRelDeltaPct    *float64 `json:"max_abs_rel_delta_pct"`
	MedianBaselineN      int      `json:"median_baseline_n"`
	MedianCandidateN     int      `json:"median_candidate_n"`
	// SuppressedByThreshold is the count of eligible trials whose
	// movement cleared the practical threshold.
	SuppressedByThreshold int `json:"movement_over_threshold_trials"`
}

// FalsePositiveDefinition is the sentence this package counts against,
// stored in the document so the number is never quoted bare.
const FalsePositiveDefinition = "A regressed or tail_regressed verdict emitted on a trial whose two subjects were the same " +
	"binary (verified by content hash), where the comparison was valid and the run quality did not withhold the verdict. " +
	"Inconclusive, invalid, not_comparable, incompatible, informational and quality-rejected decisions are not counted."

// FalseImprovementDefinition is its mirror: a verdict asserting that an
// identical subject improved is the same false claim in the other
// direction.
const FalseImprovementDefinition = "An improved or tail_improved verdict emitted on a trial whose two subjects were the same " +
	"binary (verified by content hash). Counted and reported for the same reason as a false regression."

// metricFrom converts one comparison metric into the campaign's evidence
// record. The comparison document remains the source of truth; this is
// the same information, keyed for the campaign roll-up.
func metricFrom(m compare.Metric) MetricSample {
	return MetricSample{
		Benchmark: m.Benchmark, Name: m.Name, Unit: m.Unit,
		Verdict: m.Verdict, Reason: m.VerdictReason,
		BaselineN: m.BaselineN, CandidateN: m.CandidateN,
		BaseP50: m.BaseP50, CurP50: m.CurP50,
		BaseP95: m.BaseP95, CurP95: m.CurP95,
		AbsDelta: m.AbsDelta, RelDeltaPct: m.RelDeltaPct,
		CI: m.CI, CIExcludesZero: m.CIExcludesZero,
		TailCIExcludesZero: m.TailCIExcludesZero,
		RawP:               m.RawP, AdjustedP: m.MannWhitneyP,
		HolmRank: m.HolmRank, HolmAdjusted: m.HolmAdjusted,
		ThresholdPct: m.ThresholdPct, ThresholdAbs: m.ThresholdAbs,
		ThresholdUsed: m.ThresholdUsed, ThresholdMet: m.ThresholdMet,
		NoiseFlagged: m.NoiseFlagged, NoiseFlags: m.NoiseFlags,
		QualityBase: m.QualityBase, QualityCand: m.QualityCand,
	}
}

// eligible reports whether a verdict was a real decision that could have
// been a regression.
//
// Withheld is the whole point: a metric that is inconclusive, invalid,
// not comparable, incompatible or informational has not claimed
// anything, and counting it as a successful non-regression would inflate
// the denominator with decisions the engine declined to make.
func eligible(verdict string) bool {
	switch verdict {
	case compare.VerdictInconclusive, compare.VerdictInvalid,
		compare.VerdictNotComparable, compare.VerdictIncompatible,
		compare.VerdictInformational:
		return false
	default:
		return true
	}
}

// isRegression reports whether a verdict asserts a regression.
func isRegression(verdict string) bool {
	return verdict == compare.VerdictRegressed || verdict == compare.VerdictTailRegressed
}

// isImprovement reports whether a verdict asserts an improvement.
func isImprovement(verdict string) bool {
	return verdict == compare.VerdictImproved || verdict == compare.VerdictTailImproved
}

// Resummarize recomputes the campaign summary and conclusion from the
// trials stored in a document.
//
// The trials are the evidence; the summary is a reading of them. Being
// able to re-read the same evidence without re-measuring is what makes
// the stored document authoritative rather than the report beside it,
// and it is how a future account change gets applied to old campaigns.
func Resummarize(c *Campaign) {
	c.Summary = Summarize(c.Trials)
	c.Conclusion = Conclusion(c)
}

// Summarize rolls trials up into the campaign summary. It is a pure
// function of the trials so the accounting can be tested without
// measuring a machine.
func Summarize(trials []Trial) Summary {
	s := Summary{
		Trials:          len(trials),
		Verdicts:        map[string]int{},
		WithheldReasons: map[string]int{},
		PerMetric:       []MetricSummary{},
	}
	type key struct{ benchmark, name string }
	// acc is the scratch for one metric: the exported summary plus the
	// sample vectors the reduction needs. It is local to Summarize so no
	// half-built state ever sits on the document type.
	type acc struct {
		ms     MetricSummary
		absRel []float64
		baseN  []int
		candN  []int
	}
	per := map[key]*acc{}
	var order []key

	for _, t := range trials {
		switch {
		case t.QualityRejected:
			s.TrialsQualityRejected++
		case t.Quality.Label == "degraded":
			s.TrialsDegraded++
		default:
			s.TrialsGood++
		}
		if t.BenchmarkErrors {
			s.TrialsWithBenchmarkErrors++
		}
		for _, m := range t.Metrics {
			s.Decisions++
			s.Verdicts[m.Verdict]++
			k := key{m.Benchmark, m.Name}
			a, ok := per[k]
			if !ok {
				a = &acc{ms: MetricSummary{
					Benchmark: m.Benchmark, Name: m.Name, Unit: m.Unit,
					ThresholdPct: m.ThresholdPct, ThresholdAbs: m.ThresholdAbs,
					Verdicts: map[string]int{}, WithheldReasons: map[string]int{},
				}}
				per[k] = a
				order = append(order, k)
			}
			ms := &a.ms
			ms.Decisions++
			ms.Verdicts[m.Verdict]++
			if t.QualityRejected {
				// The run quality was poor, so the comparison withheld
				// judgment on this data. It is kept visible and counted
				// separately, and it is neither a false positive nor a
				// true negative: nothing was decided.
				s.QualityRejectedDecisions++
				ms.QualityRejectedDecisions++
				continue
			}
			if !eligible(m.Verdict) {
				s.WithheldReasons[m.Verdict]++
				ms.WithheldReasons[m.Verdict]++
				continue
			}
			s.EligibleDecisions++
			ms.EligibleDecisions++
			if isRegression(m.Verdict) {
				s.countFalse(&s.FalsePositives, m.Verdict)
			}
			if isImprovement(m.Verdict) {
				s.countFalse(&s.FalseImprovements, m.Verdict)
			}
			if m.NoiseFlagged {
				ms.NoiseFlaggedTrials++
			}
			if m.ThresholdMet {
				ms.ThresholdMetTrials++
			}
			if m.CIExcludesZero || m.TailCIExcludesZero {
				ms.CIExcludesZeroTrials++
			}
			if m.RawP != nil && *m.RawP < 0.01 {
				ms.RawPSignificantTrials++
			}
			if m.HolmAdjusted {
				ms.HolmSurvivedTrials++
			}
			if m.TailCIExcludesZero {
				ms.TailEvidenceTrials++
			}
			if m.RelDeltaPct != nil {
				a.absRel = append(a.absRel, math.Abs(*m.RelDeltaPct))
			}
			a.baseN = append(a.baseN, m.BaselineN)
			a.candN = append(a.candN, m.CandidateN)
		}
	}

	for _, k := range order {
		a := per[k]
		ms := &a.ms
		ms.MedianAbsRelDeltaPct = medianOf(a.absRel)
		ms.MaxAbsRelDeltaPct = maxOf(a.absRel)
		ms.MedianBaselineN = medianInt(a.baseN)
		ms.MedianCandidateN = medianInt(a.candN)
		ms.SuppressedByThreshold = ms.ThresholdMetTrials
		s.PerMetric = append(s.PerMetric, *ms)
	}
	sort.Slice(s.PerMetric, func(i, j int) bool {
		if s.PerMetric[i].Benchmark != s.PerMetric[j].Benchmark {
			return s.PerMetric[i].Benchmark < s.PerMetric[j].Benchmark
		}
		return s.PerMetric[i].Name < s.PerMetric[j].Name
	})

	s.FalsePositives.Total = s.FalsePositives.Regressed + s.FalsePositives.TailRegressed
	s.FalsePositives.Denominator = s.EligibleDecisions
	s.FalsePositives.Definition = FalsePositiveDefinition
	s.FalseImprovements.Total = s.FalseImprovements.Improved + s.FalseImprovements.TailImproved
	s.FalseImprovements.Denominator = s.EligibleDecisions
	s.FalseImprovements.Definition = FalseImprovementDefinition
	// No rate without a denominator: a rate over a handful of decisions
	// would read as precision the evidence cannot carry.
	if s.FalsePositives.Denominator > 0 {
		rate := float64(s.FalsePositives.Total) / float64(s.FalsePositives.Denominator)
		s.FalsePositives.Rate = &rate
	}
	if s.FalseImprovements.Denominator > 0 {
		rate := float64(s.FalseImprovements.Total) / float64(s.FalseImprovements.Denominator)
		s.FalseImprovements.Rate = &rate
	}
	return s
}

// countFalse attributes one verdict-asymmetry to the right direction.
func (s *Summary) countFalse(dst *FalseChanges, verdict string) {
	switch verdict {
	case compare.VerdictRegressed:
		dst.Regressed++
	case compare.VerdictTailRegressed:
		dst.TailRegressed++
	case compare.VerdictImproved:
		dst.Improved++
	case compare.VerdictTailImproved:
		dst.TailImproved++
	}
}

func medianOf(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return &s[len(s)/2]
}

func maxOf(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	m := v[0]
	for _, x := range v[1:] {
		if x > m {
			m = x
		}
	}
	return &m
}

func medianInt(v []int) int {
	if len(v) == 0 {
		return 0
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	return s[len(s)/2]
}
