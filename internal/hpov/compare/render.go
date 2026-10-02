package compare

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Render prints a human-readable comparison: per-metric verdicts with
// their evidence, the coverage section, and any trade-offs.
//
// The layout is deliberately a table of per-metric verdicts and never a
// single number: the plan rejects an aggregate score outright (§6.6),
// because no defensible weighting exists across ms, bytes and counts.
func Render(c *Comparison, opt Options) string {
	var b strings.Builder
	bi, ci := c.GeneratedFrom.Baseline, c.GeneratedFrom.Candidate
	fmt.Fprintf(&b, "compare %s (baseline) vs %s (candidate)\n", sideLabel(bi), sideLabel(ci))
	fmt.Fprintf(&b, "%s v%s  method %s  thresholds %s\n",
		SchemaName, c.SchemaVersion, c.MethodVersion, thresholdSource(c))
	if c.Method.HolmCorrection {
		fmt.Fprintf(&b, "family-wise: Holm correction at alpha=%g across %d tests\n",
			c.Method.FamilyAlpha, c.summaryTests())
	}
	if len(c.Warnings) > 0 {
		for _, w := range c.Warnings {
			fmt.Fprintf(&b, "warning: %s\n", w)
		}
	}
	b.WriteString("\n")

	if len(c.Metrics) == 0 {
		b.WriteString("no comparable metrics\n")
	}

	for _, bench := range orderedBenchmarks(c.Metrics) {
		fmt.Fprintf(&b, "%s\n", bench)
		for _, m := range c.Metrics {
			if m.Benchmark != bench {
				continue
			}
			b.WriteString("  " + renderMetric(m, opt))
		}
		b.WriteString("\n")
	}

	for _, t := range c.TradeOffs {
		fmt.Fprintf(&b, "trade_off %s: improved on %s, regressed on %s\n",
			t.Benchmark, strings.Join(t.Improved, ", "), strings.Join(t.Regressed, ", "))
	}

	if len(c.Coverage) > 0 {
		b.WriteString("coverage (not compared, with reason):\n")
		for _, cv := range c.Coverage {
			name := cv.Benchmark
			if cv.Name != "" {
				name += "." + cv.Name
			}
			fmt.Fprintf(&b, "  %-46s %s\n", name, cv.Reason)
		}
	}

	counts := make([]string, 0, len(c.Summary.Counts))
	for v := range c.Summary.Counts {
		counts = append(counts, v)
	}
	sort.Strings(counts)
	var parts []string
	for _, v := range counts {
		parts = append(parts, fmt.Sprintf("%s=%d", v, c.Summary.Counts[v]))
	}
	fmt.Fprintf(&b, "summary: %d metric(s): %s\n", c.Summary.Reported, strings.Join(parts, " "))
	return b.String()
}

func (c *Comparison) summaryTests() int {
	n := 0
	for _, m := range c.Metrics {
		if m.HolmRank > 0 {
			n++
		}
	}
	return n
}

// thresholdSource names the table that produced the thresholds, from the
// document's own record rather than from the live options: a rendered
// report of a stored comparison must say what that comparison used.
func thresholdSource(c *Comparison) string {
	v, src := c.Method.ThresholdVersion, c.Method.ThresholdSource
	switch {
	case v == "" && src == "":
		return "unknown"
	case src == "":
		return v
	case v == "":
		return src
	default:
		return v + " (" + src + ")"
	}
}

func sideLabel(s Side) string {
	label := s.Role
	if s.Subject != "" {
		label += ":" + s.Subject
	}
	if s.RunID != "" {
		label += " " + s.RunID
	}
	return label
}

func renderMetric(m Metric, opt Options) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-34s %-14s", m.Name, m.Verdict)
	if m.VerdictReason != "" {
		fmt.Fprintf(&b, "  %s", truncate(m.VerdictReason, 90))
	}
	b.WriteString("\n")

	// Evidence line: the numbers a reader needs to disagree with the
	// verdict, whether or not the verdict is confident.
	fmt.Fprintf(&b, "      base n=%d p50=%s cur n=%d p50=%s",
		m.BaselineN, bytes3(m.BaseP50), m.CandidateN, bytes3(m.CurP50))
	if m.RelDeltaPct != nil {
		fmt.Fprintf(&b, " delta=%+.1f%%", *m.RelDeltaPct)
	} else if m.AbsDelta != 0 {
		fmt.Fprintf(&b, " delta=%s", bytes3(m.AbsDelta))
	}
	fmt.Fprintf(&b, " thr=%s(%g%%/%s)",
		bytes3(m.ThresholdUsed), m.ThresholdPct, bytes3(m.ThresholdAbs))
	if m.CI != nil {
		ci := fmt.Sprintf("[%+.3g,%+.3g]", m.CI[0], m.CI[1])
		fmt.Fprintf(&b, " ci=%s%s", ci, ciExcludes(m.CIExcludesZero))
	}
	if m.MannWhitneyP != nil {
		p := fmt.Sprintf("%.2g", *m.MannWhitneyP)
		fmt.Fprintf(&b, " mw_p=%s(holm#%d survives=%v)", p, m.HolmRank, m.HolmAdjusted)
	} else if m.RawP != nil {
		fmt.Fprintf(&b, " mw_p=%.2g(raw)", *m.RawP)
	}
	if len(m.NoiseFlags) > 0 {
		fmt.Fprintf(&b, " noise=%s", strings.Join(m.NoiseFlags, ","))
	}
	if m.ConfoundedBuild {
		b.WriteString(" confounded_build")
	}
	if opt.Explain && m.CI != nil {
		fmt.Fprintf(&b, "\n      hl_shift=%+.4g effect_estimate=hodges_lehmann", m.HLShift)
	}
	b.WriteString("\n")
	return b.String()
}

func ciExcludes(ex bool) string {
	if ex {
		return "(excl0)"
	}
	return "(incl0)"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// bytes3 formats a byte count for reading; non-byte units are printed
// with three decimals so a ms value is never dressed as bytes.
func bytes3(v float64) string {
	if math.IsNaN(v) {
		return "n/a"
	}
	switch {
	case v == 0:
		return "0"
	case math.Abs(v) >= 1024*1024:
		return fmt.Sprintf("%.1fMiB", v/(1024*1024))
	case math.Abs(v) >= 1024:
		return fmt.Sprintf("%.1fKiB", v/1024)
	default:
		return fmt.Sprintf("%.3g", v)
	}
}

func orderedBenchmarks(metrics []Metric) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range metrics {
		if !seen[m.Benchmark] {
			seen[m.Benchmark] = true
			out = append(out, m.Benchmark)
		}
	}
	return out
}
