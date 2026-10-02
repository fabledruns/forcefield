// Package suites holds HPOV's explicit benchmark registry: adding a
// benchmark means adding its constructor to All. No init() side
// effects, no global state.
package suites

import (
	"forcefield/internal/hpov/bench"
)

// All returns every registered benchmark. Launch, TUI, and memory
// suites register here as they land; tiers and kinds filter at run.
func All() []bench.Registered {
	var out []bench.Registered
	for _, b := range LaunchBenchmarks() {
		out = append(out, bench.Registered{Bench: b})
	}
	return out
}
