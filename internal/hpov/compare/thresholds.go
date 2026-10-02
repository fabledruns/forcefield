package compare

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Threshold is the practical-change rule for one benchmark family
// (plan §10.3). A change counts only when it reaches the larger of a
// relative and an absolute floor, so a metric with a tiny baseline
// cannot pass on a large percentage.
type Threshold struct {
	// Glob matches benchmark ids ("launch.*", "mem.tui.go-heap").
	Glob string `json:"glob"`
	// RelPct is the relative threshold in percent.
	RelPct float64 `json:"rel_pct"`
	// AbsBytes is the absolute floor in the metric's own unit.
	AbsBytes float64 `json:"abs_floor"`
	// Note explains the rule, and records where an I/O-bound family
	// deviates from the general case.
	Note string `json:"note,omitempty"`
}

// ThresholdTable is a versioned set of thresholds. It is data, not
// code, so a calibrated set can replace the built-in defaults without
// changing the engine (plan §10.3: thresholds.json per host class).
type ThresholdTable struct {
	Version    string      `json:"version"`
	Source     string      `json:"source"`
	HostClass  string      `json:"host_class,omitempty"`
	Thresholds []Threshold `json:"thresholds"`
}

// BuiltinThresholds is the plan's initial table (plan §10.3). These
// are starting values pending A/A calibration; nothing here invents a
// threshold the plan did not state.
func BuiltinThresholds() *ThresholdTable {
	return &ThresholdTable{
		Version: "plan-10.3-initial",
		Source:  "HPOV-Benchmark-Design-Plan.md §10.3",
		Thresholds: []Threshold{
			{Glob: "hot.session.*", RelPct: 15, AbsBytes: 0,
				Note: "I/O-bound: filesystem variance dominates, so the relative bar is raised."},
			{Glob: "hot.fs.*", RelPct: 15, AbsBytes: 0,
				Note: "I/O-bound: filesystem variance dominates, so the relative bar is raised."},
			{Glob: "hot.*", RelPct: 5, AbsBytes: 0,
				Note: "No absolute floor: use the CI."},
			{Glob: "*.micro", RelPct: 5, AbsBytes: 0,
				Note: "ns/op micro-benchmarks: no absolute floor, use the CI."},
			{Glob: "mem.tui.go-heap", RelPct: 3, AbsBytes: 256 * 1024},
			{Glob: "mem.*", RelPct: 5, AbsBytes: 1024 * 1024},
			{Glob: "mcp.*", RelPct: 15, AbsBytes: 10},
			{Glob: "tui.*", RelPct: 15, AbsBytes: 8},
			{Glob: "launch.artifact-size", RelPct: 2, AbsBytes: 64 * 1024},
			{Glob: "launch.*", RelPct: 10, AbsBytes: 5},
		},
	}
}

// For resolves the threshold for a benchmark id. The most specific
// glob wins, so mem.tui.go-heap (3%) is not swallowed by mem.* (5%).
func (t *ThresholdTable) For(benchmarkID string) (Threshold, bool) {
	var best Threshold
	bestLen := -1
	found := false
	for _, th := range t.Thresholds {
		if !matchGlob(th.Glob, benchmarkID) {
			continue
		}
		// Specificity: a literal beats a glob, a longer literal beats a
		// shorter one. Deterministic and independent of file order.
		if n := specificity(th.Glob); n > bestLen {
			best, bestLen, found = th, n, true
		}
	}
	return best, found
}

// specificity ranks a glob: literals outrank wildcards, then length.
func specificity(glob string) int {
	lit := len(glob)
	for i := 0; i < len(glob); i++ {
		if glob[i] == '*' {
			lit--
		}
	}
	// Wildcards are cheap; literals are expensive.
	return lit*100 + (len(glob)-lit)*10 + len(glob)
}

// matchGlob matches a dotted glob where "*" spans one dotted segment
// ("mem.*" matches "mem.tui.go-heap" but not "mem" or "a.mem").
func matchGlob(pattern, s string) bool {
	pp := strings.Split(pattern, ".")
	ss := strings.Split(s, ".")
	if len(pp) > len(ss) {
		return false
	}
	// The pattern may be a prefix of the id; the remainder of the id
	// identifies sub-metrics, not a different benchmark.
	for i, p := range pp {
		if p == "*" {
			continue
		}
		if i >= len(ss) || p != ss[i] {
			return false
		}
	}
	return true
}

// Effective returns the threshold in the metric's own unit: the larger
// of the relative and absolute rules. A zero floor means "relative
// only" (the plan's micro/I-O families).
func (th Threshold) Effective(baseP50 float64) float64 {
	rel := th.RelPct / 100 * baseP50
	if th.AbsBytes <= 0 {
		return rel
	}
	if rel > th.AbsBytes {
		return rel
	}
	return th.AbsBytes
}

// LoadThresholdFile reads a thresholds.json. An absent path returns the
// built-in table; a malformed one is an error, because silently falling
// back would compare against thresholds the caller did not ask for.
func LoadThresholdFile(path string) (*ThresholdTable, error) {
	if path == "" {
		return BuiltinThresholds(), nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return BuiltinThresholds(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read thresholds: %w", err)
	}
	var t ThresholdTable
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("parse thresholds %s: %w", path, err)
	}
	if len(t.Thresholds) == 0 {
		return nil, fmt.Errorf("thresholds %s: no threshold entries", path)
	}
	if t.Source == "" {
		return nil, fmt.Errorf("thresholds %s: source is required so a table can be traced", path)
	}
	for i, th := range t.Thresholds {
		if th.Glob == "" {
			return nil, fmt.Errorf("thresholds %s: entry %d has no glob", path, i)
		}
		if th.RelPct <= 0 {
			return nil, fmt.Errorf("thresholds %s: entry %s has rel_pct=%v, want > 0",
				path, th.Glob, th.RelPct)
		}
	}
	return &t, nil
}

// SaveThresholdFile writes a threshold table, for the calibration
// tooling that produces one.
func SaveThresholdFile(t *ThresholdTable, path string) error {
	raw, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
