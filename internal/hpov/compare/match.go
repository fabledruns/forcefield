package compare

import (
	"fmt"
	"sort"
	"strings"

	"forcefield/internal/hpov/schema"
)

// Match pairs baseline and candidate benchmarks by id, and reports
// what did not pair.
//
// Nothing is dropped: a benchmark on one side only appears in Coverage
// with a reason, because a silent absence reads as a pass.
func Match(base, cand *schema.Result, allowCrossHost bool) (pairs []ComparisonInput, coverage []Coverage, warnings []string) {
	byID := map[string]*schema.Benchmark{}
	for i := range base.Benchmarks {
		byID[base.Benchmarks[i].ID] = &base.Benchmarks[i]
	}
	candByID := map[string]*schema.Benchmark{}
	for i := range cand.Benchmarks {
		candByID[cand.Benchmarks[i].ID] = &cand.Benchmarks[i]
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		b := byID[id]
		c, ok := candByID[id]
		if !ok {
			coverage = append(coverage, Coverage{
				Benchmark: id, Reason: classifyAbsent(b.Status, CoverageRemoved),
			})
			continue
		}
		pairs = append(pairs, ComparisonInput{Baseline: b, Candidate: c})
	}
	for id, c := range candByID {
		if _, ok := byID[id]; !ok {
			coverage = append(coverage, Coverage{
				Benchmark: id, Reason: classifyAbsent(c.Status, CoverageNew),
			})
		}
	}

	// An environment difference applies to every metric, so it is
	// reported once here as a warning and again per metric by
	// applyCompatibility, which is where the verdict is decided.
	if msg, fatal := environmentProblem(base, cand); msg != "" && !fatal {
		warnings = append(warnings, msg+": every verdict is informational only "+
			"(re-run with --allow-cross-host to say so explicitly)")
	}
	return pairs, coverage, warnings
}

// classifyAbsent names why a benchmark exists on only one side.
func classifyAbsent(status, missing string) string {
	switch status {
	case schema.StatusUnsupported:
		return CoverageUnsupported
	case schema.StatusSkipped:
		return CoverageSkipped
	case schema.StatusInvalid, schema.StatusError:
		return CoverageInvalid
	default:
		return missing
	}
}

// environmentProblem reports the run-level incompatibility and whether
// it is fatal. Kept separate from per-metric matching because a host
// difference is not a benchmark's fault.
//
// fatal means the two sides are not the same quantity at all: a different
// OS or architecture measures something else, and no flag can make that
// comparable. A host fingerprint difference is the same quantity measured
// elsewhere — slower, busier, thermally different — so it is reported and
// degrades every verdict to informational, which the user may accept
// explicitly but the tool never treats as agreement.
func environmentProblem(base, cand *schema.Result) (msg string, fatal bool) {
	if base.Host.OS != cand.Host.OS {
		return fmt.Sprintf("platform differs: baseline %s, candidate %s",
			base.Host.OS, cand.Host.OS), true
	}
	if base.Host.Arch != cand.Host.Arch {
		return fmt.Sprintf("architecture differs: baseline %s, candidate %s",
			base.Host.Arch, cand.Host.Arch), true
	}
	if base.Host.HostID != cand.Host.HostID {
		return fmt.Sprintf("host fingerprint differs: baseline %s, candidate %s",
			base.Host.HostID, cand.Host.HostID), false
	}
	return "", false
}

// MetricPair is one matched metric with its samples already extracted
// and validity applied.
type MetricPair struct {
	Benchmark string
	Name      string
	Unit      string
	Direction string
	// PlatformSemantics is the baseline's tag; a mismatch is
	// not_comparable rather than a regression.
	PlatformSemantics string

	BaseValues []float64
	CurValues  []float64
	// Paired holds per-round differences when both sides came from one
	// interleaved run, which makes the bootstrap paired.
	Paired bool
	// BootstrapSeed reproduces the CI. It comes from the run's recorded
	// bootstrap seed so an interval can be recomputed.
	BootstrapSeed int64
	BaseN         int
	CurN          int
	// index is where this pair landed in the flattened metric list, so
	// the later passes (family correction, tail evidence) can find the
	// samples again without recomputing the match.
	index int
	// Missing/unavailable/invalid state, evaluated in order.
	State    string
	StateMsg string
	// NoiseFlags carries noisy:/bimodal:/drift: from either side.
	NoiseFlags []string
	Confounded bool
}

// MetricMatch pairs metrics within one benchmark and decides which
// pairs may be compared at all.
//
// The compatibility rules are plan §10.1, in the order they can be
// decided cheaply: identity, then definition, then the environment that
// determines whether the numbers mean the same thing.
func MetricMatch(b, c *schema.Benchmark, base, cand *schema.Result, paired, allowCrossHost bool, seed int64) ([]MetricPair, []Coverage) {
	baseIdx := map[string]*schema.Metric{}
	baseOrder := []string{}
	for i := range b.Metrics {
		baseIdx[b.Metrics[i].Name] = &b.Metrics[i]
		baseOrder = append(baseOrder, b.Metrics[i].Name)
	}
	candIdx := map[string]*schema.Metric{}
	candOrder := []string{}
	for i := range c.Metrics {
		candIdx[c.Metrics[i].Name] = &c.Metrics[i]
		candOrder = append(candOrder, c.Metrics[i].Name)
	}

	var pairs []MetricPair
	var coverage []Coverage

	for _, name := range baseOrder {
		bm := baseIdx[name]
		cm, ok := candIdx[name]
		if !ok {
			coverage = append(coverage, Coverage{Benchmark: b.ID, Name: name, Reason: CoverageMissing})
			continue
		}
		p := MetricPair{Benchmark: b.ID, Name: name, Unit: bm.Unit,
			Direction: bm.Direction, PlatformSemantics: bm.PlatformSemantics,
			Paired: paired}
		applyCompatibility(&p, b, c, bm, cm, base, cand, allowCrossHost, seed)
		pairs = append(pairs, p)
	}
	for _, name := range candOrder {
		if _, ok := baseIdx[name]; !ok {
			coverage = append(coverage, Coverage{Benchmark: c.ID, Name: name, Reason: CoverageNew})
		}
	}
	return pairs, coverage
}

// applyCompatibility decides the match state. The first rule that fires
// wins, and each failure names itself so the report can explain it.
func applyCompatibility(p *MetricPair, b, c *schema.Benchmark, bm, cm *schema.Metric, base, cand *schema.Result, allowCrossHost bool, seed int64) {
	p.BootstrapSeed = seed
	// Definition identity: same name must mean the same measurement.
	switch {
	case bm.Unit != cm.Unit:
		p.State = VerdictIncompatible
		p.StateMsg = fmt.Sprintf("unit differs: %s vs %s", bm.Unit, cm.Unit)
		return
	case bm.Direction != cm.Direction:
		p.State = VerdictIncompatible
		p.StateMsg = fmt.Sprintf("direction differs: %s vs %s", bm.Direction, cm.Direction)
		return
	case bm.PlatformSemantics != cm.PlatformSemantics:
		p.State = VerdictNotComparable
		p.StateMsg = fmt.Sprintf("platform semantics differ: %q vs %q",
			bm.PlatformSemantics, cm.PlatformSemantics)
		return
	case b.DefinitionVersion != c.DefinitionVersion:
		p.State = VerdictIncompatible
		p.StateMsg = fmt.Sprintf("definition_version differs: %d vs %d",
			b.DefinitionVersion, c.DefinitionVersion)
		return
	}
	if msg := paramsDiffer(b, c); msg != "" {
		p.State = VerdictIncompatible
		p.StateMsg = msg
		return
	}

	// Environment: same measurement only if it was taken the same way.
	// A host fingerprint difference alone is the same quantity measured
	// elsewhere; an OS or architecture difference is not the same
	// quantity at all and no flag makes it comparable.
	if env, fatal := environmentProblem(base, cand); env != "" && (fatal || !allowCrossHost) {
		p.State = VerdictNotComparable
		p.StateMsg = env
		return
	}
	if msg := subjectProblem(base, cand, b.Subject, c.Subject); msg != "" {
		p.State = VerdictNotComparable
		p.StateMsg = msg
		return
	}

	// Benchmark-level validity precedes metric samples: a benchmark that
	// errored has no trustworthy samples to reason about.
	switch {
	case b.Status != schema.StatusOK && c.Status != schema.StatusOK:
		p.State = VerdictInvalid
		p.StateMsg = fmt.Sprintf("benchmark status: baseline=%s candidate=%s", b.Status, c.Status)
		return
	case b.Status != schema.StatusOK:
		p.State = VerdictInvalid
		p.StateMsg = "baseline benchmark status " + b.Status
		return
	case c.Status != schema.StatusOK:
		p.State = VerdictInvalid
		p.StateMsg = "candidate benchmark status " + c.Status
		return
	}

	// Samples, with unavailable and invalid observations excluded
	// rather than read as zeros.
	bs := samplesOf(b, bm)
	cs := samplesOf(c, cm)
	p.BaseValues, p.BaseN = bs.Values, bs.n()
	p.CurValues, p.CurN = cs.Values, cs.n()
	bReason, cReason := bs.Reason, cs.Reason
	if bs.Dropped != "" {
		bReason = strings.TrimSpace(bReason + " (" + bs.Dropped + ")")
	}
	if cs.Dropped != "" {
		cReason = strings.TrimSpace(cReason + " (" + cs.Dropped + ")")
	}
	p.NoiseFlags = noiseFlags(b, c, p.Name)
	p.Confounded = confoundedBuild(base, cand)

	// Unavailable samples are not an error: the suite records them as
	// absent measurements, and the ones that remain still decide. Only a
	// side with no usable sample at all stops the comparison, and it says
	// which of the two named causes emptied it.
	switch {
	case bs.n() == 0 && cs.n() == 0:
		p.State = VerdictInconclusive
		p.StateMsg = withCause(ReasonNoValidSamples, joinReasons("baseline", bReason, "candidate", cReason))
	case bs.n() == 0:
		p.State = VerdictInconclusive
		p.StateMsg = withCause(ReasonNoValidSamples+" (baseline)", bReason)
	case cs.n() == 0:
		p.State = VerdictInconclusive
		p.StateMsg = withCause(ReasonNoValidSamples+" (candidate)", cReason)
	default:
		p.State = ""
		// A partial loss does not stop the comparison, but it travels
		// with the verdict: it says how many samples the claim rests on.
		p.StateMsg = joinReasons("baseline", bReason, "candidate", cReason)
	}
}

// withCause appends a cause to a reason, leaving the reason alone when
// there is nothing to say.
func withCause(reason, cause string) string {
	if cause == "" {
		return reason
	}
	return reason + ": " + cause
}

// sampleSet is one side's usable samples for one metric, plus what was
// dropped and why.
type sampleSet struct {
	Values []float64
	// Reason names the drop that emptied the set, or "" when samples
	// remain. It is ReasonMetricUnavailable for an absent measurement and
	// ReasonSamplesInvalid for one that failed the suite's own validity
	// contract.
	Reason string
	// Dropped describes partial loss, e.g. "3 of 20 samples unavailable".
	Dropped string
}

func (s sampleSet) n() int { return len(s.Values) }

// joinReasons renders "side: reason" pairs, skipping sides with nothing
// to report. It returns "" when there is nothing to say, so a caller can
// tell "no loss" from "loss of an unknown kind".
func joinReasons(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] != "" {
			parts = append(parts, pairs[i]+": "+pairs[i+1])
		}
	}
	return strings.Join(parts, "; ")
}

// samplesOf extracts the raw usable samples for a metric.
//
// Two things are dropped, and neither may enter a comparison as a
// measurement: iterations that failed validity, and iterations the suite
// marked unavailable for this metric. An unavailable sample is stored as a
// zero placeholder, so counting it would be worse than dropping it — a
// zero is not a small measurement, it is no measurement at all.
//
// Iterations are the authority: they carry both the validity flag and the
// per-iteration value. A metric vector is used only when the benchmark
// recorded no per-iteration values for it.
func samplesOf(bench *schema.Benchmark, m *schema.Metric) sampleSet {
	if m.Statistics == nil {
		return sampleSet{Reason: ReasonSamplesInvalid + ": no recorded statistics"}
	}
	unavailable, invalid := 0, 0
	out := make([]float64, 0, len(m.Values))
	recorded := false
	for _, it := range bench.Iterations {
		if it.Phase != "measure" {
			continue
		}
		// Whether the iteration recorded this metric at all is decided
		// before validity: a benchmark that never stored per-iteration
		// values has a different shape from one whose values were all
		// dropped.
		if _, ok := it.Values[m.Name]; ok {
			recorded = true
		}
		if !it.Valid {
			invalid++
			continue
		}
		if _, na := it.Attrs[schema.UnavailablePrefix+m.Name]; na {
			unavailable++
			continue
		}
		v, ok := it.Values[m.Name]
		if !ok {
			continue
		}
		out = append(out, v)
	}
	if !recorded {
		// No per-iteration values recorded: the metric vector is all
		// there is, and it carries no validity information of its own.
		return sampleSet{Values: m.Values}
	}
	set := sampleSet{Values: out}
	switch {
	case invalid > 0 && unavailable > 0:
		set.Reason = ReasonSamplesInvalid + " and " + ReasonMetricUnavailable
		set.Dropped = fmt.Sprintf("%d invalid and %d unavailable of %d",
			invalid, unavailable, invalid+unavailable+len(out))
	case invalid > 0:
		set.Reason = ReasonSamplesInvalid
		set.Dropped = fmt.Sprintf("%d invalid of %d", invalid, invalid+len(out))
	case unavailable > 0:
		set.Reason = ReasonMetricUnavailable
		set.Dropped = fmt.Sprintf("%d of %d samples unavailable", unavailable, unavailable+len(out))
	}
	if len(out) == 0 && set.Reason == "" {
		set.Reason = ReasonNoValidSamples
	}
	return set
}

// paramsDiffer reports the first parameter mismatch, or "".
func paramsDiffer(b, c *schema.Benchmark) string {
	keys := map[string]bool{}
	for k := range b.Params {
		keys[k] = true
	}
	for k := range c.Params {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if b.Params[k] != c.Params[k] {
			return fmt.Sprintf("param %q differs: %q vs %q", k, b.Params[k], c.Params[k])
		}
	}
	return ""
}

// subjectProblem reports a subject incompatibility for the benchmark's
// subject label, or "".
func subjectProblem(base, cand *schema.Result, baseLabel, candLabel string) string {
	bs, ok := subjectByLabel(base, baseLabel)
	if !ok {
		return "baseline subject " + baseLabel + " not found"
	}
	cs, ok := subjectByLabel(cand, candLabel)
	if !ok {
		return "candidate subject " + candLabel + " not found"
	}
	if bs.Binary.GoOS != cs.Binary.GoOS || bs.Binary.GoArch != cs.Binary.GoArch {
		return fmt.Sprintf("subject platform differs: %s/%s vs %s/%s",
			bs.Binary.GoOS, bs.Binary.GoArch, cs.Binary.GoOS, cs.Binary.GoArch)
	}
	if bs.Binary.Source != cs.Binary.Source {
		return fmt.Sprintf("subject provenance differs: %s vs %s",
			bs.Binary.Source, cs.Binary.Source)
	}
	return ""
}

func subjectByLabel(r *schema.Result, label string) (schema.Subject, bool) {
	for _, s := range r.Subjects {
		if s.Label == label {
			return s, true
		}
	}
	return schema.Subject{}, false
}

// confoundedBuild reports whether a build difference prevents
// separating a code change from a toolchain change (plan §10.5).
func confoundedBuild(base, cand *schema.Result) bool {
	for _, bs := range base.Subjects {
		cs, ok := subjectByLabel(cand, bs.Label)
		if !ok {
			continue
		}
		if buildDiffers(bs.Binary, cs.Binary) {
			return true
		}
	}
	return false
}

func buildDiffers(a, b schema.Binary) bool {
	if a.GoVersion != b.GoVersion {
		return true
	}
	if a.Ldflags != b.Ldflags {
		return true
	}
	if a.CGO != b.CGO {
		return true
	}
	if (a.Trimpath == nil) != (b.Trimpath == nil) {
		return true
	}
	if a.Trimpath != nil && b.Trimpath != nil && *a.Trimpath != *b.Trimpath {
		return true
	}
	return false
}

// noiseFlags collects the flags that mean "this data is not trustworthy
// enough for a verdict": noisy, bimodal and drift. Any other flag a
// benchmark carries (a skipped sub-step, a note about the machine) is
// left alone — treating it as noise would inflate every threshold for no
// stated reason.
//
// Flags are recorded per metric as "kind:metric", so a noisy wall_ms does
// not double the threshold for the benchmark's memory metric.
func noiseFlags(b, c *schema.Benchmark, metric string) []string {
	var out []string
	for _, set := range []struct {
		name  string
		bench *schema.Benchmark
	}{{"base", b}, {"cur", c}} {
		for _, f := range set.bench.Flags {
			kind, subject, ok := splitNoiseFlag(f)
			if !ok {
				continue
			}
			// A flag with no metric names the whole benchmark.
			if subject != "" && subject != metric {
				continue
			}
			out = append(out, set.name+":"+kind)
		}
	}
	return out
}

// splitNoiseFlag parses "kind:metric" for the three noise kinds.
func splitNoiseFlag(f string) (kind, metric string, ok bool) {
	for _, p := range []string{"noisy:", "bimodal:", "drift:"} {
		if len(f) > len(p) && f[:len(p)] == p {
			return p[:len(p)-1], f[len(p):], true
		}
	}
	return "", "", false
}
