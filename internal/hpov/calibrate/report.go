package calibrate

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Render prints the campaign as a human-readable report: what was run,
// what each trial's quality was, what verdicts appeared, how much each
// metric naturally varied, and what that does and does not license.
//
// It counts verdicts by name and reports denominators. There is no score,
// because a single number would hide the distinction between "the
// threshold suppressed a 3% wobble" and "nothing was ever decided".
func Render(c *Campaign) string {
	var b strings.Builder

	fmt.Fprintf(&b, "hpov A/A calibration  %s v%s\n", c.Schema, c.SchemaVersion)
	fmt.Fprintf(&b, "suite %s definitions %s  comparison method %s (paired=%v, alpha=%g)\n",
		c.Suite.Version, c.Suite.DefinitionSet, c.Comparison.MethodVersion,
		c.Comparison.Paired, c.Comparison.FamilyAlpha)
	fmt.Fprintf(&b, "subject %s\n  sha256 %s  size %s  %s\n",
		c.Subjects.Path, c.Subjects.SHA256, humanBytes(c.Subjects.SizeBytes), c.Subjects.Version)
	if c.Subjects.Identical {
		fmt.Fprintf(&b, "  identical subjects in every trial (%d/%d), verified by content hash\n",
			c.Subjects.IdenticalTrials, c.Summary.Trials)
	} else {
		fmt.Fprintf(&b, "  WARNING: identical subjects in only %d/%d trials\n",
			c.Subjects.IdenticalTrials, c.Summary.Trials)
	}
	fmt.Fprintf(&b, "host %s/%s %s  cpus=%d  clock=%s(%dns)  power=%s/%s  single_host=%v\n",
		c.Host.OS, c.Host.Arch, c.Host.CPUModel, c.Host.LogicalCPUs,
		c.Host.ClockSource, c.Host.ClockResNs, c.Host.PowerSource, c.Host.PowerPlan, c.Host.SingleHost)
	fmt.Fprintf(&b, "plan profile=%s repeats=%d seeds=%s\n", c.Plan.Profile, c.Plan.Repeats, seedRange(c.Plan.Seeds))
	fmt.Fprintf(&b, "benchmarks %s\n", strings.Join(c.Plan.Benchmarks, " "))
	fmt.Fprintf(&b, "thresholds %s (%s)\n", c.Thresholds.Version, c.Thresholds.Source)
	b.WriteString("\n")

	fmt.Fprintf(&b, "trial quality (poor trials withhold verdicts and are excluded from the denominator)\n")
	for _, t := range c.Trials {
		note := ""
		switch {
		case t.QualityRejected:
			note = "  quality-rejected"
		case t.BenchmarkErrors:
			note = "  has benchmark errors"
		case t.Identical:
			note = "  identical subjects"
		}
		fmt.Fprintf(&b, "  trial %02d seed=%d run=%s quality=%-8s%s\n",
			t.Index, t.Seed, short(t.RunID), t.Quality.Label, note)
		if len(t.Quality.Flags) > 0 {
			fmt.Fprintf(&b, "           flags %s\n", strings.Join(t.Quality.Flags, ","))
		}
	}
	b.WriteString("\n")

	fp := c.Summary.FalsePositives
	fmt.Fprintf(&b, "A/A regressions: %d regressed + %d tail_regressed = %d over %d eligible decisions",
		fp.Regressed, fp.TailRegressed, fp.Total, fp.Denominator)
	if fp.Rate != nil {
		fmt.Fprintf(&b, " (rate %.4f)", *fp.Rate)
	} else {
		fmt.Fprintf(&b, " (no eligible decision: no rate is reported)")
	}
	b.WriteString("\n")
	fi := c.Summary.FalseImprovements
	fmt.Fprintf(&b, "A/A improvements: %d improved + %d tail_improved = %d over the same denominator",
		fi.Improved, fi.TailImproved, fi.Total)
	if fi.Rate != nil {
		fmt.Fprintf(&b, " (rate %.4f)\n", *fi.Rate)
	} else {
		b.WriteString("\n")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "decisions %d total, %d eligible, %d quality-rejected; withheld: %s\n",
		c.Summary.Decisions, c.Summary.EligibleDecisions,
		c.Summary.QualityRejectedDecisions, counts(c.Summary.WithheldReasons))
	fmt.Fprintf(&b, "verdicts %s\n", counts(c.Summary.Verdicts))
	b.WriteString("\n")

	fmt.Fprintf(&b, "per metric (n/elig = eligible / total decisions; d%% = median |delta| as %% of baseline centre)\n")
	fmt.Fprintf(&b, "%-34s %-11s %-9s %-8s %-7s %-7s %-7s %-7s %-7s\n",
		"metric", "thr", "n/elig", "d%", "maxd%", "thr_met", "ci0", "p<.01", "holm")
	for _, m := range c.Summary.PerMetric {
		fmt.Fprintf(&b, "%-34s %-11s %-9s %-8s %-7s %-7s %-7s %-7s %-7s\n",
			m.Benchmark+"/"+m.Name,
			thresholdLabel(m),
			fmt.Sprintf("%d/%d", m.EligibleDecisions, m.Decisions),
			pct(m.MedianAbsRelDeltaPct), pct(m.MaxAbsRelDeltaPct),
			itoa(m.SuppressedByThreshold), itoa(m.CIExcludesZeroTrials),
			itoa(m.RawPSignificantTrials), itoa(m.HolmSurvivedTrials))
	}
	b.WriteString("\n")

	if rejected := c.Summary.TrialsQualityRejected; rejected > 0 {
		fmt.Fprintf(&b, "%d of %d trials were quality-rejected (poor run quality): their comparisons are\n"+
			"inconclusive by construction and contribute no verdict either way.\n", rejected, c.Summary.Trials)
	}
	b.WriteString("\n")
	b.WriteString(c.Conclusion)
	b.WriteString("\n")
	return b.String()
}

// thresholdLabel renders a metric's practical threshold compactly. The
// absolute floor is in the metric's own unit, so it is formatted as such: a
// 5 ms floor rendered as "5B" would read as a byte budget.
func thresholdLabel(m MetricSummary) string {
	switch {
	case m.ThresholdAbs > 0 && m.ThresholdPct > 0:
		return fmt.Sprintf("%g%%/%s", m.ThresholdPct, humanUnit(m.Unit, m.ThresholdAbs))
	case m.ThresholdPct > 0:
		return fmt.Sprintf("%g%%", m.ThresholdPct)
	default:
		return "none"
	}
}

// humanUnit formats a value in the metric's declared unit.
func humanUnit(unit string, v float64) string {
	if unit == "bytes" || strings.HasSuffix(unit, "_bytes") {
		switch {
		case math.Abs(v) >= 1<<20:
			return fmt.Sprintf("%.1fMiB", v/(1<<20))
		case math.Abs(v) >= 1<<10:
			return fmt.Sprintf("%.0fKiB", v/(1<<10))
		default:
			return fmt.Sprintf("%.0fB", v)
		}
	}
	if unit == "ms" {
		return fmt.Sprintf("%.0fms", v)
	}
	return fmt.Sprintf("%.4g%s", v, unit)
}

func pct(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", *v)
}

func itoa(v int) string { return fmt.Sprintf("%d", v) }

func counts(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func seedRange(seeds []int64) string {
	if len(seeds) == 0 {
		return "-"
	}
	if len(seeds) == 1 {
		return fmt.Sprintf("%d", seeds[0])
	}
	return fmt.Sprintf("%d..%d", seeds[0], seeds[len(seeds)-1])
}

func short(runID string) string {
	if len(runID) <= 24 {
		return runID
	}
	return runID[:24]
}

func humanBytes(v int64) string {
	switch {
	case v >= 1<<20:
		return fmt.Sprintf("%.1fMiB", float64(v)/(1<<20))
	case v >= 1<<10:
		return fmt.Sprintf("%.0fKiB", float64(v)/(1<<10))
	default:
		return fmt.Sprintf("%dB", v)
	}
}
