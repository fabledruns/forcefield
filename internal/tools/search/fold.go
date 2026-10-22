// Case-insensitive offsets for the built-in walker. Lowering a line
// with strings.ToLower can change its byte length (KELVIN SIGN lowers
// to a one-byte k, İ to one letter, and U+023A grows from two bytes to
// three), so an offset found in the lowered copy is not necessarily an
// offset into the original bytes. These helpers walk the original
// string rune by rune, so the offset they report is where the match
// really starts and slicing there keeps the match inside the elision
// window.
package search

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// foldedIndex returns the byte offset in line of the first
// rune-boundary occurrence of the already-folded needle, plus the span
// of original line bytes that fold to it. It returns (-1, 0) when the
// fold of line does not start at any rune boundary with needle. The
// span, not len(needle), is what the caller must slice with: shrink
// mappings (KELVIN SIGN lowers 3 bytes to 1) make a folded occurrence
// cover more original bytes than the needle, and growth mappings
// (U+023A) can make it cover fewer. An empty needle is (0, 0); only
// the detector's Contains predicate can produce one, and callers never
// reach elision for it.
//
// The fast path folds line once with the same primitive the detector
// uses (strings.ToLower, which does not copy when nothing changes) and
// locates the folded occurrence with strings.Index. The hit offset f
// is then mapped back to original bytes by walking both strings rune
// by rune in lockstep: strings.ToLower is the per-rune
// unicode.ToLower of every rune, so the k-th rune of lowered always
// starts at a byte offset cursor that corresponds to the k-th rune of
// line (an invalid byte decodes as a one-byte RuneError and is
// re-encoded as a three-byte RuneError in lowered — sizes differ, but
// the correspondence survives). A lowercased needle's first byte is
// never a continuation byte (ToLower output is always valid UTF-8), so
// a strings.Index hit always lands on a rune boundary of lowered and
// cursor can reach it exactly. The result is pinned to the old
// per-rune walk's semantics by a byte-exact hasFoldedPrefix gate;
// anything off leads to foldedIndexProbe, which is the old walk
// kept verbatim as a fallback.
func foldedIndex(line, needle string) (int, int) {
	if needle == "" {
		return 0, 0
	}
	lowered := strings.ToLower(line)
	f := strings.Index(lowered, needle)
	if f < 0 {
		return -1, 0
	}
	want := f + len(needle)
	start, end := -1, -1
	cursor := 0 // folded bytes consumed so far = foldedLen(line[:i])
	for i := 0; i < len(line); {
		if start < 0 && cursor == f {
			start = i
		}
		if cursor == want {
			end = i
			break
		}
		if line[i] < utf8.RuneSelf {
			// ASCII: its fold is one ASCII byte, in both strings.
			i++
			cursor++
		} else {
			// Non-ASCII: advance by the rune-k correspondence in both
			// strings; the sizes may differ per rune (shrink/growth).
			_, sz := utf8.DecodeRuneInString(line[i:])
			if sz == 0 {
				sz = 1
			}
			_, fs := utf8.DecodeRuneInString(lowered[cursor:])
			if fs == 0 {
				fs = 1 // defensive; lowered is valid UTF-8, so this cannot fire
			}
			i += sz
			cursor += fs
		}
	}
	if end < 0 {
		// The occurrence runs to the line end: no i saw cursor==want
		// before the walk ran out. cursor is foldedLen(line) by
		// construction, so only a skipped-over f (a non-boundary hit,
		// which the boundary invariant rules out) could disagree.
		if cursor != want {
			return foldedIndexProbe(line, needle)
		}
		end = len(line)
	}
	if start >= 0 && end > start && hasFoldedPrefix(line[start:], needle) {
		return start, end - start
	}
	return foldedIndexProbe(line, needle)
}

// foldedIndexProbe is the pre-index walker kept verbatim: it advances
// the original string rune by rune and reports the first position
// whose per-rune-lowered runes reproduce needle byte for byte. On a
// hit the span is len(needle), which is byte-identical to the
// behavior callers relied on before the two-value walker; on a miss
// it reports (-1, 0).
func foldedIndexProbe(line, needle string) (int, int) {
	if needle == "" {
		return 0, 0
	}
	for i := 0; i < len(line); {
		if hasFoldedPrefix(line[i:], needle) {
			return i, len(needle)
		}
		_, size := utf8.DecodeRuneInString(line[i:])
		if size == 0 {
			size = 1 // DecodeRune never reports 0; stay safe anyway
		}
		i += size
	}
	return -1, 0
}

// hasFoldedPrefix reports whether lowering the runes at the start of s
// reproduces needle byte for byte. Invalid byte sequences decode as
// RuneError and cannot fold onto ordinary needle bytes, so they simply
// fail the comparison.
func hasFoldedPrefix(s, needle string) bool {
	j := 0
	for j < len(needle) {
		if s == "" {
			return false
		}
		r, size := utf8.DecodeRuneInString(s)
		if size == 0 {
			return false
		}
		lowered := unicode.ToLower(r)
		var buf [utf8.UTFMax]byte
		n := utf8.EncodeRune(buf[:], lowered)
		for k := 0; k < n; k++ {
			if j >= len(needle) || needle[j] != buf[k] {
				return false
			}
			j++
		}
		s = s[size:]
	}
	return true
}
