package compare

import "sort"

// holmAdjust applies the Holm–Bonferroni step-down correction across
// the family of tests in one comparison.
//
// A single comparison makes dozens of Mann-Whitney tests. At a 1%
// per-test level, ~60 tests give roughly a 45% chance of at least one
// spurious finding (plan §17.3), so unadjusted results cannot be read
// as a family. Holm controls the family-wise error rate without
// assuming independence, which these correlated metrics do not have.
//
// Input is each test's raw p-value plus the index of the metric it
// belongs to. Output is, per test: whether it survives correction, its
// rank in the ordered family, and the adjusted alpha it was judged
// against. Monotone p-values are enforced, as Holm requires.
func holmAdjust(tests []testResult, alpha float64) {
	m := len(tests)
	if m == 0 {
		return
	}
	order := make([]int, m)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return tests[order[a]].rawP < tests[order[b]].rawP
	})

	// Holm: the i-th smallest p is judged at alpha/(m-i), and the
	// procedure stops at the first failure — every larger p in the family
	// is rejected too. That stopping rule is what makes the correction
	// conservative: a family of forty marginal p-values all fail
	// together instead of the largest one passing on the full alpha.
	prev := 0.0
	stopped := false
	for rank, idx := range order {
		stepped := alpha / float64(m-rank)
		// The adjusted value starts at the raw p and is then made
		// monotone non-decreasing in rank: an adjusted p may not be
		// smaller than the one before it, or a later, less
		// significant test could appear stronger.
		adj := tests[idx].rawP
		if adj < prev {
			adj = prev
		}
		prev = adj
		tests[idx].adjP = adj
		tests[idx].rank = rank + 1
		tests[idx].adjustedAlpha = stepped
		tests[idx].survives = !stopped && tests[idx].rawP <= stepped
		if !tests[idx].survives {
			stopped = true
		}
	}
}

// testResult is one test's input and correction outcome.
type testResult struct {
	metricIndex   int
	rawP          float64
	adjP          float64
	rank          int
	adjustedAlpha float64
	survives      bool
}

// holmOnSlice is holmAdjust over a subset of results, used to correct
// only the family that actually ran a test.
func holmOnSlice(all []testResult, idxs []int, alpha float64) {
	family := make([]testResult, 0, len(idxs))
	for _, i := range idxs {
		family = append(family, all[i])
	}
	holmAdjust(family, alpha)
	for k, i := range idxs {
		all[i] = family[k]
	}
}
