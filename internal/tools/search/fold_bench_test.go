package search

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// benchFoldedIndexProbeOld is today's pre-index walk, copied verbatim
// with its single-value signature, so the paired benchmarks compare
// the reference implementation the walker replaced. It advances the
// original string rune by rune and runs hasFoldedPrefix at every
// candidate position.
func benchFoldedIndexProbeOld(line, needle string) int {
	if needle == "" {
		return 0
	}
	for i := 0; i < len(line); {
		if hasFoldedPrefix(line[i:], needle) {
			return i
		}
		_, size := utf8.DecodeRuneInString(line[i:])
		if size == 0 {
			size = 1 // DecodeRune never reports 0; stay safe anyway
		}
		i += size
	}
	return -1
}

// The bench plan: near-misses amplify the probe's O(n*m) worst case
// where the new path is O(n+m); the remaining cases are ordinary
// finds and misses, to show both paths' steady state. Each paired
// benchmark checks its answer every iteration, so a bench can never
// pass with a wrong answer baked in.

func benchNearMiss599() (string, string, int) {
	needle := strings.Repeat("a", 599) + "b"
	line := strings.Repeat("a", 500000) + needle
	return line, needle, 500000
}

// benchNearMiss4096Cap: the admissibly sized file (the tool cage caps
// searched files at 512 KiB) with the near-miss needle appended, so
// both paths must find the only real occurrence after a full run of
// near-candidates.
func benchNearMiss4096Cap() (string, string, int) {
	needle := strings.Repeat("a", 4095) + "b"
	const cap512KiB = 512 << 10
	line := strings.Repeat("a", cap512KiB) + needle
	return line, needle, cap512KiB
}

func benchTypical4K() (string, string, int) {
	line := strings.Repeat("x", 1000) + "Target" + strings.Repeat("x", 4090-1000-6)
	return line, "target", 1000
}

func benchMiss500K() (string, string, int) {
	return strings.Repeat("a", 500000), "missing", -1
}

func benchKelvinInstant() (string, string, int) {
	line := strings.Repeat("\u212A", 500000) // 1.5 MB of KELVIN SIGN
	return line, "k", 0
}

func benchMultibyteLowerE() (string, string, int) {
	line := strings.Repeat("\u00e9", 500000) + "needle" // ~1 MB, needle late
	return line, "needle", 1000000
}

// benchFoldedPaired runs one benchmark body old- and new-style.
func benchFoldedPaired(b *testing.B, mk func() (string, string, int)) {
	line, needle, wantOff := mk()
	b.Run("old_probe", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got := benchFoldedIndexProbeOld(line, needle); got != wantOff {
				b.Fatalf("probe old = %d, want %d", got, wantOff)
			}
		}
	})
	b.Run("new_foldedIndex", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if off, span := foldedIndex(line, needle); off != wantOff {
				b.Fatalf("walker = (%d, %d), want off %d", off, span, wantOff)
			}
		}
	})
}

func BenchmarkFoldedIndex_NearMiss599(b *testing.B)     { benchFoldedPaired(b, benchNearMiss599) }
func BenchmarkFoldedIndex_NearMiss4096Cap(b *testing.B) { benchFoldedPaired(b, benchNearMiss4096Cap) }
func BenchmarkFoldedIndex_Typical4K(b *testing.B)       { benchFoldedPaired(b, benchTypical4K) }
func BenchmarkFoldedIndex_Miss500K(b *testing.B)        { benchFoldedPaired(b, benchMiss500K) }
func BenchmarkFoldedIndex_KelvinInstant(b *testing.B)   { benchFoldedPaired(b, benchKelvinInstant) }
func BenchmarkFoldedIndex_MultibyteLowerE(b *testing.B) { benchFoldedPaired(b, benchMultibyteLowerE) }
