// Line elision for the built-in and ripgrep-backed search tools. Both
// paths report the full text of a matching line, so one outlier line
// (a minified bundle, generated data, or a long prose sentence with the
// symbol buried mid-sentence) can dominate the tool's output. Eliding
// keeps the match and a bounded margin around it.
package search

import (
	"fmt"
	"unicode/utf8"
)

// elideHead and elideTail bracket text dropped from an elided line.
// Both are short so a marked line still fits the byte cap. Every side
// of the window whose bytes were dropped gets its marker: a mid-line
// window is marked on both sides, not just the leading one.
const (
	elideHead = "…"
	elideTail = "…[line elided]"
)

// elidedMatchFmt annotates a match too large to emit whole: not even
// the match's own bytes fit the per-line budget. %d is the full byte
// length of the match, so the reader can size what was skipped and the
// reported start column remains the true match start.
const elidedMatchFmt = "[match elided: %d bytes]"

// elideMargin is the trailing context kept after a match when its line
// is elided, so the code that follows it stays visible. Whatever the
// window has left after the match and this margin is spent on leading
// context. The match itself is preserved whole unless it is bigger than
// the entire budget; elision otherwise only removes material far from
// it.
const elideMargin = 400

// elideLine shortens a matching line to the match plus a bounded margin
// around it. Lines within maxBytes are returned untouched, so ordinary
// matches keep their full context; only outlier lines are shortened.
//
// matchCol is the 1-based byte offset of the match, matching ripgrep's
// column. matchLen is the match's byte length; pass 0 when the caller
// does not know it, and the window starts at the column, spending the
// rest of the budget on leading context. Callers that know both bounds
// get the whole match when it fits the budget.
//
// The envelope callers can rely on: every dropped side is marked, so
// output never claims a line ended where only the window ended. The
// emitted line stays within maxBytes plus a few bytes of rune-boundary
// snapping and marker text; the oversized path spends the rest of the
// budget on the match head and its explicit marker. The match position
// is carried by the caller for search_code as path:line:column; the
// built-in walker (search_files output has no columns) instead relies
// on the emitted text containing the match, which is why the window is
// located around the match rather than around the head.
func elideLine(text string, matchCol, matchLen, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	off := matchCol - 1
	if off < 0 {
		off = 0
	}
	if off > len(text) {
		off = len(text)
	}
	end := off
	if matchLen > 0 && off+matchLen <= len(text) {
		end = off + matchLen
	}
	if matchLen > maxBytes {
		// The match is larger than the whole per-line budget. Keep its
		// head and elide the remainder with an annotated marker, so no
		// single result can dominate the output. This branch is only
		// reached when matchLen > maxBytes, and keep is bounded above
		// by maxBytes minus the markers, so keep is always a strict
		// slice of the match.
		marker := fmt.Sprintf(elidedMatchFmt, matchLen)
		keep := maxBytes - len(elideHead) - len(elideTail) - len(marker)
		if keep <= 0 {
			return marker // degenerate budget: the marker is the report
		}
		cut := snapStart(text, off+keep)
		if cut < off {
			cut = off
		}
		kept := text[off:cut]
		switch {
		case off > 0 && end < len(text):
			return elideHead + kept + marker + elideTail
		case off > 0:
			return elideHead + kept + marker
		case end < len(text):
			return kept + marker + elideTail
		default:
			return kept + marker
		}
	}
	// The window ends just past the match plus the elbow room wanted
	// after it, bounded by both the line and the byte cap.
	hi := snapEnd(text, end+elideMargin)
	if hi-off > maxBytes {
		hi = snapEnd(text, off+maxBytes)
	}
	// ...and it is maxBytes long or less, but never starts past the
	// match: that would clip the match itself out of its own output.
	lo := snapStart(text, hi-maxBytes)
	if lo > off {
		lo = off // never start past the match: that would drop it
	}
	keep := text[lo:hi]
	// Mark every side the window cut. The window CAN cover the whole
	// line: a line only a few bytes over the cap with its match at the
	// end can clamp hi to the line end while a rune boundary (or the
	// match itself) forces lo to 0, leaving nothing dropped — the bare
	// default emits no marker, because an honest "whole line kept"
	// reports no cut. The used-to-be-omitted trailing marker is why
	// this has both explicit side cases.
	switch {
	case lo > 0 && hi < len(text):
		return elideHead + keep + elideTail
	case lo > 0:
		return elideHead + keep
	case hi < len(text):
		return keep + elideTail
	default:
		return keep
	}
}

// snapStart moves n down to the nearest UTF-8 rune boundary within
// [0, len(text)]. Cutting between a rune's continuation bytes would
// emit invalid UTF-8. The n < len(text) guard keeps a cut sitting
// exactly at the line end (it is already a valid boundary) from
// indexing one past the line, which callers passing degenerate
// columns previously could.
func snapStart(text string, n int) int {
	if n < 0 {
		n = 0
	}
	if n > len(text) {
		n = len(text)
	}
	for n > 0 && n < len(text) && !utf8.RuneStart(text[n]) {
		n--
	}
	return n
}

// snapEnd moves n up to the nearest UTF-8 rune boundary within
// [0, len(text)].
func snapEnd(text string, n int) int {
	if n > len(text) {
		n = len(text)
	}
	if n < 0 {
		n = 0
	}
	for n < len(text) && !utf8.RuneStart(text[n]) {
		n++
	}
	return n
}

// elideMatch applies elideLine to a matched line, locating the match
// with locate only when the line is long enough to need eliding. Short
// lines are returned untouched and never rescanned, so the common case
// costs nothing beyond the length check.
func elideMatch(text string, locate func(string) (int, int), maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	col, mlen := locate(text)
	return elideLine(text, col, mlen, maxBytes)
}
