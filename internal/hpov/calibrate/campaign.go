package calibrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"forcefield/internal/hpov/bench"
	"forcefield/internal/hpov/compare"
	"forcefield/internal/hpov/runner"
	"forcefield/internal/hpov/schema"
	"forcefield/internal/hpov/subject"
)

// Default subject labels. Two labels, one binary: the comparison needs
// distinct subject labels to pair, and the campaign needs the two
// subjects to be the same file so the trial is a true A/A.
const (
	DefaultBaseLabel = "aa-base"
	DefaultHeadLabel = "aa-head"
)

// Config describes one A/A campaign.
type Config struct {
	// Benchmarks is the registered set to select from, as in a run.
	Benchmarks []bench.Benchmark
	// SubjectPath is the one binary measured as both subjects.
	SubjectPath string
	// SubjectProfile is the workload contract both presentations are
	// measured under. The zero profile measures only what needs no
	// contract, so a calibration of a real harness must name one.
	SubjectProfile subject.Profile
	Source         string
	BaseLabel      string
	HeadLabel      string
	Select         []string
	Tier           int
	Profile        string
	N              *int
	Warmup         *int
	// SeedBase is trial 0's seed; trial i uses SeedBase+i, so the
	// interleaving order stays reproducible per trial while the campaign
	// as a whole samples more than one order.
	SeedBase int64
	Repeats  int
	WorkRoot string
	// OutDir receives trial-NN.json, trial-NN-compare.json and
	// calibration.json.
	OutDir      string
	CommandLine []string
	// Thresholds is the table every trial decides against.
	Thresholds *compare.ThresholdTable
}

// Run executes the campaign and returns its document.
//
// Each trial is written as it completes, so an interrupted campaign still
// leaves usable evidence on disk even though Run returns no document.
func Run(ctx context.Context, cfg Config) (*Campaign, error) {
	if cfg.Repeats <= 0 {
		return nil, fmt.Errorf("calibrate: repeats must be >= 1, got %d", cfg.Repeats)
	}
	if cfg.SubjectPath == "" {
		return nil, fmt.Errorf("calibrate: a subject binary is required")
	}
	baseLabel, headLabel := cfg.BaseLabel, cfg.HeadLabel
	if baseLabel == "" {
		baseLabel = DefaultBaseLabel
	}
	if headLabel == "" {
		headLabel = DefaultHeadLabel
	}
	if baseLabel == headLabel {
		return nil, fmt.Errorf("calibrate: base and head labels must differ (got %q twice)", baseLabel)
	}
	opt := compare.Options{Thresholds: cfg.Thresholds}
	if opt.Thresholds == nil {
		opt.Thresholds = compare.BuiltinThresholds()
	}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return nil, fmt.Errorf("calibrate: create out dir: %w", err)
	}

	c := &Campaign{
		Schema: SchemaName, SchemaVersion: SchemaVersion,
		Kind: "aa", MethodVersion: compare.MethodVersion,
		Subjects: Subjects{
			BaseLabel: baseLabel, HeadLabel: headLabel, Path: cfg.SubjectPath,
		},
		Thresholds: Thresholds{Version: opt.Thresholds.Version, Source: opt.Thresholds.Source},
		Comparison: Comparison{
			SchemaVersion: compare.SchemaVersion, MethodVersion: compare.MethodVersion,
			FamilyAlpha: compare.FamilyAlpha(), Paired: true,
		},
		Plan: Plan{
			Profile: cfg.Profile, Repeats: cfg.Repeats,
			Warmup: cfg.Warmup, N: cfg.N, SeedBase: cfg.SeedBase,
		},
	}

	for i := 0; i < cfg.Repeats; i++ {
		seed := cfg.SeedBase + int64(i)
		c.Plan.Seeds = append(c.Plan.Seeds, seed)
		tr, err := runTrial(ctx, cfg, baseLabel, headLabel, i, seed, opt)
		if err != nil {
			return nil, fmt.Errorf("calibrate: trial %d: %w", i, err)
		}
		if tr.trial.SubjectHashes[0] != tr.trial.SubjectHashes[1] {
			// The campaign's premise is broken: the two subjects were not
			// the same build, so nothing observed here is an A/A result.
			// Refusing beats publishing a false-positive rate measured
			// across two different binaries.
			return nil, fmt.Errorf("calibrate: trial %d subjects differ (%s vs %s); "+
				"the subject binary changed during the campaign",
				i, tr.trial.SubjectHashes[0], tr.trial.SubjectHashes[1])
		}
		c.Subjects.IdenticalTrials++
		if i == 0 {
			c.Subjects.SHA256 = tr.trial.SubjectHashes[0]
			c.Subjects.SizeBytes = tr.sizeBytes
			c.Subjects.Version = tr.version
			c.Subjects.Source = tr.source
			c.Suite = Suite{Version: tr.suiteVersion, DefinitionSet: tr.definitionSet}
			c.Host = tr.host
		}
		if len(c.Plan.Benchmarks) == 0 {
			c.Plan.Benchmarks = tr.ids
		}
		c.Host.HostIDs = appendUnique(c.Host.HostIDs, tr.host.HostID)
		c.Trials = append(c.Trials, tr.trial)
	}

	c.Subjects.Identical = c.Subjects.IdenticalTrials == len(c.Trials)
	c.Host.SingleHost = len(c.Host.HostIDs) == 1
	c.Summary = Summarize(c.Trials)
	c.Conclusion = Conclusion(c)
	return c, nil
}

// Conclusion states what the campaign licenses. It is stored in the
// document so a report copied out of context keeps its caveat.
func Conclusion(c *Campaign) string {
	fp, fi := c.Summary.FalsePositives, c.Summary.FalseImprovements
	base := fmt.Sprintf(
		"A/A campaign: %d trials, %d metric decisions, %d eligible decisions. "+
			"Observed A/A regressions: %d regressed, %d tail_regressed (rate %s over %d eligible decisions). "+
			"Observed A/A improvements: %d improved, %d tail_improved (rate %s). "+
			"Trial quality: %d good, %d degraded, %d poor; quality-rejected trials withhold verdicts and are "+
			"excluded from the denominator rather than counted as passes. ",
		c.Summary.Trials, c.Summary.Decisions, c.Summary.EligibleDecisions,
		fp.Regressed, fp.TailRegressed, formatRate(fp.Rate), fp.Denominator,
		fi.Improved, fi.TailImproved, formatRate(fi.Rate),
		c.Summary.TrialsGood, c.Summary.TrialsDegraded, c.Summary.TrialsQualityRejected)
	if fp.Denominator == 0 && fi.Denominator == 0 {
		return base + "No eligible decision was recorded, so this campaign establishes nothing about the " +
			"false-positive rate and no threshold conclusion may be drawn from it."
	}
	return base + "This is an observation on one host, one build and this sample size, not a calibrated rate. " +
		"The thresholds used are HPOV's engineering defaults and are unchanged by this campaign."
}

func formatRate(r *float64) string {
	if r == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.4f", *r)
}

// trialResult is one completed trial plus the fields the campaign header
// takes from its result document.
type trialResult struct {
	trial         Trial
	ids           []string
	host          Host
	suiteVersion  string
	definitionSet string
	version       string
	source        string
	sizeBytes     int64
}

// runTrial performs one repetition: an interleaved run of the same
// binary under two labels, then the ordinary paired comparison of those
// two subjects from the result it just wrote. Nothing here special-cases
// the comparison, which is the point: a campaign must exercise the same
// decision path a real A/B would.
func runTrial(ctx context.Context, cfg Config, baseLabel, headLabel string, index int, seed int64, opt compare.Options) (*trialResult, error) {
	tag := fmt.Sprintf("trial-%02d", index)
	resultPath := filepath.Join(cfg.OutDir, tag+".json")
	cmpPath := filepath.Join(cfg.OutDir, tag+"-compare.json")

	outcome, err := runner.Run(ctx, runner.Config{
		Benchmarks: cfg.Benchmarks,
		Subjects: cfg.SubjectProfile.ApplyTo([]bench.Subject{
			{Label: baseLabel, Path: cfg.SubjectPath},
			{Label: headLabel, Path: cfg.SubjectPath},
		}),
		SubjectProfile: cfg.SubjectProfile,
		SubjectSource:  cfg.Source,
		Select:         cfg.Select,
		Tier:           cfg.Tier,
		Profile:        cfg.Profile,
		N:              cfg.N,
		Warmup:         cfg.Warmup,
		Seed:           seed,
		Out:            resultPath,
		WorkRoot:       cfg.WorkRoot,
		CommandLine:    cfg.CommandLine,
	})
	if err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}
	cmp, err := compare.Subjects(outcome.Path, baseLabel, headLabel, opt)
	if err != nil {
		return nil, fmt.Errorf("compare subjects: %w", err)
	}
	if err := cmp.Write(cmpPath); err != nil {
		return nil, fmt.Errorf("write comparison: %w", err)
	}

	tr := Trial{
		Index: index, Seed: seed,
		RunID:  outcome.Result.Run.ID,
		Result: resultPath, Decision: cmpPath,
		Quality: Quality{
			Label: outcome.Result.Run.Quality.Label,
			Flags: outcome.Result.Run.Quality.Flags,
		},
		// A poor run cannot support a verdict, so its comparison is
		// inconclusive by construction. Recording that here is what
		// lets the summary exclude it instead of scoring it.
		QualityRejected: outcome.Result.Run.Quality.Label == "poor",
		BenchmarkErrors: outcome.BenchmarkErrors,
		SubjectHashes:   hashesOf(outcome.Result, baseLabel, headLabel),
		Coverage:        cmp.Coverage,
	}
	for _, m := range cmp.Metrics {
		tr.Metrics = append(tr.Metrics, metricFrom(m))
	}

	out := &trialResult{
		trial:         tr,
		ids:           selectedIDs(outcome.Result),
		suiteVersion:  outcome.Result.Suite.Version,
		definitionSet: outcome.Result.Suite.DefinitionSet,
		host: Host{
			OS: outcome.Result.Host.OS, Arch: outcome.Result.Host.Arch,
			CPUModel:    outcome.Result.Host.CPU.Model,
			LogicalCPUs: outcome.Result.Host.CPU.Logical,
			ClockSource: outcome.Result.Host.Clock.Source,
			ClockResNs:  outcome.Result.Host.Clock.ResolutionNs,
			PowerSource: outcome.Result.Host.Power.Source,
			PowerPlan:   outcome.Result.Host.Power.Plan,
			HostID:      outcome.Result.Host.HostID,
		},
	}
	for _, s := range outcome.Result.Subjects {
		if s.Label == baseLabel {
			out.version, out.source = s.Version, s.Binary.Source
			out.sizeBytes = s.Binary.SizeBytes
		}
	}
	return out, nil
}

// hashesOf returns the content hash each side presented. A trial whose
// two hashes differ is not an A/A trial.
func hashesOf(r *schema.Result, labels ...string) [2]string {
	var out [2]string
	for i, label := range labels {
		for _, s := range r.Subjects {
			if s.Label == label {
				out[i] = s.Binary.SHA256
			}
		}
	}
	return out
}

// selectedIDs lists the benchmark ids a trial actually ran, so the
// campaign records the real selection rather than the requested globs.
func selectedIDs(r *schema.Result) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range r.Benchmarks {
		if !seen[b.ID] {
			seen[b.ID] = true
			out = append(out, b.ID)
		}
	}
	sort.Strings(out)
	return out
}

func appendUnique(dst []string, v string) []string {
	for _, x := range dst {
		if x == v {
			return dst
		}
	}
	return append(dst, v)
}

// Write persists the campaign document.
func (c *Campaign) Write(path string) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// Read loads a campaign document and checks its identity, so evidence
// from a future method or schema is not read as this one's.
func Read(path string) (*Campaign, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Campaign
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse calibration %s: %w", path, err)
	}
	switch {
	case c.Schema != SchemaName:
		return nil, fmt.Errorf("calibration %s: schema is %q, want %q", path, c.Schema, SchemaName)
	case c.SchemaVersion != SchemaVersion:
		return nil, fmt.Errorf("calibration %s: schema_version %q, want %q", path, c.SchemaVersion, SchemaVersion)
	}
	return &c, nil
}
