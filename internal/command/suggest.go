package command

// levenshtein returns the edit distance between a and b: the minimum
// number of single-character insertions, deletions, or substitutions
// needed to turn a into b.
//
// This is only ever run over command names (a handful of characters) and
// only after an unknown command misses the registry lookup, so it favors
// a clear, standard two-row DP implementation over any further
// micro-optimization.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	la, lb := len(ar), len(br)

	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			deletion := prev[j] + 1
			insertion := curr[j-1] + 1
			substitution := prev[j-1] + cost
			curr[j] = min3(deletion, insertion, substitution)
		}
		prev, curr = curr, prev
	}

	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
