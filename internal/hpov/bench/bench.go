// Package bench defines the core HPOV benchmark contracts: benchmark
// identity, metric ownership, iteration plans, and the Benchmark
// interface every suite implements.
//
// Benchmarks are registered explicitly (see internal/hpov/suites):
// no init() side effects, no global state.
package bench

import (
	"context"
	"fmt"
	"strings"
	"time"

	"forcefield/internal/hpov/schema"
)

// Kind distinguishes end-to-end process benchmarks (release-comparable
// with only a binary) from source-level and static ones.
type Kind string

const (
	// KindE2E runs an external process and measures it black-box.
	KindE2E Kind = "e2e"
	// KindMicro wraps `go test -bench` output (checkout required).
	KindMicro Kind = "micro"
	// KindStatic records an exact value (size, constants).
	KindStatic Kind = "static"
)

// Metric directions. "informational" metrics are reported but never
// gate a regression verdict.
const (
	LowerIsBetter  = "lower_is_better"
	HigherIsBetter = "higher_is_better"
	Informational  = "informational"
)

// Iteration phases. Warm-up samples are stored but never enter
// statistics.
const (
	PhaseWarmup  = "warmup"
	PhaseMeasure = "measure"
)

// MetricSpec owns one metric of one benchmark. Each metric has exactly
// one owner; other benchmarks may reference but never recompute it.
type MetricSpec struct {
	Name      string
	Unit      string // "ms" | "bytes" | "ns/op" | "B/op" | "allocs/op" | "count"
	Direction string // LowerIsBetter | HigherIsBetter | Informational
	// PlatformSemantics maps os -> semantics tag when collection
	// differs per platform (e.g. windows:"peak_working_set").
	PlatformSemantics map[string]string
}

// Plan resolves how many iterations a profile runs.
type Plan struct {
	Warmup     int
	N          int
	TimeoutSec int
}

// Standard profiles shared by every benchmark unless its Spec
// overrides them.
var StandardPlans = map[string]Plan{
	"quick":    {Warmup: 2, N: 10, TimeoutSec: 60},
	"standard": {Warmup: 5, N: 30, TimeoutSec: 60},
	"full":     {Warmup: 5, N: 100, TimeoutSec: 120},
}

// Spec describes one benchmark. IDs are stable, dotted, and never
// reused; any workload/boundary change bumps DefinitionVersion.
type Spec struct {
	ID                string
	DefinitionVersion int
	Title             string
	Purpose           string
	Kind              Kind
	Tier              int // 1..3, 0 = experimental/infrastructure
	Metrics           []MetricSpec
	Platforms         []string // {"windows","linux","darwin"}; empty = all
	Requires          []string // "pty","git","rg","go","markers",...
	Params            map[string]string
	Plans             map[string]Plan // profile -> plan; nil = StandardPlans
	// Predicate is the human-readable validity rule recorded in
	// results (e.g. "exit_code==1"). Empty means exit-code checks
	// implied by the suite.
	Predicate string
	// CVThreshold overrides the robust-CV noise flag (default 0.10;
	// TUI/MCP use 0.15).
	CVThreshold float64
}

// PlanFor resolves the iteration plan for a profile.
func (s Spec) PlanFor(profile string) Plan {
	if p, ok := s.Plans[profile]; ok {
		return p
	}
	if p, ok := StandardPlans[profile]; ok {
		return p
	}
	return StandardPlans["standard"]
}

// RunnableOn reports whether this benchmark may run on the given GOOS
// and whether a missing requirement blocks it.
func (s Spec) RunnableOn(goos string, have map[string]bool) (ok bool, reason string) {
	for _, p := range s.Platforms {
		if p == goos {
			ok = true
			break
		}
	}
	if len(s.Platforms) > 0 && !ok {
		return false, "unsupported_platform"
	}
	for _, r := range s.Requires {
		if !have[r] {
			return false, "requirement_unmet:" + r
		}
	}
	return true, ""
}

// Subject is one binary under measurement plus the workload contract
// that describes how to drive it. Benchmarks never hard-code a
// product's command syntax, output text, exit status or marker names:
// they read them from Contract.
type Subject struct {
	Label string
	Path  string
	// Contract is the subject's workload semantics. The zero contract
	// measures nothing a subject-specific benchmark can use: those
	// benchmarks report unsupported, and generic ones (artifact size)
	// are unaffected.
	Contract Contract
}

// Unsupported reports a benchmark as inapplicable to this subject,
// naming the contract field that is missing. Callers return it from
// Setup so the entry carries an explicit state instead of a wrong
// measurement.
func (s Subject) Unsupported(missing string) error {
	product := s.Contract.Product
	if product == "" {
		product = "(none)"
	}
	return &SkipError{
		Status: schema.StatusUnsupported,
		Code:   schema.ErrSubjectContract,
		Detail: fmt.Sprintf("subject %s (profile %s) does not declare %s", s.Label, product, missing),
	}
}

// Iter identifies one iteration passed to Benchmark.Iterate.
type Iter struct {
	Index int
	Phase string // PhaseWarmup | PhaseMeasure
}

// RunEnv is the untimed per-run context handed to Setup.
type RunEnv struct {
	Root    string // run-scoped temp root; fixtures live under it
	Profile string // "quick" | "standard" | "full"
	Seed    int64
}

// Fixture carries benchmark-scoped isolated directories. Fixtures are
// always created outside timed regions.
type Fixture struct {
	HomeDir string
	WorkDir string
	// Timeout is the per-iteration spawn budget, resolved from the
	// Spec plan in Setup. Zero means the spawn default.
	Timeout time.Duration
}

// Observation is one iteration's raw result. Values maps owned metric
// names to values; metrics a pass does not collect are simply absent.
type Observation struct {
	Values        map[string]float64
	Valid         bool
	InvalidReason string
	Attrs         map[string]string
	// Unavailable marks owned metrics this iteration could not
	// measure, each with its reason (e.g. a missing marker endpoint).
	// Such a metric is excluded from that metric's statistics instead
	// of being reported as a real zero, and it never invalidates the
	// iteration: an unavailable optional metric is a gap in the data,
	// not a failed run.
	Unavailable map[string]string
}

// Benchmark is one HPOV benchmark: untimed setup, one timed
// iteration, untimed teardown. Setup receives the subject because
// fixtures are per (benchmark, subject): steady-state priming runs
// the subject binary, and subjects must never share fixture state.
type Benchmark interface {
	Spec() Spec
	Setup(ctx context.Context, env *RunEnv, subj Subject) (Fixture, error)
	Iterate(ctx context.Context, fx Fixture, subj Subject, it Iter) (Observation, error)
	Teardown(ctx context.Context, fx Fixture) error
}

// Registered is one explicitly registered benchmark.
type Registered struct {
	Bench Benchmark
}

// SkipError lets Setup or Iterate report a whole (benchmark, subject)
// entry as skipped/unsupported/invalid without iterations.
type SkipError struct {
	Status string // StatusSkipped | StatusUnsupported | StatusInvalid
	Detail string
	Code   string
}

func (e *SkipError) Error() string { return e.Status + ": " + e.Detail }

// Prober is an optional Benchmark extension: a markers-on probe run
// before and after the wall pass confirming the workload still
// reaches its intended boundary.
type Prober interface {
	Probe(ctx context.Context, fx Fixture, subj Subject) (ok bool, checks map[string]bool, err error)
}

// SubjectSpecifier is an optional Benchmark extension for benchmarks
// whose shape depends on the subject's workload contract. The TUI
// families derive their metric set from the subject's marker names, so
// their metrics cannot be known before the subject is known.
//
// The runner prefers SpecFor over Spec. Benchmarks that do not implement
// it are described entirely by Spec.
type SubjectSpecifier interface {
	SpecFor(subj Subject) Spec
}

// SpecFor resolves a benchmark's spec for one subject.
func SpecFor(b Benchmark, subj Subject) Spec {
	if ss, ok := b.(SubjectSpecifier); ok {
		return ss.SpecFor(subj)
	}
	return b.Spec()
}

// Match reports whether a dotted id matches a glob supporting "*"
// segments (e.g. "launch.*").
func Match(pattern, id string) bool {
	if pattern == id {
		return true
	}
	pp, ii := strings.Split(pattern, "."), strings.Split(id, ".")
	if len(pp) != len(ii) {
		// A trailing "*" matches the whole subtree.
		if len(pp) < len(ii) && pp[len(pp)-1] == "*" {
			for i := 0; i < len(pp)-1; i++ {
				if pp[i] != "*" && pp[i] != ii[i] {
					return false
				}
			}
			return true
		}
		return false
	}
	for i := range pp {
		if pp[i] != "*" && pp[i] != ii[i] {
			return false
		}
	}
	return true
}

// MatchAny reports whether id matches any of the globs. Empty globs
// matches everything.
func MatchAny(globs []string, id string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, g := range globs {
		if Match(strings.TrimSpace(g), id) {
			return true
		}
	}
	return false
}
