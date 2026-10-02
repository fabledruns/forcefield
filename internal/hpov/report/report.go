// Package report renders HPOV results as human-readable tables.
// run and show share this renderer. Colors stay off (no TTY
// detection games): plain aligned columns.
package report

import (
	"fmt"
	"strings"

	"forcefield/internal/hpov/schema"
)

// Render returns the full human report for a result.
func Render(r *schema.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "hpov %s profile=%s suite=%s\n", r.Suite.Version, r.Suite.Profile, r.Suite.DefinitionSet)
	fmt.Fprintf(&b, "run %s  seed=%d  quality=%s", r.Run.ID, r.Run.Seed, r.Run.Quality.Label)
	if len(r.Run.Quality.Flags) > 0 {
		fmt.Fprintf(&b, "  flags=%s", strings.Join(r.Run.Quality.Flags, ","))
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "host %s/%s env=%s cpus=%d clock=%s(%dns)",
		r.Host.OS, r.Host.Arch, r.Host.Env, r.Host.CPU.Logical,
		r.Host.Clock.Source, r.Host.Clock.ResolutionNs)
	if r.Host.CPU.Model != "" {
		fmt.Fprintf(&b, "  %s", r.Host.CPU.Model)
	}
	b.WriteByte('\n')
	cal := r.Environment.Calibration.SpawnFloorMs
	fmt.Fprintf(&b, "spawn floor: start p50=%s  end p50=%s  (n=%d/%d, never subtracted)\n",
		ms(cal.Start.P50), ms(cal.End.P50), cal.Start.N, cal.End.N)
	if r.Environment.Quality.IdleCPUPct != nil {
		fmt.Fprintf(&b, "idle cpu: %.1f%%\n", *r.Environment.Quality.IdleCPUPct)
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	b.WriteByte('\n')
	for _, bm := range r.Benchmarks {
		renderBenchmark(&b, bm)
	}
	return b.String()
}

func renderBenchmark(b *strings.Builder, bm schema.Benchmark) {
	subj := bm.Subject
	if subj == "" {
		subj = "-"
	}
	fmt.Fprintf(b, "== %s  [%s] subject=%s\n", bm.ID, bm.Status, subj)
	if bm.StatusDetail != nil {
		fmt.Fprintf(b, "   %s\n", *bm.StatusDetail)
	}
	if bm.Error != nil {
		fmt.Fprintf(b, "   error %s/%s: %s\n", bm.Error.Code, bm.Error.Phase, bm.Error.Message)
	}
	if bm.Validity != nil {
		fmt.Fprintf(b, "   validity: %s pre=%v post=%v\n",
			bm.Validity.Predicate, probeStr(bm.Validity.ProbePre), probeStr(bm.Validity.ProbePost))
	}
	for _, m := range bm.Metrics {
		renderMetric(b, m)
	}
	if len(bm.Flags) > 0 {
		fmt.Fprintf(b, "   flags: %s\n", strings.Join(bm.Flags, ", "))
	}
	for _, w := range bm.Warnings {
		fmt.Fprintf(b, "   warning: %s\n", w)
	}
	b.WriteByte('\n')
}

func probeStr(p *schema.Probe) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("ok=%v", p.OK)
}

func renderMetric(b *strings.Builder, m schema.Metric) {
	fmt.Fprintf(b, "   %-22s n/valid=", m.Name)
	if m.Statistics == nil {
		fmt.Fprintf(b, "no valid samples  values=%d\n", len(m.Values))
		return
	}
	st := m.Statistics
	fmt.Fprintf(b, "%d/%d  min=%s  p50=%s [%s]  max=%s  cv=%.2f\n",
		st.N, st.ValidN, fmtVal(m.Unit, st.Min),
		fmtVal(m.Unit, st.P50), ci(m.Unit, st.CI95P50),
		fmtVal(m.Unit, st.Max), st.RobustCV)
	fmt.Fprintf(b, "   %-22s p90=%s  p95=%s  p99=%s  mean=%s  mad=%s\n", "",
		optVal(m.Unit, st.P90, st.Nulls, "p90"), optVal(m.Unit, st.P95, st.Nulls, "p95"),
		optVal(m.Unit, st.P99, st.Nulls, "p99"),
		fmtVal(m.Unit, st.Mean), fmtVal(m.Unit, st.MAD))
}

func optVal(unit string, v *float64, nulls map[string]string, key string) string {
	if v == nil {
		if r, ok := nulls[key]; ok {
			return "null(" + r + ")"
		}
		return "null"
	}
	return fmtVal(unit, *v)
}

func ci(unit string, c [2]float64) string {
	return fmtVal(unit, c[0]) + "–" + fmtVal(unit, c[1])
}

// fmtVal formats by unit: ms with 1 decimal, bytes humanized.
func fmtVal(unit string, v float64) string {
	if unit == "bytes" || strings.HasSuffix(unit, "_bytes") || unit == "B/op" {
		if v >= 1048576 {
			return fmt.Sprintf("%.1fMiB", v/1048576)
		}
		if v >= 1024 {
			return fmt.Sprintf("%.0fKiB", v/1024)
		}
		return fmt.Sprintf("%.0fB", v)
	}
	if unit == "ms" {
		return fmt.Sprintf("%.1fms", v)
	}
	return fmt.Sprintf("%.3f%s", v, unit)
}

func ms(v float64) string { return fmt.Sprintf("%.1fms", v) }
