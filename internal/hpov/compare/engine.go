package compare

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"forcefield/internal/hpov/schema"
)

// Result compares two hpov.result documents.
//
// Both files are validated first: comparing against a document whose
// stored statistics do not match its own samples would produce verdicts
// that cannot be reproduced.
func Result(basePath, candPath string, opt Options) (*Comparison, error) {
	base, cand, err := loadPair(basePath, candPath)
	if err != nil {
		return nil, err
	}
	return Build(base, cand, opt, basePath, candPath)
}

// Subjects compares two subject entries inside one interleaved run.
//
// This is the paired form: because both sides ran round-robin in one
// process on one host, per-round pairing is valid and machine drift
// cancels, which is what makes absolute thresholds portable (plan
// §10.6).
func Subjects(resultPath, baseLabel, headLabel string, opt Options) (*Comparison, error) {
	r, problems, err := schema.ValidateFile(resultPath)
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("result %s has %d validation problems; fix them first: %s",
			resultPath, len(problems), problems[0])
	}
	return buildSubjects(r, baseLabel, headLabel, opt, resultPath)
}

func loadPair(basePath, candPath string) (*schema.Result, *schema.Result, error) {
	base, bProblems, err := schema.ValidateFile(basePath)
	if err != nil {
		return nil, nil, fmt.Errorf("baseline: %w", err)
	}
	cand, cProblems, err := schema.ValidateFile(candPath)
	if err != nil {
		return nil, nil, fmt.Errorf("candidate: %w", err)
	}
	if len(bProblems) > 0 {
		return nil, nil, fmt.Errorf("baseline %s has %d validation problems; fix them first: %s",
			basePath, len(bProblems), bProblems[0])
	}
	if len(cProblems) > 0 {
		return nil, nil, fmt.Errorf("candidate %s has %d validation problems; fix them first: %s",
			candPath, len(cProblems), cProblems[0])
	}
	// Plan §10.5: MINOR/PATCH differences compare on shared fields, but a
	// MAJOR difference means the documents do not describe the same thing,
	// and comparing them would silently drop whatever changed meaning.
	if major(base.SchemaVersion) != major(cand.SchemaVersion) {
		return nil, nil, fmt.Errorf("schema_version major differs: baseline %s, candidate %s; "+
			"re-run the candidate with the same tool", base.SchemaVersion, cand.SchemaVersion)
	}
	return base, cand, nil
}

// major returns the MAJOR component of a version string, or "" when it
// is not a version at all.
func major(version string) string {
	head, _, _ := strings.Cut(version, ".")
	return head
}

// Build compares two already-loaded results. Exposed so tests can
// construct results directly instead of writing files.
func Build(base, cand *schema.Result, opt Options, basePath, candPath string) (*Comparison, error) {
	thresholds := opt.Thresholds
	if thresholds == nil {
		thresholds = BuiltinThresholds()
	}
	opt.Thresholds = thresholds
	opt.qualityBase = base.Run.Quality.Label
	opt.qualityCand = cand.Run.Quality.Label
	opt.profileBase = base.Suite.Profile
	opt.profileCand = cand.Suite.Profile

	pairs, coverage, warnings := Match(base, cand, opt.AllowCrossHost)
	if base.Host.HostID != cand.Host.HostID && opt.AllowCrossHost {
		warnings = append(warnings,
			"host fingerprint differs: verdicts are informational, absolute values are not the same quantity")
	}

	c := &Comparison{
		Schema:        SchemaName,
		SchemaVersion: SchemaVersion,
		MethodVersion: MethodVersion,
		GeneratedFrom: Input{
			Baseline: Side{
				Role: "baseline", Path: basePath, RunID: base.Run.ID,
				SuiteVersion: base.Suite.Version, Profile: base.Suite.Profile,
				Quality: base.Run.Quality.Label, HostID: base.Host.HostID,
				OS: base.Host.OS, Arch: base.Host.Arch,
			},
			Candidate: Side{
				Role: "candidate", Path: candPath, RunID: cand.Run.ID,
				SuiteVersion: cand.Suite.Version, Profile: cand.Suite.Profile,
				Quality: cand.Run.Quality.Label, HostID: cand.Host.HostID,
				OS: cand.Host.OS, Arch: cand.Host.Arch,
			},
		},
		Options: opt,
		Method: Method{
			FamilyAlpha:         familyAlpha,
			HolmCorrection:      true,
			StatisticalEvidence: []string{"bootstrap_ci_of_median_difference_excludes_zero", "mann_whitney_p_lt_0.01"},
			EffectEstimate:      "hodges_lehmann_shift",
			BootstrapResamples:  cand.Run.Bootstrap.Resamples,
			BootstrapSeed:       cand.Run.Bootstrap.Seed,
			NoiseMultiplier:     2,
			DirectionalOnly:     false,
		},
		Coverage: coverage,
		Warnings: warnings,
	}

	metrics, tests, metricCoverage, decided := evaluate(pairs, base, cand, &opt, c.Method.BootstrapSeed)
	c.Coverage = append(c.Coverage, metricCoverage...)
	c.Method.ThresholdSource = opt.Thresholds.Source
	c.Method.ThresholdVersion = opt.Thresholds.Version
	c.Method.DirectionalOnly = opt.profileBase == ProfileQuick || opt.profileCand == ProfileQuick
	applyHolm(tests, familyAlpha, metrics)
	annotateTails(metrics, decided)
	tradeOffs := annotateTradeOffs(metrics)
	c.Metrics = metrics
	c.TradeOffs = tradeOffs
	c.Summary = summarise(metrics)
	sortMetrics(c.Metrics)
	return c, nil
}

// buildSubjects is the paired path inside one interleaved run.
func buildSubjects(r *schema.Result, baseLabel, headLabel string, opt Options, path string) (*Comparison, error) {
	thresholds := opt.Thresholds
	if thresholds == nil {
		thresholds = BuiltinThresholds()
	}
	opt.Thresholds = thresholds
	opt.qualityBase = r.Run.Quality.Label
	opt.qualityCand = r.Run.Quality.Label
	opt.profileBase = r.Suite.Profile
	opt.profileCand = r.Suite.Profile

	var pairs []ComparisonInput
	var coverage []Coverage
	for i := range r.Benchmarks {
		b := &r.Benchmarks[i]
		if b.Subject != baseLabel {
			continue
		}
		head := findSubjectBenchmark(r, b.ID, headLabel)
		if head == nil {
			coverage = append(coverage, Coverage{
				Benchmark: b.ID, Reason: classifyAbsent(b.Status, CoverageRemoved)})
			continue
		}
		pairs = append(pairs, ComparisonInput{
			Baseline: b, Candidate: head,
			BaselineSubject: baseLabel, CandidateSubject: headLabel,
			Paired: true,
		})
	}

	c := &Comparison{
		Schema:        SchemaName,
		SchemaVersion: SchemaVersion,
		MethodVersion: MethodVersion,
		GeneratedFrom: Input{
			Baseline: Side{Role: "baseline", Path: path, RunID: r.Run.ID,
				SuiteVersion: r.Suite.Version, Profile: r.Suite.Profile,
				Quality: r.Run.Quality.Label, HostID: r.Host.HostID,
				OS: r.Host.OS, Arch: r.Host.Arch,
				FromSameFile: true, Subject: baseLabel},
			Candidate: Side{Role: "candidate", Path: path, RunID: r.Run.ID,
				SuiteVersion: r.Suite.Version, Profile: r.Suite.Profile,
				Quality: r.Run.Quality.Label, HostID: r.Host.HostID,
				OS: r.Host.OS, Arch: r.Host.Arch,
				FromSameFile: true, Subject: headLabel},
		},
		Options: opt,
		Method: Method{
			FamilyAlpha:         familyAlpha,
			HolmCorrection:      true,
			StatisticalEvidence: []string{"paired_bootstrap_ci_of_median_difference_excludes_zero", "mann_whitney_p_lt_0.01"},
			EffectEstimate:      "hodges_lehmann_shift",
			BootstrapResamples:  r.Run.Bootstrap.Resamples,
			BootstrapSeed:       r.Run.Bootstrap.Seed,
			NoiseMultiplier:     2,
		},
		Coverage: coverage,
	}
	metrics, tests, metricCoverage, decided := evaluate(pairs, r, r, &opt, c.Method.BootstrapSeed)
	c.Coverage = append(c.Coverage, metricCoverage...)
	c.Method.ThresholdSource = opt.Thresholds.Source
	c.Method.ThresholdVersion = opt.Thresholds.Version
	c.Method.DirectionalOnly = opt.profileBase == ProfileQuick || opt.profileCand == ProfileQuick
	applyHolm(tests, familyAlpha, metrics)
	annotateTails(metrics, decided)
	c.TradeOffs = annotateTradeOffs(metrics)
	c.Metrics = metrics
	c.Summary = summarise(metrics)
	sortMetrics(c.Metrics)
	return c, nil
}

func findSubjectBenchmark(r *schema.Result, id, subject string) *schema.Benchmark {
	for i := range r.Benchmarks {
		if r.Benchmarks[i].ID == id && r.Benchmarks[i].Subject == subject {
			return &r.Benchmarks[i]
		}
	}
	return nil
}

// evaluate runs the per-metric decision for every matched pair,
// collecting the tests that need family correction and the coverage
// entries the pairs produced. Metric-level coverage is returned rather
// than dropped: a metric present on one side only must appear in the
// report with a reason, or its absence reads as a pass.
func evaluate(pairs []ComparisonInput, base, cand *schema.Result, opt *Options, seed int64) ([]Metric, []testResult, []Coverage, []MetricPair) {
	var metrics []Metric
	var tests []testResult
	var coverage []Coverage
	var decided []MetricPair
	for _, p := range pairs {
		mp, cov := MetricMatch(p.Baseline, p.Candidate, base, cand, p.Paired, opt.AllowCrossHost, seed)
		coverage = append(coverage, cov...)
		for _, q := range mp {
			th, ok := opt.Thresholds.For(q.Benchmark)
			if !ok {
				// No stated threshold means no practical verdict; the
				// movement is still reported.
				th = Threshold{Glob: q.Benchmark, RelPct: 0, AbsBytes: 0,
					Note: "no threshold in the plan table for this benchmark family"}
			}
			m, tr := decide(q, th, opt)
			if tr.rawP == tr.rawP { // not NaN: a test actually ran
				tr.metricIndex = len(metrics)
				tests = append(tests, tr)
			} else {
				tr.metricIndex = -1
			}
			q.index = len(metrics)
			decided = append(decided, q)
			metrics = append(metrics, m)
		}
	}
	return metrics, tests, coverage, decided
}

// applyHolm corrects the family of tests that actually ran and writes
// the outcome back onto each metric.
func applyHolm(tests []testResult, alpha float64, metrics []Metric) {
	idxs := make([]int, 0, len(tests))
	for i := range tests {
		if tests[i].metricIndex >= 0 {
			idxs = append(idxs, i)
		}
	}
	holmOnSlice(tests, idxs, alpha)
	for i := range tests {
		tr := tests[i]
		if tr.metricIndex < 0 {
			continue
		}
		m := &metrics[tr.metricIndex]
		m.HolmRank = tr.rank
		m.HolmAdjusted = tr.survives
		adj := tr.adjP
		m.MannWhitneyP = &adj
		// A verdict that leaned on the raw p-value is only as strong as
		// the family-corrected one.
		if !tr.survives && (m.Verdict == VerdictRegressed || m.Verdict == VerdictImproved) {
			was := m.Verdict
			m.Verdict = VerdictInconclusive
			m.VerdictReason = ReasonHolmAdjusted + " (raw p=" + fmt.Sprintf("%.2g", tr.rawP) +
				" vs adjusted " + fmt.Sprintf("%.2g", tr.adjP) + ")"
			_ = was
		}
	}
}

// summarise counts verdicts. There is deliberately no total score.
func summarise(metrics []Metric) Summary {
	s := Summary{Counts: map[string]int{}}
	for _, m := range metrics {
		s.Counts[m.Verdict]++
		s.Reported++
		if m.Verdict == VerdictRegressed || m.Verdict == VerdictTailRegressed {
			s.Regressed++
		}
	}
	return s
}

// sortMetrics gives a stable, readable order: benchmark then metric.
func sortMetrics(metrics []Metric) {
	sort.SliceStable(metrics, func(i, j int) bool {
		if metrics[i].Benchmark != metrics[j].Benchmark {
			return metrics[i].Benchmark < metrics[j].Benchmark
		}
		return metrics[i].Name < metrics[j].Name
	})
}

// Regressed reports whether any metric carries a regression verdict.
// Used by the CLI exit code; it is not a score.
func (c *Comparison) Regressed() bool {
	for _, m := range c.Metrics {
		if m.Verdict == VerdictRegressed || m.Verdict == VerdictTailRegressed {
			return true
		}
	}
	return false
}

// Write persists a comparison as its own versioned document.
func (c *Comparison) Write(path string) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// Read loads a comparison document.
//
// The document is checked on the way in: a comparison written by another
// tool, or by a version whose verdicts meant something else, must not be
// read as if it were this one's output.
func Read(path string) (*Comparison, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Comparison
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse comparison %s: %w", path, err)
	}
	switch {
	case c.Schema != SchemaName:
		return nil, fmt.Errorf("comparison %s: schema is %q, want %q", path, c.Schema, SchemaName)
	case major(c.SchemaVersion) != major(SchemaVersion):
		return nil, fmt.Errorf("comparison %s: schema_version %s is not major %s",
			path, c.SchemaVersion, SchemaVersion)
	case c.MethodVersion != MethodVersion:
		return nil, fmt.Errorf("comparison %s: method_version %q, want %q; the decision rules changed",
			path, c.MethodVersion, MethodVersion)
	}
	return &c, nil
}

// Determinism re-runs the decision over the same inputs and reports
// whether the output is byte-identical. A comparison that varies
// between runs on the same data cannot be audited.
func Determinism(base, cand *schema.Result, opt Options) (bool, error) {
	a, err := Build(base, cand, opt, "base", "cand")
	if err != nil {
		return false, err
	}
	b, err := Build(base, cand, opt, "base", "cand")
	if err != nil {
		return false, err
	}
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb), nil
}
