package tools

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTruncateString(t *testing.T) {
	cases := []struct {
		name      string
		s         string
		max       int
		want      string
		truncated bool
	}{
		{"empty", "", 10, "", false},
		{"under", "abc", 10, "abc", false},
		{"exact", "abcde", 5, "abcde", false},
		{"over", "abcdef", 5, "abcde", true},
		{"no cap zero", "abcdef", 0, "abcdef", false},
		{"no cap negative", "abcdef", -3, "abcdef", false},
	}
	for _, tc := range cases {
		got, rec := TruncateString(tc.s, tc.max)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
		if rec.Truncated != tc.truncated {
			t.Errorf("%s: Truncated = %v, want %v", tc.name, rec.Truncated, tc.truncated)
		}
		if rec.OriginalBytes != len(tc.s) || rec.KeptBytes != len(got) || rec.Limit != tc.max {
			t.Errorf("%s: record = %+v, want original=%d kept=%d limit=%d",
				tc.name, rec, len(tc.s), len(got), tc.max)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: result is not valid UTF-8", tc.name)
		}
	}
}

func TestTruncateString_RuneBoundary(t *testing.T) {
	// "é" is 2 bytes; a 5-byte cut of "ééé" must keep 2 runes (4 bytes),
	// never a torn half-character.
	got, rec := TruncateString("ééé", 5)
	if got != "éé" {
		t.Errorf("got %q, want %q", got, "éé")
	}
	if !rec.Truncated || rec.KeptBytes != 4 || rec.OriginalBytes != 6 {
		t.Errorf("record = %+v", rec)
	}
	// 3-byte runes: cut lands mid-rune.
	got, _ = TruncateString(strings.Repeat("世", 10), 10)
	if !utf8.ValidString(got) || len(got) > 10 || len(got) < 7 {
		t.Errorf("got %d bytes %q, want a valid rune-boundary prefix", len(got), got)
	}
}

func TestCapLines(t *testing.T) {
	if got, dropped := CapLines("a\nb\nc", 5); got != "a\nb\nc" || dropped {
		t.Errorf("under = %q,%v", got, dropped)
	}
	if got, dropped := CapLines("a\nb\nc", 3); got != "a\nb\nc" || dropped {
		t.Errorf("exact = %q,%v", got, dropped)
	}
	got, dropped := CapLines("a\nb\nc\nd", 2)
	if got != "a\nb" || !dropped {
		t.Errorf("over = %q,%v, want %q,true", got, dropped, "a\nb")
	}
	// Trailing newline preserved on the kept prefix.
	if got, dropped := CapLines("a\nb\nc\n", 2); got != "a\nb\n" || !dropped {
		t.Errorf("trailing = %q,%v", got, dropped)
	}
	if got, dropped := CapLines("a\nb", 0); got != "a\nb" || dropped {
		t.Errorf("no cap = %q,%v", got, dropped)
	}
}

func TestLimitsWithDefaults(t *testing.T) {
	d := Limits{MaxBytes: 100, MaxLines: 10, Timeout: time.Second}
	got := Limits{}.WithDefaults(d)
	if got != d {
		t.Errorf("empty = %+v, want %+v", got, d)
	}
	got = Limits{MaxBytes: 50, Timeout: 2 * time.Second}.WithDefaults(d)
	if got.MaxBytes != 50 || got.MaxLines != 10 || got.Timeout != 2*time.Second {
		t.Errorf("partial = %+v", got)
	}
}

func TestDefaultLimitsFor(t *testing.T) {
	if got := DefaultLimitsFor("shell"); got.MaxBytes != DefaultShellMaxBytes || got.Timeout != DefaultShellTimeout {
		t.Errorf("shell = %+v", got)
	}
	if got := DefaultLimitsFor("read_file"); got.MaxBytes != DefaultReadMaxBytes {
		t.Errorf("read_file = %+v", got)
	}
	if got := DefaultLimitsFor("list_files"); got.MaxLines != DefaultListMaxLines {
		t.Errorf("list_files = %+v", got)
	}
	if got := DefaultLimitsFor("search_files"); got.MaxLines != DefaultSearchMaxLines {
		t.Errorf("search_files = %+v", got)
	}
	if got := DefaultLimitsFor("find_files"); got.MaxLines != DefaultFindMaxResults {
		t.Errorf("find_files = %+v", got)
	}
	if got := DefaultLimitsFor("shell_job"); got.MaxBytes != DefaultJobMaxBytes || got.Timeout != DefaultJobTimeout {
		t.Errorf("shell_job = %+v", got)
	}
	if got := DefaultLimitsFor("secret_scan"); got.MaxLines != DefaultSecretMaxFindings {
		t.Errorf("secret_scan = %+v", got)
	}
	if got := DefaultLimitsFor("mystery"); got.Timeout != DefaultToolTimeout || got.MaxBytes != 0 {
		t.Errorf("unknown = %+v, want generic timeout-only bound", got)
	}
}

func TestClampTimeout(t *testing.T) {
	if got := ClampTimeout(0, time.Second); got != time.Second {
		t.Errorf("zero = %v", got)
	}
	if got := ClampTimeout(-time.Second, time.Second); got != time.Second {
		t.Errorf("negative = %v", got)
	}
	if got := ClampTimeout(time.Hour, time.Second); got != MaxTimeout {
		t.Errorf("oversized = %v, want ceiling %v", got, MaxTimeout)
	}
	if got := ClampTimeout(5*time.Second, time.Second); got != 5*time.Second {
		t.Errorf("normal = %v", got)
	}
}

func TestTruncationFields(t *testing.T) {
	_, rec := TruncateString("abcdef", 3)
	f := rec.Fields()
	if f["truncated"] != true || f["original_bytes"] != 6 || f["kept_bytes"] != 3 || f["limit_bytes"] != 3 {
		t.Errorf("fields = %v", f)
	}
}
