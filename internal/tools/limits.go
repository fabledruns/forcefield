package tools

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Canonical output and execution bounds. These are the single source of
// truth for every built-in tool's defaults: tool packages reference
// these (never local duplicates) and DefaultLimitsFor assembles them,
// so auditing "what is capped where" means reading this file plus one
// small table.
const (
	// DefaultShellMaxBytes caps combined shell stdout+stderr capture.
	DefaultShellMaxBytes = 2 << 20 // 2 MiB
	// DefaultShellTimeout bounds a shell command without an explicit
	// timeout_seconds argument.
	DefaultShellTimeout = 30 * time.Second
	// MaxTimeout is the hard execution ceiling no tool may exceed,
	// enforced by the scheduler on top of every per-tool timeout.
	MaxTimeout = 300 * time.Second
	// DefaultReadMaxBytes bounds a single read_file result; larger
	// files are refused with a note, not read partially.
	DefaultReadMaxBytes = 5 << 20 // 5 MiB
	// DefaultWriteMaxBytes bounds a single write_file content payload;
	// larger writes are refused with a note instead of filling disk.
	DefaultWriteMaxBytes = 5 << 20 // 5 MiB
	// DefaultListMaxLines bounds list_files entries reported.
	DefaultListMaxLines = 500
	// DefaultSearchMaxLines bounds search_files matches reported.
	DefaultSearchMaxLines = 100
	// DefaultFindMaxResults bounds find_files paths reported.
	DefaultFindMaxResults = 50
	// DefaultGitMaxBytes bounds git tool output (diffs can be large).
	DefaultGitMaxBytes = 256 << 10 // 256 KiB
	// DefaultJobMaxBytes bounds one background job's captured output.
	DefaultJobMaxBytes = 1 << 20 // 1 MiB
	// DefaultJobTimeout bounds one background job's lifetime.
	DefaultJobTimeout = 300 * time.Second
	// DefaultSecretMaxFindings bounds secret_scan findings reported.
	DefaultSecretMaxFindings = 50
	// DefaultToolTimeout bounds any tool execution without its own
	// advertised timeout.
	DefaultToolTimeout = 30 * time.Second
)

// Limits bounds one tool's output and execution. Zero values mean "use
// the tool's default" (see DefaultLimitsFor); they are never silent
// unlimited, except MaxLines which means "no line cap" only for tools
// that document it.
type Limits struct {
	// MaxBytes caps combined output bytes (stdout+stderr+content).
	// Values <= 0 resolve to the tool default.
	MaxBytes int
	// MaxLines caps reported output lines or findings. Values <= 0
	// resolve to the tool default.
	MaxLines int
	// Timeout caps execution time. Values <= 0 resolve to the tool
	// default; the scheduler additionally clamps everything to
	// MaxTimeout.
	Timeout time.Duration
}

// DefaultLimitsFor returns the effective default bounds for a built-in
// tool by name. Unknown names get a safe generic bound (30s timeout, no
// byte/line caps of their own — the runtime's context guard still
// applies downstream).
func DefaultLimitsFor(name string) Limits {
	switch name {
	case "shell":
		return Limits{MaxBytes: DefaultShellMaxBytes, Timeout: DefaultShellTimeout}
	case "read_file":
		return Limits{MaxBytes: DefaultReadMaxBytes, Timeout: DefaultToolTimeout}
	case "list_files":
		return Limits{MaxLines: DefaultListMaxLines, Timeout: DefaultToolTimeout}
	case "search_files":
		return Limits{MaxLines: DefaultSearchMaxLines, Timeout: DefaultToolTimeout}
	case "find_files":
		return Limits{MaxLines: DefaultFindMaxResults, Timeout: DefaultToolTimeout}
	case "git":
		return Limits{MaxBytes: DefaultGitMaxBytes, Timeout: DefaultToolTimeout}
	case "shell_job":
		return Limits{MaxBytes: DefaultJobMaxBytes, Timeout: DefaultJobTimeout}
	case "secret_scan":
		return Limits{MaxLines: DefaultSecretMaxFindings, Timeout: DefaultToolTimeout}
	default:
		return Limits{Timeout: DefaultToolTimeout}
	}
}

// WithDefaults fills any non-positive field of l from d, returning the
// merged result. Configuration overrides flow through here: explicit
// positive values win, everything else falls back to the tool default.
func (l Limits) WithDefaults(d Limits) Limits {
	if l.MaxBytes <= 0 {
		l.MaxBytes = d.MaxBytes
	}
	if l.MaxLines <= 0 {
		l.MaxLines = d.MaxLines
	}
	if l.Timeout <= 0 {
		l.Timeout = d.Timeout
	}
	return l
}

// LimitsSetter is implemented by tools whose bounds are configurable.
// The runtime applies configured overrides through it right after
// construction (and before agent filtering, so limits survive Filtered
// which reuses the same instances).
type LimitsSetter interface {
	SetLimits(Limits)
}

// LimitsProvider is implemented by tools that can report their resolved
// bounds. The scheduler consults it for the execution timeout so a
// configured override applies end to end.
type LimitsProvider interface {
	ToolLimits() Limits
}

// Truncation describes how output was bounded: structured truncation
// information that travels in Result.Metadata instead of silently
// cutting output.
type Truncation struct {
	Truncated bool
	// OriginalBytes is the output size before bounding.
	OriginalBytes int
	// KeptBytes is the output size after bounding.
	KeptBytes int
	// Limit is the byte bound that was applied.
	Limit int
}

// Fields renders the truncation as Result.Metadata entries.
func (t Truncation) Fields() map[string]any {
	return map[string]any{
		"truncated":      t.Truncated,
		"original_bytes": t.OriginalBytes,
		"kept_bytes":     t.KeptBytes,
		"limit_bytes":    t.Limit,
	}
}

// TruncateString cuts s to at most maxBytes on a rune boundary,
// returning the kept prefix and its truncation record. Values <= 0
// return s unchanged with Truncated=false. The caller formats its own
// model-visible marker from the record: shared cutting, per-tool
// wording (so existing markers and their tests never churn).
func TruncateString(s string, maxBytes int) (string, Truncation) {
	rec := Truncation{OriginalBytes: len(s), KeptBytes: len(s), Limit: maxBytes}
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, rec
	}
	cut := 0
	for i, r := range s {
		if i+utf8.RuneLen(r) > maxBytes {
			break
		}
		cut = i + utf8.RuneLen(r)
	}
	if cut == 0 {
		cut = 1
		for cut < len(s) && !utf8.ValidString(s[:cut]) {
			cut++
		}
	}
	rec.Truncated = true
	rec.KeptBytes = cut
	return s[:cut], rec
}

// CapLines keeps at most maxLines lines of s, reporting whether anything
// was dropped. Values <= 0 return s unchanged. The trailing newline, if
// any, is preserved on the kept prefix.
func CapLines(s string, maxLines int) (string, bool) {
	if maxLines <= 0 {
		return s, false
	}
	trailing := strings.HasSuffix(s, "\n")
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= maxLines {
		return s, false
	}
	out := strings.Join(lines[:maxLines], "\n")
	if trailing {
		out += "\n"
	}
	return out, true
}

// ClampTimeout bounds d to (0, MaxTimeout]: non-positive values fall
// back to def, oversized values clamp to the hard ceiling.
func ClampTimeout(d, def time.Duration) time.Duration {
	if d <= 0 {
		d = def
	}
	if d > MaxTimeout {
		d = MaxTimeout
	}
	return d
}

// TruncationNote formats the shared model-visible marker for byte
// truncation. Tools with an established marker keep their own wording;
// new truncation sites use this one.
func TruncationNote(kept, total int) string {
	return fmt.Sprintf("\n[...output truncated at %d bytes, %d bytes total. Re-run with narrower output (e.g. filters, -run, grep) if you need the rest.]", kept, total)
}
