package compare

import (
	"fmt"
	"math"
	"sort"

	"forcefield/internal/hpov/stats"
)

// familyAlpha is the per-test significance level required before
// multiple-comparison correction (plan §10.3: p < 0.01).
const familyAlpha = 0.01

// minNPerSide is the smallest n for which the Mann-Whitney test is
// used at all (plan §10.3: needs n >= 8 per side). Below it the test
// reports no evidence rather than a weakly significant p.
const minNPerSide = 8

// decide evaluates one matched pair and returns the metric record.
//
// The order is the plan's, cheapest test first: statistical machinery
// runs only once the pair is comparable and has enough samples, so a
// non-comparable metric never gets a p-value it cannot support.
func decide(p MetricPair, th Threshold, opt *Options) (Metric, testResult) {
	m := Metric{
		Benchmark:       p.Benchmark,
		Name:            p.Name,
		Unit:            p.Unit,
		Direction:       p.Direction,
		PlatformSemant:  p.PlatformSemantics,
		BaselineN:       p.BaseN,
		CandidateN:      p.CurN,
		NoiseFlagged:    len(p.NoiseFlags) > 0,
		NoiseFlags:      p.NoiseFlags,
		ConfoundedBuild: p.Confounded,
		ThresholdPct:    th.RelPct,
		ThresholdAbs:    th.AbsBytes,
		QualityBase:     opt.qualityBase,
		QualityCand:     opt.qualityCand,
	}
	tr := testResult{metricIndex: -1, rawP: math.NaN()}

	if p.State != "" {
		// Matching already decided this one; no statistics to report.
		m.Verdict = p.State
		m.VerdictReason = p.StateMsg
		if p.BaseN > 0 {
			m.BaseP50 = median(p.BaseValues)
			m.BaseP95 = percentile(p.BaseValues, 95)
		}
		if p.CurN > 0 {
			m.CurP50 = median(p.CurValues)
			m.CurP95 = percentile(p.CurValues, 95)
		}
		return m, tr
	}

	b50 := median(p.BaseValues)
	c50 := median(p.CurValues)
	m.BaseP50, m.CurP50 = b50, c50
	m.BaseP95 = percentile(p.BaseValues, 95)
	m.CurP95 = percentile(p.CurValues, 95)
	m.AbsDelta = c50 - b50
	if b50 != 0 {
		rel := m.AbsDelta / b50 * 100
		m.RelDeltaPct = &rel
	}

	// Direction comes from the metric contract. A directionless metric
	// reports movement and nothing else.
	signed, directional := signedDelta(p.Direction, m.AbsDelta)
	m.Signed = signed
	if b50 != 0 && directional {
		sr := signed / b50 * 100
		m.SignedRel = &sr
	}

	// Rule 1: practical threshold, max(rel_thr * base_p50, abs_floor).
	relThr := th.RelPct / 100 * b50
	eff := th.Effective(b50)
	m.ThresholdBytes = relThr
	m.ThresholdUsed = eff
	m.ThresholdMet = math.Abs(signed) >= eff

	// Rule 2: statistical evidence.
	statOK, statReason := statisticalEvidence(p, &m)
	if m.RawP != nil {
		tr.rawP = *m.RawP
	}

	m.Verdict, m.VerdictReason = verdictOf(p, th, m, statOK, statReason)

	// A partial sample loss does not stop the comparison, but the reader
	// has to know it happened: the verdict rests on fewer samples than the
	// plan asked for.
	if p.State == "" && p.StateMsg != "" {
		if m.VerdictReason == "" {
			m.VerdictReason = p.StateMsg
		} else {
			m.VerdictReason += " | " + p.StateMsg
		}
	}

	// Quality may only weaken a verdict, never strengthen one.
	if opt.qualityBase == "poor" || opt.qualityCand == "poor" {
		if m.Verdict == VerdictRegressed || m.Verdict == VerdictImproved {
			m.Verdict = VerdictInconclusive
			m.VerdictReason = ReasonPoorRunQuality
		}
	}
	// The quick profile is directional only: its plan is too small to
	// support a verdict, so movement is reported without a claim.
	if opt.profileBase == ProfileQuick || opt.profileCand == ProfileQuick {
		if m.Verdict == VerdictRegressed || m.Verdict == VerdictImproved {
			m.Verdict = VerdictInformational
			m.VerdictReason = ReasonQuickProfile
		}
	}
	if opt.AllowCrossHost {
		switch m.Verdict {
		case VerdictRegressed, VerdictImproved, VerdictTailRegressed, VerdictTailImproved:
			m.Verdict = VerdictInformational
			m.VerdictReason = "cross-host comparison: informational only"
		}
	}
	if p.Confounded {
		m.VerdictReason += " | confounded_build: toolchain/flags differ"
	}
	return m, tr
}

// signedDelta applies the metric's declared direction: positive always
// means worse, so every downstream threshold test reads the same way.
func signedDelta(direction string, abs float64) (signed float64, directional bool) {
	switch direction {
	case schemaLowerIsBetter:
		return abs, true
	case schemaHigherIsBetter:
		return -abs, true
	default:
		// informational: report the delta, assign no direction.
		return 0, false
	}
}

// statisticalEvidence runs the plan's two required tests on the median
// difference: a bootstrap CI that excludes zero, and a Mann-Whitney
// test. The Hodges-Lehmann shift is the reported effect estimate either
// way, including when no test runs.
func statisticalEvidence(p MetricPair, m *Metric) (ok bool, reason string) {
	m.HLShift = stats.HodgesLehmann(p.BaseValues, p.CurValues)
	if p.BaseN < minNPerSide || p.CurN < minNPerSide {
		return false, fmt.Sprintf("%s: n=%d/%d, need %d per side",
			ReasonMannWhitneyNSmall, p.BaseN, p.CurN, minNPerSide)
	}
	// Deterministic bootstrap of the median difference. Paired only
	// when both sides came from one interleaved run, which is what
	// makes per-round pairing valid.
	lo, hi := stats.DifferenceCI50(p.BaseValues, p.CurValues, p.Paired, p.BootstrapSeed)
	m.CI = &[2]float64{lo, hi}
	m.CIExcludesZero = !(lo <= 0 && hi >= 0)

	_, pRaw := stats.MannWhitney(p.BaseValues, p.CurValues)
	if math.IsNaN(pRaw) {
		return false, ReasonMannWhitneyNSmall
	}
	m.RawP = &pRaw

	// The plan requires both: an interval that excludes zero and a
	// two-sided p below the family level. Significance alone is never a
	// verdict, and neither is a threshold alone.
	if !m.CIExcludesZero {
		return false, ReasonCIIncludesZero
	}
	if pRaw >= familyAlpha {
		return false, fmt.Sprintf("mann_whitney p=%.3g >= %g before family correction", pRaw, familyAlpha)
	}
	return true, ""
}

// verdictOf combines the three evidence rules of plan §10.3 into a
// state. It never turns statistical significance alone into a verdict,
// and never turns a large noisy percentage into one either.
func verdictOf(p MetricPair, th Threshold, m Metric, statOK bool, statReason string) (string, string) {
	// Rule 3 first: noise can block an otherwise qualifying verdict, and
	// it is checked before the rules below so its reason wins.
	if noiseDemandDouble(p, m) {
		return VerdictInconclusive, ReasonNoisyNotDouble
	}

	if p.Direction == schemaInformational {
		if statOK && m.ThresholdMet {
			return VerdictInformational, "changed beyond threshold; directionless metric, no verdict"
		}
		return VerdictUnchanged, "directionless metric; movement reported, no verdict"
	}

	switch {
	case m.ThresholdMet && statOK:
		if m.Signed > 0 {
			return VerdictRegressed, "beyond threshold with statistical evidence"
		}
		if m.Signed < 0 {
			return VerdictImproved, "beyond threshold with statistical evidence"
		}
		return VerdictUnchanged, "no directional movement"
	case m.ThresholdMet && !statOK:
		// Practically large, statistically unresolved.
		return VerdictInconclusive, "beyond threshold but statistically unresolved: " + statReason
	case !m.ThresholdMet && statOK:
		// Statistically resolvable, practically small.
		return VerdictUnchanged, "statistically significant but below practical threshold"
	default:
		return VerdictUnchanged, "below practical threshold and no statistical evidence"
	}
}

// noiseDemandDouble implements rule 3: when either side is noisy,
// bimodal or drifting, an effect must reach 2x threshold or the verdict
// is inconclusive rather than a confident call.
func noiseDemandDouble(p MetricPair, m Metric) bool {
	if len(p.NoiseFlags) == 0 {
		return false
	}
	return math.Abs(m.Signed) < 2*m.ThresholdUsed
}

// annotateTails upgrades a p50-unchanged metric to a tail verdict when
// p95 moved beyond its own threshold. It applies the same evidence rule
// as the median (plan §10.3): the threshold is relative to the baseline
// p50, and the bootstrap CI of the p95 difference must exclude zero. A
// metric whose p95 is not resolvable at its n, or whose tail shift has
// no interval behind it, stays unchanged rather than implying a tail it
// cannot support.
func annotateTails(metrics []Metric, pairs []MetricPair) {
	byIndex := make(map[int]*MetricPair, len(pairs))
	for i := range pairs {
		byIndex[pairs[i].index] = &pairs[i]
	}
	for i := range metrics {
		m := &metrics[i]
		if m.Verdict != VerdictUnchanged {
			continue
		}
		p, ok := byIndex[i]
		if !ok || p.State != "" {
			continue
		}
		// p95 needs n >= 20 to be a stable estimate; below that the
		// plan forbids reading it as one.
		if m.BaselineN < stats.MinNForP95 || m.CandidateN < stats.MinNForP95 {
			continue
		}
		if m.Direction == schemaInformational {
			continue
		}
		// Same rule as the median: the practical bar is relative to the
		// baseline centre, not to the tail it is being compared with.
		thrTail := math.Max(m.ThresholdPct/100*m.BaseP50, m.ThresholdAbs)
		if m.ThresholdMet || thrTail == 0 {
			continue
		}
		lo, hi := stats.DifferencePercentileCI(p.BaseValues, p.CurValues, 95, p.Paired, p.BootstrapSeed)
		if math.IsNaN(lo) || math.IsNaN(hi) {
			continue
		}
		m.TailCI = &[2]float64{lo, hi}
		m.TailCIExcludesZero = !(lo <= 0 && hi >= 0)
		if !m.TailCIExcludesZero {
			continue
		}
		signed := *m.CurP95 - *m.BaseP95
		if math.Abs(signed) < thrTail {
			continue
		}
		if m.Direction == schemaHigherIsBetter {
			signed = -signed
		}
		switch {
		case signed > 0:
			m.Verdict = VerdictTailRegressed
			m.VerdictReason = "p50 unchanged; p95 beyond threshold with a CI excluding zero"
		case signed < 0:
			m.Verdict = VerdictTailImproved
			m.VerdictReason = "p50 unchanged; p95 beyond threshold with a CI excluding zero"
		}
	}
}

// annotateTradeOffs links metrics of one benchmark that moved in
// opposite directions. It annotates; it never merges or resolves them
// into a winner.
func annotateTradeOffs(metrics []Metric) []TradeOff {
	byBench := map[string][]int{}
	for i, m := range metrics {
		byBench[m.Benchmark] = append(byBench[m.Benchmark], i)
	}
	names := make([]string, 0, len(byBench))
	for b := range byBench {
		names = append(names, b)
	}
	sort.Strings(names)

	var out []TradeOff
	for _, bench := range names {
		var improved, regressed []string
		for _, i := range byBench[bench] {
			switch metrics[i].Verdict {
			case VerdictImproved:
				improved = append(improved, metrics[i].Name)
			case VerdictRegressed:
				regressed = append(regressed, metrics[i].Name)
			}
		}
		if len(improved) == 0 || len(regressed) == 0 {
			continue
		}
		// The trade-off annotates the regressed metrics; the improved
		// ones keep their own verdicts unchanged.
		for _, i := range byBench[bench] {
			if metrics[i].Verdict == VerdictRegressed {
				metrics[i].VerdictReason += " | trade_off: " + bench +
					" improved on " + join(improved) + " while regressing on " + join(regressed)
			}
		}
		out = append(out, TradeOff{
			Benchmark: bench, Improved: improved, Regressed: regressed,
			Explanation: bench + ": improved on " + join(improved) +
				" but regressed on " + join(regressed) + "; both reported, no single verdict",
		})
	}
	return out
}

func join(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	return stats.NearestRank(s, 50)
}

func percentile(values []float64, p float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	s := append([]float64(nil), values...)
	sort.Float64s(s)
	v := stats.NearestRank(s, p)
	return &v
}
