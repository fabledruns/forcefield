package search

import (
	"math/rand"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// ASCII case folding finds the usual offsets. The needle is already
// folded, exactly as the matchOffset caller lowers it. The span is the
// original bytes consumed by the folded occurrence.
func TestFoldedIndex_ASCII(t *testing.T) {
	if off, span := foldedIndex("alpha Beta C", "beta"); off != 6 || span != 4 {
		t.Fatalf("foldedIndex = (%d, %d), want (6, 4)", off, span)
	}
	if off, span := foldedIndex("ABC", "abc"); off != 0 || span != 3 {
		t.Fatalf("fold-through-fold = (%d, %d), want (0, 3)", off, span)
	}
}

// A fold that shrinks a rune (U+212A KELVIN SIGN -> k, 3 bytes to 1)
// must not move the reported offset: an offset taken from the lowered
// copy points two bytes earlier per preceding Kelvin than the original
// string does. The old Index(ToLower(line)) approach returned 2 here;
// the original offset of the real first match is 6. The span is the
// original bytes: the last Kelvin plus "abc" fold to the 4-byte
// needle while consuming 6 original bytes.
func TestFoldedIndex_ShrinkingFoldKeepsOriginalOffset(t *testing.T) {
	line := "\u212A\u212A\u212Aabc" // three 3-byte Kelvins + "abc"
	if off, span := foldedIndex(line, "kabc"); off != 6 || span != 6 {
		t.Fatalf("foldedIndex = (%d, %d), want (6, 6) (an offset from the folded copy would be 2)", off, span)
	}
	if off, span := foldedIndex("\u212Aabc", "kabc"); off != 0 || span != 6 {
		t.Fatalf("head Kelvin = (%d, %d), want (0, 6)", off, span)
	}
	if off, span := foldedIndex("\u212A\u212A", "k"); off != 0 || span != 3 {
		t.Fatalf("needle k in a Kelvin run = (%d, %d), want (0, 3) for one Kelvin", off, span)
	}
}

// U+0130 (İ) lowers to one letter, again shrinking the folded copy.
// The needle's first rune-boundary occurrence in the original is at
// byte 2, not the folded copy's byte 1, and the span covers İ (2
// bytes) plus "abc".
func TestFoldedIndex_MultibyteFoldPrefix(t *testing.T) {
	line := "\u0130\u0130abc"
	if off, span := foldedIndex(line, "iabc"); off != 2 || span != 5 {
		t.Fatalf("foldedIndex = (%d, %d), want (2, 5)", off, span)
	}
	if off, span := foldedIndex("\u0130abc", "iabc"); off != 0 || span != 5 {
		t.Fatalf("head İ = (%d, %d), want (0, 5)", off, span)
	}
}

// A fold that GROWS a rune (U+023A lowers from two bytes to the
// three-byte U+2C65) is only visible through the span: the folded
// occurrence is one byte longer than the original bytes it consumes.
// Occurrences that run to the line end also pin the end boundary.
func TestFoldedIndex_GrowthAndLineEndSpans(t *testing.T) {
	// Growth alone: needle 3 folded bytes consume 2 original bytes.
	k := strings.ToLower("\u023A") // Ⱥ -> ⱥ, 2 bytes -> 3
	if off, span := foldedIndex("\u023A", k); off != 0 || span != 2 {
		t.Fatalf("growth = (%d, %d), want (0, 2)", off, span)
	}
	// Growth + shrink in one occurrence: ȺKabc (8 original bytes)
	// folds to ⱥkabc (7 folded bytes) and the occurrence covers the
	// whole line.
	if off, span := foldedIndex("\u023A\u212Aabc", "\u2C65kabc"); off != 0 || span != 8 {
		t.Fatalf("growth+shrink = (%d, %d), want (0, 8)", off, span)
	}
	// Occurrences running to the line end, shrink mappings.
	if off, span := foldedIndex("xyz\u212A", "k"); off != 3 || span != 3 {
		t.Fatalf("line-end Kelvin = (%d, %d), want (3, 3)", off, span)
	}
	if off, span := foldedIndex("ab\u212A\u212A", "kk"); off != 2 || span != 6 {
		t.Fatalf("line-end KK = (%d, %d), want (2, 6)", off, span)
	}
}

// Invalid byte sequences decode as RuneError, which folds to the
// three-byte RuneError encoding; a needle of exactly that encoding
// matches one invalid byte and the span shows it (1 original byte -> 3
// folded bytes). Two invalid bytes then two ordinary runes pin both
// behaviors in one walk.
func TestFoldedIndex_InvalidBytesAndMisses(t *testing.T) {
	if off, span := foldedIndex("\xffabc", "abc"); off != 1 || span != 3 {
		t.Fatalf("invalid-byte walk = (%d, %d), want (1, 3)", off, span)
	}
	if off, span := foldedIndex("\xff\xfe", "\uFFFD"); off != 0 || span != 1 {
		t.Fatalf("RuneError needle = (%d, %d), want (0, 1) (one invalid byte folds to 3 bytes)", off, span)
	}
	if off, span := foldedIndex("\xff\xfe\u212Aab", "kab"); off != 2 || span != 5 {
		t.Fatalf("two invalid bytes then Kelvin = (%d, %d), want (2, 5)", off, span)
	}
	if off, span := foldedIndex("nothing", "zzz"); off != -1 || span != 0 {
		t.Fatalf("miss = (%d, %d), want (-1, 0)", off, span)
	}
	// No early-out for a longer needle: growth folds make a two-byte
	// line match a longer needle, so length is never a cheap miss.
	if off, span := foldedIndex("ab", "abc"); off != -1 || span != 0 {
		t.Fatalf("needle longer than line = (%d, %d), want (-1, 0)", off, span)
	}
	if off, span := foldedIndex("anything", ""); off != 0 || span != 0 {
		t.Fatalf("empty needle = (%d, %d), want (0, 0)", off, span)
	}
}

// The two strings foldedIndex depends on must agree byte for byte:
// strings.ToLower(string(r)) is the encoding of unicode.ToLower(r) for
// every rune. The detector folds line with the former and the walker's
// gate compares with the latter; any disagreement would desynchronize
// cursor and i.
func TestFoldedIndex_TableLowerIsPerRuneFold(t *testing.T) {
	var buf [utf8.UTFMax]byte
	for r := rune(0); r <= 0x10FFFF; r++ {
		n := utf8.EncodeRune(buf[:], unicode.ToLower(r))
		want := string(buf[:n])
		if got := strings.ToLower(string(r)); got != want {
			t.Fatalf("rune %U: ToLower = %q, want per-rune fold %q", r, got, want)
		}
	}
}

// The boundary invariant: a lowercased needle's first byte is never a
// continuation byte, so a strings.Index hit in lowered always lands on
// a rune boundary there and the lockstep walk can map it back. Exercised
// over the full rune table.
func TestFoldedIndex_NeedleFirstByteNeverContinuation(t *testing.T) {
	for r := rune(0); r <= 0x10FFFF; r++ {
		needle := strings.ToLower(string(r))
		if len(needle) == 0 {
			continue
		}
		if !utf8.RuneStart(needle[0]) {
			t.Fatalf("rune %U: lowercasing to %q yields a continuation first byte", r, needle)
		}
	}
}

// FuzzOracle folds random mixed-alphabet fragments and checks the
// walker's answer against a direct oracle: the offset is reachable by
// decoding from 0, the span is positive, the occurrence actually folds
// onto the needle, and the slice's own ToLower is the needle exactly.
func TestFoldedIndex_FuzzOracle(t *testing.T) {
	if testing.Short() {
		t.Skip("fuzz oracle in -short")
	}
	tokens := []string{
		"a", "A", "K", "k", "B", "x", "xy", "xyz", "\u212A", "\u0130",
		"\u023A", "\u023E", "\u00df", "\u00c9", "\u212B", "\u0130",
		"\xff", "\xc3", "\x80", "\uFFFD", " ",
	}
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 20000; iter++ {
		nTokens := 1 + rng.Intn(8)
		var line strings.Builder
		for i := 0; i < nTokens; i++ {
			line.WriteString(tokens[rng.Intn(len(tokens))])
		}
		full := line.String()
		// A fragment of at least one rune, taken at a random rune
		// boundary; its fold is the needle the walker must find.
		startByte := 0
		startRunes := rng.Intn(nTokens) // count of runes to skip first
		for r := 0; r < startRunes; r++ {
			_, sz := utf8.DecodeRuneInString(full[startByte:])
			startByte += sz
		}
		endRunes := 1 + rng.Intn(4)
		endByte := startByte
		for r := 0; r < endRunes && endByte < len(full); r++ {
			_, sz := utf8.DecodeRuneInString(full[endByte:])
			endByte += sz
		}
		frag := full[startByte:endByte]
		needle := strings.ToLower(frag)
		if needle == "" {
			continue
		}
		off, span := foldedIndex(full, needle)

		// Oracle 1: the offset must be reachable by decoding from 0.
		walk := 0
		reachable := false
		for walk < len(full) {
			if walk == off {
				reachable = true
				break
			}
			_, sz := utf8.DecodeRuneInString(full[walk:])
			if sz == 0 {
				sz = 1
			}
			walk += sz
		}
		if off == len(full) {
			reachable = true
		}
		if off >= 0 && !reachable {
			t.Fatalf("iter %d: offset %d is not a rune boundary (line %q, needle %q)", iter, off, full, needle)
		}
		if off < 0 {
			// A miss is only acceptable if the needle's fold is absent
			// from the whole line, which ToLower vs ToLower settles.
			if strings.Contains(strings.ToLower(full), needle) {
				t.Fatalf("iter %d: miss but the needle is folded in the line (line %q, needle %q)", iter, full, needle)
			}
			continue
		}

		// Oracle 2..4: positive span, the occurrence starts by folding
		// to the needle, and the whole slice folds to it exactly.
		if span <= 0 {
			t.Fatalf("iter %d: span %d <= 0 (line %q, needle %q)", iter, span, full, needle)
		}
		if !hasFoldedPrefix(full[off:], needle) {
			t.Fatalf("iter %d: occurrence at %d does not start by folding to the needle (line %q, needle %q)", iter, off, full, needle)
		}
		if off+span > len(full) {
			t.Fatalf("iter %d: span %d overruns the line (line %q, needle %q)", iter, span, full, needle)
		}
		if got := strings.ToLower(full[off : off+span]); got != needle {
			t.Fatalf("iter %d: slice fold = %q, want needle %q (line %q, off %d, span %d)", iter, got, needle, full, off, span)
		}
	}
}

// Small-scaled near-miss offsets: an occurrence one byte away from a
// real one must never turn into a match, and a real one at these
// offsets must keep its exact offset and span, for needle lengths
// straddling the multi-byte boundary.
func TestFoldedIndex_AdversarialNearMissUnit(t *testing.T) {
	for _, prefix := range []int{0, 1, 5, 37} {
		for _, k := range []int{1, 40} {
			hit := strings.Repeat("a", k) + "b"
			prefixBytes := strings.Repeat("z", prefix)
			// Real occurrence at the prefix, with a tail.
			if off, span := foldedIndex(prefixBytes+hit+"tail", hit); off != prefix || span != k+1 {
				t.Fatalf("hit at %d (k=%d) = (%d, %d), want (%d, %d)", prefix, k, off, span, prefix, k+1)
			}
			// One byte off: the 'b' is not there, fold must miss.
			if off, span := foldedIndex(prefixBytes+strings.Repeat("a", k)+"c", hit); off != -1 || span != 0 {
				t.Fatalf("near miss at %d (k=%d) = (%d, %d), want (-1, 0)", prefix, k, off, span)
			}
			// One byte off: the 'b' is there but behind a separator, so
			// the near-miss candidate must be skipped and the walk must
			// land on the real occurrence after it.
			if off, span := foldedIndex(prefixBytes+strings.Repeat("a", k)+"?"+hit, hit); off != prefix+k+1 || span != k+1 {
				t.Fatalf("occurrence after near miss at %d (k=%d) = (%d, %d), want (%d, %d)", prefix, k, off, span, prefix+k+1, k+1)
			}
		}
	}
}
