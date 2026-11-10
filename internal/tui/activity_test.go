package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

func TestFormatToolStartIncludesUsefulArguments(t *testing.T) {
	got := formatToolStart(&providers.ToolCall{
		Name:      "read_file",
		Arguments: map[string]any{"path": "README.md"},
	})
	if got != "● Running read_file README.md" {
		t.Errorf("formatToolStart() = %q", got)
	}
}

func TestFormatToolFinishSummarizesListFiles(t *testing.T) {
	got := formatToolFinish(&runtime.ToolResult{
		Name:     "list_files",
		Content:  "README.md\ncmd/\ninternal/",
		Success:  true,
		Duration: 150 * time.Millisecond,
	}, runtime.EventToolFinish)
	if got != "* Found 3 entries (150ms)" {
		t.Errorf("formatToolFinish() = %q", got)
	}
}

func TestFormatToolFinishSearchUsesStar(t *testing.T) {
	got := formatToolFinish(&runtime.ToolResult{
		Name:     "search_files",
		Content:  "a.go:1: foo\nb.go:2: foo bar",
		Success:  true,
		Duration: 210 * time.Millisecond,
	}, runtime.EventToolFinish)
	if got != "* Found 2 matches (210ms)" {
		t.Errorf("formatToolFinish() = %q", got)
	}
}

func TestFormatToolFinishSearchNoMatchesFallsBack(t *testing.T) {
	got := formatToolFinish(&runtime.ToolResult{
		Name:    "search_files",
		Content: "no matches for \"zzz\" under .",
		Success: true,
	}, runtime.EventToolFinish)
	if got != "◈ search_files completed" {
		t.Errorf("formatToolFinish() = %q", got)
	}
}

func TestFormatToolFinishReadUsesDiamond(t *testing.T) {
	got := formatToolFinish(&runtime.ToolResult{
		Name:      "read_file",
		Arguments: map[string]any{"path": "go.mod"},
		Content:   "module foo",
		Success:   true,
	}, runtime.EventToolFinish)
	if got != "◈ Read go.mod" {
		t.Errorf("formatToolFinish() = %q", got)
	}
}

func TestFormatToolFinishGenericUsesDiamond(t *testing.T) {
	got := formatToolFinish(&runtime.ToolResult{
		Name:    "shell",
		Content: "ok",
		Success: true,
	}, runtime.EventToolFinish)
	if got != "◈ shell completed" {
		t.Errorf("formatToolFinish() = %q", got)
	}
}

func TestFormatToolFinishDoesNotExposeLargeFailureOutput(t *testing.T) {
	got := formatToolFinish(&runtime.ToolResult{
		Name:    "read_file",
		Content: strings.Repeat("x", 200),
		Success: false,
	}, runtime.EventToolFailed)
	if len(got) > 160 || !strings.HasPrefix(got, "✕ read_file failed: ") {
		t.Errorf("formatToolFinish() = %q", got)
	}
}

// --- shortResult truncation ---------------------------------------------------------
//
// shortResult limits a result to one summary line. The limit is counted
// in characters, and the cut is made at a character boundary: the guard
// used to compare a byte length against a character limit while the cut
// sliced a rune slice, so multi-byte text whose byte count exceeded the
// limit but whose character count did not sliced past the end of the rune
// slice and panicked ("slice bounds out of range [:119] with capacity 72").

// shortResultLimit mirrors the limit enforced by shortResult.
const shortResultLimit = 120

func TestShortResultEmpty(t *testing.T) {
	if got := shortResult(""); got != "" {
		t.Errorf("shortResult(\"\") = %q, want empty", got)
	}
}

func TestShortResultShorterThanLimitIsUnchanged(t *testing.T) {
	value := "a short tool result"
	if got := shortResult(value); got != value {
		t.Errorf("shortResult(%q) = %q, want unchanged", value, got)
	}
}

func TestShortResultExactLimitIsUnchanged(t *testing.T) {
	value := strings.Repeat("x", shortResultLimit)
	got := shortResult(value)
	if got != value {
		t.Errorf("shortResult of exactly %d characters = %q, want unchanged", shortResultLimit, got)
	}
	if strings.Contains(got, "…") {
		t.Errorf("shortResult of exactly %d characters was truncated: %q", shortResultLimit, got)
	}
}

func TestShortResultLongerThanLimitTruncates(t *testing.T) {
	value := strings.Repeat("x", 200)
	got := shortResult(value)
	if want := strings.Repeat("x", shortResultLimit-1) + "…"; got != want {
		t.Errorf("shortResult = %q, want %q", got, want)
	}
	if n := utf8.RuneCountInString(got); n != shortResultLimit {
		t.Errorf("shortResult returned %d characters, want %d", n, shortResultLimit)
	}
}

// TestShortResultMultibyteShorterThanLimitIsUnchanged is the regression
// guard for the crash: the reported panic came from a value with 72
// characters but more than 120 bytes.
func TestShortResultMultibyteShorterThanLimitIsUnchanged(t *testing.T) {
	// 72 characters, 152 bytes: the exact shape of the reported panic
	// ("[:119] with capacity 72").
	value := strings.Repeat("世", 40) + strings.Repeat("a", 32)
	if len(value) <= shortResultLimit {
		t.Fatalf("test setup: want more than %d bytes, got %d", shortResultLimit, len(value))
	}
	if n := utf8.RuneCountInString(value); n != 72 {
		t.Fatalf("test setup: want 72 characters, got %d", n)
	}
	got := shortResult(value)
	if got != value {
		t.Errorf("shortResult = %q, want the value unchanged", got)
	}
}

func TestShortResultUnicodeTruncatesOnCharacterBoundary(t *testing.T) {
	value := strings.Repeat("é", 200) // 200 characters, 400 bytes
	got := shortResult(value)
	if want := strings.Repeat("é", shortResultLimit-1) + "…"; got != want {
		t.Errorf("shortResult = %q, want %q", got, want)
	}
	if !utf8.ValidString(got) {
		t.Errorf("shortResult produced invalid UTF-8: %q", got)
	}
}

func TestShortResultNeverSplitsARune(t *testing.T) {
	cases := map[string]string{
		"emoji":     strings.Repeat("🙂", 200),
		"cjk":       strings.Repeat("世界", 200),
		"mixed":     strings.Repeat("a世🙂é", 100),
		"combining": strings.Repeat("e"+string(rune(0x0301)), 200),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			got := shortResult(value)
			if !utf8.ValidString(got) {
				t.Fatalf("shortResult produced invalid UTF-8: %q", got)
			}
			if strings.ContainsRune(got, utf8.RuneError) {
				t.Fatalf("shortResult split a multi-byte rune: %q", got)
			}
			if !strings.HasSuffix(got, "…") {
				t.Fatalf("shortResult missing ellipsis: %q", got)
			}
			if n := utf8.RuneCountInString(got); n != shortResultLimit {
				t.Errorf("shortResult returned %d characters, want %d", n, shortResultLimit)
			}
		})
	}
}

func TestShortResultCollapsesNewlinesToSpaces(t *testing.T) {
	got := shortResult("line one\nline two")
	if got != "line one line two" {
		t.Errorf("shortResult = %q, want %q", got, "line one line two")
	}
}

// TestFormatToolProgressMultibyteDoesNotPanic exercises the path from the
// crash report: streamed tool output reaches the transcript through
// formatToolProgress.
func TestFormatToolProgressMultibyteDoesNotPanic(t *testing.T) {
	data := strings.Repeat("世", 40) + strings.Repeat("a", 32)
	got := formatToolProgress(&runtime.ToolProgress{
		ToolCallID: "t1",
		Name:       "shell",
		Stream:     "stdout",
		Data:       data,
	})
	if !strings.Contains(got, data) {
		t.Errorf("formatToolProgress dropped the streamed output: %q", got)
	}
}

func TestFormatToolProgressTruncatesUnicodeSafely(t *testing.T) {
	got := formatToolProgress(&runtime.ToolProgress{
		ToolCallID: "t1",
		Name:       "shell",
		Stream:     "stdout",
		Data:       strings.Repeat("世", 200),
	})
	if !utf8.ValidString(got) {
		t.Errorf("formatToolProgress produced invalid UTF-8: %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Errorf("formatToolProgress split a multi-byte rune: %q", got)
	}
}

func TestSanitizeTerminalOutput(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"CSI Color", "hello \x1b[31mred\x1b[0m world", "hello red world"},
		{"OSC Title", "result\x1b]0;Title\x07after", "resultafter"},
		{"CSI Clear", "before\x1b[2Jafter", "beforeafter"},
		{"Control Chars", "line1\x00\x07\x1bline2\r\nline3", "line1line2\nline3"},
		{"Preserves Tabs and Newlines", "col1\tcol2\nrow2", "col1\tcol2\nrow2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeTerminalOutput(tc.input)
			if got != tc.want {
				t.Errorf("sanitizeTerminalOutput(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestFormatToolDetailsSanitizesEscapeSequences(t *testing.T) {
	rec := &toolRecord{
		name: "read_file",
		args: map[string]any{
			"path": "path/with/\x1b[31mescape\x1b[0m.txt",
		},
		content: "line 1\x1b]0;Hacked Title\x07\nline 2\x1b[2Jcleared\x00",
		err:     "failed\x1b[1mbold\x1b[0m",
	}
	details := formatToolDetails(rec, 80)
	if strings.Contains(details, "\x1b") {
		t.Errorf("formatToolDetails leaked raw ESC (0x1b): %q", details)
	}
	if strings.Contains(details, "Hacked Title") {
		t.Errorf("formatToolDetails leaked OSC sequence text: %q", details)
	}
	if !strings.Contains(details, "escape.txt") {
		t.Errorf("formatToolDetails lost sanitized path: %q", details)
	}
	if !strings.Contains(details, "line 2cleared") {
		t.Errorf("formatToolDetails lost sanitized content: %q", details)
	}
}
