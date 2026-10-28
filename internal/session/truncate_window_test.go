package session

// Regression tests for the TruncateModelToolResult window redesign
// (head + center-elided marker + tail, total always <= MaxModelToolResultChars
// instead of today's head-only cut that overflows the window by ~130 bytes
// and silently drops the tail).
//
// These tests are authored against the PRE-FIX baseline and only reference
// exported baseline symbols (TruncateModelToolResult, MaxModelToolResultChars).

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// adviceSuffix is the trailing advice marker both the baseline and the
// redesigned window keep: "[...output truncated at %d bytes, T bytes total.
// Re-run with narrower output (e.g. filters, -run, grep) if you need the rest.]".
// The first number is the window budget (6000), the second the total input
// size.
func adviceSuffix(total int) string {
	return fmt.Sprintf("[...output truncated at 6000 bytes, %d bytes total. Re-run with narrower output (e.g. filters, -run, grep) if you need the rest.]", total)
}

var centerElidedRe = regexp.MustCompile(`\[...center elided: (\d+) bytes...\]`)

// centerElidedBytes extracts N from a "[...center elided: N bytes...]"
// marker, reporting whether the marker is present.
func centerElidedBytes(s string) (int, bool) {
	m := centerElidedRe.FindStringSubmatch(s)
	if len(m) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// TestTruncateModelToolResultShortContentReturnedUnchanged pins that content
// within the 6000-byte window is returned byte-for-byte unchanged, including
// exactly-at-limit and multibyte-at-limit shapes. Parity pin: valid before
// and after the redesign.
func TestTruncateModelToolResultShortContentReturnedUnchanged(t *testing.T) {
	shorts := []string{
		"short",
		"line one\nline two\n\n\ttabs kept\r\n",
		strings.Repeat("x", 5999),
		strings.Repeat("x", 6000),
		// Exactly 6000 bytes of 2-byte runes.
		strings.Repeat("é", 3000),
		strings.Repeat("世界呀", 400), // 1200 bytes, pure multibyte
	}
	for i, in := range shorts {
		if len(in) > MaxModelToolResultChars {
			t.Fatalf("fixture %d built wrong: %d bytes", i, len(in))
		}
		got := TruncateModelToolResult(in)
		if got != in {
			t.Fatalf("content within the window must be returned unchanged (fixture %d, %d bytes): got %d bytes, head %q", i, len(in), len(got), firstChars(got, 40))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("fixture %d: unchanged content must stay valid UTF-8", i)
		}
	}
	if got := TruncateModelToolResult(""); got != "" {
		t.Fatalf("empty content must be returned unchanged")
	}
}

// TestTruncateModelToolResultOutputWithinBudget pins the core bug fix: the
// FINAL output of an oversized window is never longer than
// MaxModelToolResultChars. At the baseline the head-only cut appends a ~130
// byte marker on top of the full 6000-byte window, so the output is ~6133
// bytes: longer than the bound the model-visible window is supposed to have.
func TestTruncateModelToolResultOutputWithinBudget(t *testing.T) {
	sizes := []int{6001, 6201, 9000, 12000, 60_000, 600_001}
	for _, n := range sizes {
		t.Run(fmt.Sprintf("ASCII_%d", n), func(t *testing.T) {
			in := strings.Repeat("A", n)
			got := TruncateModelToolResult(in)
			if len(got) > MaxModelToolResultChars {
				t.Fatalf("oversized window output = %d bytes, want <= MaxModelToolResultChars (%d); the marker must live inside the budget, not after it", len(got), MaxModelToolResultChars)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("oversized window output is not valid UTF-8 (size %d)", n)
			}
			if !strings.Contains(got, "[...output truncated at 6000 bytes, ") {
				t.Fatalf("window must keep the explicit truncation advice, got:\n%s…", firstChars(got, 80))
			}
		})
	}
}

// TestTruncateModelToolResultKeepsTailWindow pins tail retention: the last
// bytes of an oversized tool result must survive truncation (changed suffix
// window) and sit directly under the advice marker. Baseline: head-only cut,
// tail is gone.
func TestTruncateModelToolResultKeepsTailWindow(t *testing.T) {
	sizes := []int{6001, 9000, 60_000}
	for _, n := range sizes {
		t.Run(fmt.Sprintf("ASCII_%d", n), func(t *testing.T) {
			const sentinel = "TAILWINDOW_SENTINEL_9F2AOK" // 26 ASCII bytes
			in := strings.Repeat("A", n-len(sentinel)) + sentinel
			got := TruncateModelToolResult(in)
			want := sentinel + "\n\n" + adviceSuffix(n)
			if !strings.HasSuffix(got, want) {
				t.Fatalf("tail window lost or misplaced (%d bytes in): got tail …%q, want suffix %q", n, firstChars(got, 0)+lastChars(got, 120), want)
			}
			if len(got) > MaxModelToolResultChars {
				t.Fatalf("tail-keeping output = %d bytes, want <= %d", len(got), MaxModelToolResultChars)
			}
		})
	}
}

// TestTruncateModelToolResultKeepsHeadAndCenterElided pins that the head
// window stays the unchanged prefix of the content and that the elided
// middle is summarized by the explicit center marker with the exact elided
// byte count. Baseline: no center marker at all.
func TestTruncateModelToolResultKeepsHeadAndCenterElided(t *testing.T) {
	const headSentinel = "HEADWINDOW_UNIQUE_MARK" // 22 ASCII bytes
	t.Run("head_prefix_and_marker_present", func(t *testing.T) {
		in := headSentinel + strings.Repeat("B", 9228) // 9250 bytes total
		got := TruncateModelToolResult(in)
		if !strings.HasPrefix(got, headSentinel) {
			t.Fatalf("head window must stay the unchanged prefix of the content, got:\n%s…", firstChars(got, 60))
		}
		// The center marker joins the head with "\n\n" exactly as the
		// tail joins the advice marker.
		if !strings.Contains(got, "\n\n[...center elided: ") {
			t.Fatalf("missing center-elided summary line, got:\n%s", describeWindow(got))
		}
		n, ok := centerElidedBytes(got)
		if !ok {
			t.Fatalf("center marker unparsable, got:\n%s", describeWindow(got))
		}
		if n < 181 {
			t.Fatalf("center marker reports N=%d, want >= 181 for any input over the budget", n)
		}
		if n >= len(in) {
			t.Fatalf("center marker reports N=%d for %d bytes of input, want N < total", n, len(in))
		}
	})
	t.Run("smallest_oversize_elides_at_least_181", func(t *testing.T) {
		in := strings.Repeat("c", 6001)
		got := TruncateModelToolResult(in)
		n, ok := centerElidedBytes(got)
		if !ok {
			t.Fatalf("6001-byte input must produce a center-elided marker, got:\n%s", describeWindow(got))
		}
		if n < 181 {
			t.Fatalf("N = %d, want >= 181 (head+tail cannot eat the whole budget)", n)
		}
	})
}

// TestTruncateModelToolResultWindowComposition pins the composed shape:
// head, one occurrence of each marker, joined by "\n\n", with the advice
// marker last. Baseline: the center marker never appears.
func TestTruncateModelToolResultWindowComposition(t *testing.T) {
	const total = 9000
	in := strings.Repeat("D", total-3) + "xyz" // ASCII tail marker too
	got := TruncateModelToolResult(in)
	if cnt := strings.Count(got, "[...output truncated at 6000 bytes, "); cnt != 1 {
		t.Fatalf("advice marker appears %d times, want exactly once", cnt)
	}
	if cnt := strings.Count(got, "[...center elided: "); cnt != 1 {
		t.Fatalf("center marker appears %d times, want exactly once", cnt)
	}
	if !strings.HasSuffix(got, adviceSuffix(total)) {
		t.Fatalf("advice marker must close the window, got tail %q", lastChars(got, 200))
	}
}

// TestTruncateModelToolResultMultibyteWindowEdgesStayValid cuts a sequence of
// multibyte-heavy inputs so a naive (non-rune-safe) window would split a
// multi-byte rune at either window edge. Varying a small ASCII offset 0..7
// shifts the byte alignment of both windows. The output must stay valid UTF-8
// and keep the duty of both windows: unchanged prefix head, unchanged
// suffix window, advice marker last. Baseline: head edge is already rune-safe,
// but the tail window does not exist, so the tail assertions are RED.
func TestTruncateModelToolResultMultibyteWindowEdgesStayValid(t *testing.T) {
	for _, off := range []int{0, 1, 2, 3, 4, 5, 6, 7} {
		t.Run(fmt.Sprintf("offset_%d", off), func(t *testing.T) {
			const tailSentinel = "TAIL_OK_KEY" // 11 ASCII bytes
			// Layout: ASCII filler (offset) -> 2-byte rune run -> 3-byte
			// rune run -> 4-byte rune run -> ASCII tail sentinel -> 'Z'.
			// The head budget (~5220) opens inside the 3-byte rune run
			// for every offset, but a mid-rune straddle is only
			// guaranteed at offsets {0, 2, 3, 5, 6}: at {1, 4, 7} the
			// head edge lands exactly on a rune boundary (a rune-naive
			// byte cut would coincide). The tail budget (~600) opens
			// inside the 4-byte rune run. Rune safety at the edges is
			// cutRuneBoundary's guarantee either way; the sentinel sits
			// inside the suffix window the tail must preserve.
			in := strings.Repeat("a", off) +
				strings.Repeat("é", 700) + // 1400 bytes
				strings.Repeat("世", 1600) + // 4800 bytes
				strings.Repeat("🙂", 900) + // 3600 bytes
				tailSentinel +
				"Z"
			got := TruncateModelToolResult(in)

			if !utf8.ValidString(got) {
				t.Fatalf("window output is not valid UTF-8: a window edge split a rune")
			}
			if !strings.HasPrefix(got, in[:8]) {
				t.Fatalf("head window must start with the content prefix (%d bytes shown), got:\n%s…", minOff(8), firstChars(got, 40))
			}
			if !strings.HasSuffix(got, adviceSuffix(len(in))) {
				t.Fatalf("advice marker must close the window (multibyte tail case), got tail %q", lastChars(got, 160))
			}
			// The tail sentinel must appear no earlier than the head
			// window could reach — i.e. the tail window is a real
			// suffix-side window. Baseline: gone entirely.
			idx := strings.Index(got, tailSentinel)
			if idx < 4600 {
				t.Fatalf("tail sentinel lost from the model-visible output: index %d in %d bytes (baseline drops the tail window)", idx, len(got))
			}
			if len(got) > MaxModelToolResultChars {
				t.Fatalf("multibyte window output = %d bytes, want <= %d", len(got), MaxModelToolResultChars)
			}
		})
	}
}

// firstChars returns the first n runes as a string for diagnostics.
func firstChars(s string, n int) string {
	if n <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) > n {
		rs = rs[:n]
	}
	return string(rs)
}

// lastChars returns the last n bytes as a string for diagnostics.
func lastChars(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func minOff(n int) int { return n }

// describeWindow renders a compact shape summary for diagnostics.
func describeWindow(g string) string {
	return fmt.Sprintf("len=%d prefix=%q tail=%q", len(g), firstChars(g, 40), lastChars(g, 120))
}
