// Package compare decides, metric by metric, whether a candidate HPOV
// result moved relative to a baseline.
//
// Three rules shape everything here:
//
//  1. Statistical significance alone is never a verdict. A change is
//     significant only when it clears the practical threshold, has
//     statistical evidence, and is not explained by noise (plan §10.3).
//  2. There is no aggregate score. Every metric is decided on its own
//     evidence and reported on its own; a benchmark is never collapsed
//     into one number, because no defensible weighting exists across
//     ms, bytes and counts (plan §6.6).
//  3. Unknown is a real answer. incompatible, not_comparable and
//     inconclusive are reported with reasons rather than silently
//     folded into pass or regression.
//
// The statistics are the ones the suite already uses — nearest-rank
// quantiles, deterministic bootstrap, Mann-Whitney, Hodges-Lehmann —
// so a comparison and a single run cannot disagree about a
// distribution. Nothing here re-implements them.
package compare

import (
	"forcefield/internal/hpov/schema"
)

// Document identity. Comparison output is a versioned document in its
// own right, not an ad-hoc dump: it records the method that produced
// every verdict so the decision can be recomputed and audited.
const (
	SchemaName    = "hpov.compare"
	SchemaVersion = "1.0.0"
)

// MethodVersion identifies the decision rules. Bump it when a verdict
// would change for identical inputs, so stored comparisons remain
// interpretable.
const MethodVersion = "1"

// Suite profiles. The quick profile exists to be fast, not to decide:
// its sample plan cannot support the evidence rule below, so its
// verdicts are directional only.
const ProfileQuick = "quick"

// Verdict states (plan §10.4).
//
// The set is deliberately wide: each of these means something different
// to a reader, and collapsing them into "pass"/"fail" is what makes a
// comparison lie.
const (
	// VerdictImproved: a change in the metric's good direction, with
	// both practical and statistical evidence.
	VerdictImproved = "improved"
	// VerdictRegressed: a change in the metric's bad direction, with
	// both practical and statistical evidence.
	VerdictRegressed = "regressed"
	// VerdictUnchanged: below the practical threshold, or the CI of the
	// median difference includes zero.
	VerdictUnchanged = "unchanged"
	// VerdictTailRegressed / VerdictTailImproved: p50 unchanged while p95
	// moved beyond its own threshold with a CI excluding zero. Reported
	// separately because a healthy median hides a degrading tail.
	VerdictTailRegressed = "tail_regressed"
	VerdictTailImproved  = "tail_improved"
	// VerdictTradeOff is not a metric verdict: opposite-direction movement
	// inside one benchmark is reported as a TradeOff entry plus an
	// annotation on each metric, because collapsing two metrics into one
	// state would hide the fact that both happened.
	// VerdictInconclusive: the data cannot support a verdict. Named
	// causes are recorded in InconclusiveReason.
	VerdictInconclusive = "inconclusive"
	// VerdictMissingBase / VerdictMissingCurrent: the metric is absent
	// from one side.
	VerdictMissingBase    = "missing_base"
	VerdictMissingCurrent = "missing_current"
	// VerdictIncompatible: same name, different definition (unit,
	// params, definition_version, direction).
	VerdictIncompatible = "incompatible"
	// VerdictNotComparable: comparable in shape but measured on a
	// different host, platform semantics or subject OS/arch, so the
	// numbers are not the same quantity.
	VerdictNotComparable = "not_comparable"
	// VerdictInvalid: the metric's samples did not pass the suite's own
	// validity contract.
	VerdictInvalid = "invalid"
	// VerdictInformational: a directionless metric moved; reported as
	// changed, never improved or regressed.
	VerdictInformational = "informational"
)

// Reasons a verdict is inconclusive or a metric is not comparable.
// Recorded as data so a reader never has to guess.
const (
	ReasonNoValidSamples    = "no_valid_samples"
	ReasonMetricUnavailable = "metric_unavailable"
	ReasonSamplesInvalid    = "samples_failed_validity"
	ReasonNoisyNotDouble    = "noisy_effect_below_2x_threshold"
	ReasonPoorRunQuality    = "poor_run_quality"
	ReasonQuickProfile      = "quick_profile_directional_only"
	ReasonCIIncludesZero    = "ci_includes_zero"
	ReasonMannWhitneyNSmall = "mann_whitney_needs_n>=8"
	ReasonHolmAdjusted      = "not_significant_after_holm"
)

// Comparison is the versioned document produced by one comparison.
type Comparison struct {
	Schema        string     `json:"schema"`
	SchemaVersion string     `json:"schema_version"`
	MethodVersion string     `json:"method_version"`
	GeneratedFrom Input      `json:"inputs"`
	Options       Options    `json:"options"`
	Method        Method     `json:"method"`
	Summary       Summary    `json:"summary"`
	Metrics       []Metric   `json:"metrics"`
	Coverage      []Coverage `json:"coverage,omitempty"`
	TradeOffs     []TradeOff `json:"trade_offs,omitempty"`
	Warnings      []string   `json:"warnings,omitempty"`
}

// Input records which documents were compared. Enough to re-run the
// comparison given the same files.
type Input struct {
	Baseline  Side `json:"baseline"`
	Candidate Side `json:"candidate"`
}

// Side identifies one side of the comparison.
type Side struct {
	Role         string `json:"role"` // "baseline" | "candidate"
	Path         string `json:"path,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	SuiteVersion string `json:"suite_version,omitempty"`
	Profile      string `json:"profile,omitempty"`
	Quality      string `json:"quality,omitempty"`
	HostID       string `json:"host_id,omitempty"`
	OS           string `json:"os,omitempty"`
	Arch         string `json:"arch,omitempty"`
	// FromSameFile marks a paired compare-live comparison: both sides
	// come from one interleaved run, so per-iteration pairing is valid.
	FromSameFile bool   `json:"from_same_file,omitempty"`
	Subject      string `json:"subject,omitempty"`
}

// Method records the decision rules so a verdict can be explained and
// recomputed.
type Method struct {
	// FamilyAlpha is the per-test significance level required by the
	// plan before multiple-comparison correction (§10.3).
	FamilyAlpha float64 `json:"family_alpha"`
	// HolmCorrection records that the family of tests was adjusted.
	HolmCorrection bool `json:"holm_correction"`
	// StatisticalEvidence names both required tests.
	StatisticalEvidence []string `json:"statistical_evidence"`
	// EffectEstimate names the reported location shift.
	EffectEstimate string `json:"effect_estimate"`
	// BootstrapResamples and BootstrapSeed make intervals reproducible.
	BootstrapResamples int   `json:"bootstrap_resamples"`
	BootstrapSeed      int64 `json:"bootstrap_seed"`
	// NoiseMultiplier is the factor applied when either side is
	// noisy/bimodal/drift (§10.3 rule 3).
	NoiseMultiplier float64 `json:"noise_multiplier"`
	// DirectionalOnly marks a profile whose verdicts are informational.
	DirectionalOnly bool `json:"directional_only,omitempty"`
	// ThresholdSource/ThresholdVersion name the table that produced
	// every threshold, because the table is not stored inside the
	// options: a verdict whose thresholds cannot be identified cannot be
	// audited or recalculated.
	ThresholdSource  string `json:"threshold_source"`
	ThresholdVersion string `json:"threshold_version"`
}

// Summary counts verdicts. There is no score: counts are the only
// roll-up, and they name every state rather than collapsing them.
type Summary struct {
	Counts map[string]int `json:"counts"`
	// Regressed is the number of metrics asserting a regression. It is a
	// count of statements, not a weight, and it gates nothing on its own:
	// the CLI decides, from the verdict set and the user's flags.
	Regressed int `json:"regressed_metrics"`
	Reported  int `json:"reported_metrics"`
}

// Metric is one metric-level comparison.
type Metric struct {
	Benchmark string `json:"benchmark"`
	Name      string `json:"name"`
	Unit      string `json:"unit"`
	// Direction comes from the metric definition, never inferred from
	// the sign of the delta.
	Direction      string   `json:"direction"`
	PlatformSemant string   `json:"platform_semantics,omitempty"`
	Verdict        string   `json:"verdict"`
	VerdictReason  string   `json:"verdict_reason,omitempty"`
	BaselineN      int      `json:"baseline_n"`
	CandidateN     int      `json:"candidate_n"`
	BaseP50        float64  `json:"base_p50"`
	CurP50         float64  `json:"cur_p50"`
	BaseP95        *float64 `json:"base_p95"`
	CurP95         *float64 `json:"cur_p95"`
	AbsDelta       float64  `json:"abs_delta"`
	// RelDeltaPct is null when the baseline centre is zero: a
	// percentage of nothing is not a number.
	RelDeltaPct *float64 `json:"rel_delta_pct"`
	// Signed is the direction-aware delta: positive always means worse.
	// Zero value when the metric is directionless.
	Signed             float64     `json:"signed"`
	SignedRel          *float64    `json:"signed_rel_pct"`
	HLShift            float64     `json:"hodges_lehmann_shift"`
	CI                 *[2]float64 `json:"ci95_median_diff,omitempty"`
	CIExcludesZero     bool        `json:"ci_excludes_zero"`
	TailCI             *[2]float64 `json:"ci95_p95_diff,omitempty"`
	TailCIExcludesZero bool        `json:"ci_excludes_zero_p95,omitempty"`
	// MannWhitneyP is the family-corrected p and RawP the uncorrected
	// one. Both are null when no test ran: a non-comparable metric or one
	// below the sample floor has no p-value, and writing a number there
	// would invent evidence.
	MannWhitneyP    *float64 `json:"mann_whitney_p"`
	RawP            *float64 `json:"mann_whitney_p_raw"`
	HolmAdjusted    bool     `json:"holm_adjusted"`
	HolmRank        int      `json:"holm_rank,omitempty"`
	ThresholdPct    float64  `json:"threshold_pct"`
	ThresholdAbs    float64  `json:"threshold_abs"`
	ThresholdBytes  float64  `json:"threshold_effective_bytes"`
	ThresholdUsed   float64  `json:"threshold_used_bytes"`
	ThresholdMet    bool     `json:"threshold_met"`
	NoiseFlagged    bool     `json:"noise_flagged"`
	NoiseFlags      []string `json:"noise_flags,omitempty"`
	QualityBase     string   `json:"baseline_quality,omitempty"`
	QualityCand     string   `json:"candidate_quality,omitempty"`
	ConfoundedBuild bool     `json:"confounded_build,omitempty"`
}

// Coverage reports every benchmark/metric present on either side, with
// a reason when it was not compared. Nothing is dropped silently
// (plan §10.5).
type Coverage struct {
	Benchmark string `json:"benchmark"`
	Name      string `json:"name,omitempty"`
	Reason    string `json:"reason"`
}

// Coverage reasons.
const (
	CoverageUnsupported = "unsupported"
	CoverageSkipped     = "skipped"
	CoverageRemoved     = "removed"
	CoverageNew         = "new"
	CoverageMissing     = "missing"
	CoverageInvalid     = "invalid"
)

// TradeOff records opposite-direction movement within one benchmark.
// It never resolves to a winner: the point is that "better" and
// "worse" both happened and a single summary would hide one of them.
type TradeOff struct {
	Benchmark   string   `json:"benchmark"`
	Improved    []string `json:"improved"`
	Regressed   []string `json:"regressed"`
	Explanation string   `json:"explanation"`
}

// Metric direction, mirroring bench.MetricSpec.Direction. Duplicated as
// constants so this package compares stored results without importing
// the benchmark definition package: a comparison must be able to run
// against a result whose metrics were defined by an older tool.
const (
	schemaLowerIsBetter  = "lower_is_better"
	schemaHigherIsBetter = "higher_is_better"
	schemaInformational  = "informational"
)

// Options controls a comparison.
type Options struct {
	// AllowCrossHost degrades every verdict to informational: the host
	// fingerprint differs, so absolute numbers are not the same
	// quantity.
	AllowCrossHost bool `json:"allow_cross_host"`
	// FailOnRegression makes a regressed metric set the exit code.
	FailOnRegression bool `json:"fail_on_regression"`
	// Explain adds the evidence chain to each metric.
	Explain bool `json:"explain"`
	// Thresholds overrides the built-in table (calibrated
	// thresholds.json), keyed by benchmark glob.
	Thresholds *ThresholdTable `json:"-"`

	// Run quality of each side, from the result documents. Populated by
	// the entry points; not user input, because a caller must not be
	// able to assert that a poor run was good.
	qualityBase string
	qualityCand string
	// Profile of each side. The quick profile is directional only.
	profileBase string
	profileCand string
}

// ComparisonInput is one matched pair of sides to compare.
type ComparisonInput struct {
	Baseline  *schema.Benchmark
	Candidate *schema.Benchmark
	// BaselineSubject/CandidateSubject label the subject entries when
	// both come from one interleaved run.
	BaselineSubject  string
	CandidateSubject string
	Paired           bool
}
