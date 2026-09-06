// Package redact is Forcefield's single centralized secret-redaction
// mechanism. Every boundary that can carry secrets outward — tool
// results, shell output, provider errors, session files, memory,
// diagnostics — scrubs through here, so coverage is audited in one
// place instead of scattered across packages.
//
// Detection is deliberately conservative and explicit, never perfect:
// targeted patterns for known credential shapes plus exact values
// registered from the environment (see AddSecret). Anything unusual
// should be added here with a test, not inline in a caller.
package redact

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

type pattern struct {
	re   *regexp.Regexp
	mask string
}

func mustPattern(expr, mask string) pattern {
	return pattern{re: regexp.MustCompile(expr), mask: mask}
}

// patterns covers known credential shapes. Replacements name the kind
// redacted so output stays debuggable ("[redacted aws key]" rather than
// a bare hole), except the two historical patterns whose wording is
// pinned by existing behavior.
var patterns = []pattern{
	// Historical patterns (wording preserved for compatibility).
	mustPattern(`(?i)sk-[A-Za-z0-9\-_]{20,}`, "[redacted]"),
	mustPattern(`(?i)gsk_[A-Za-z0-9]{20,}`, "[redacted]"),
	mustPattern(`(?i)(api[_-]?key|apikey)\s*[:=]\s*["']?[^"'\s,;]+["']?`, "[redacted api_key]"),
	mustPattern(`(?i)bearer\s+[A-Za-z0-9\-_\.]{20,}`, "[redacted bearer]"),
	mustPattern(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`, "[redacted private key]"),
	// Provider-issued tokens with unmistakable prefixes.
	mustPattern(`gh[pousr]_[A-Za-z0-9_]{36,}`, "[redacted github token]"),
	mustPattern(`xox[bpas]-[A-Za-z0-9-]{10,}`, "[redacted slack token]"),
	mustPattern(`AKIA[0-9A-Z]{16}`, "[redacted aws key]"),
	mustPattern(`AIza[0-9A-Za-z_-]{35}`, "[redacted google key]"),
	// Credential assignments: quoted values of any length are a strong
	// signal; unquoted values need length to avoid eating normal code.
	// The whole assignment is replaced so neither name nor value leaks
	// structure that helps guessing.
	mustPattern(`(?i)(?:password|passwd|pwd|secret|token|api[_-]?key|api[_-]?secret)\s*[:=]\s*('[^']{1,200}'|"[^"]{1,200}"|[^\s'";,]{8,})`, "[redacted credential]"),
}

// keywords is the fast-path prefilter: Scrub skips all pattern work
// unless the lowercased content contains one of these. Every alternative
// of every pattern contains one of these literals (credential prefixes,
// key names, or the PEM header), so gating on them loses nothing.
var keywords = []string{
	"sk-", "gsk_", "api", "bearer", "begin",
	"ghp_", "gho_", "ghu_", "ghs_", "ghr_",
	"xoxb-", "xoxp-", "xoxa-", "xoxs-", "akia", "aiza",
	"password", "passwd", "pwd", "secret", "token",
}

// minSecretLen is the shortest exact value ever registered: shorter
// strings are too likely to be ordinary words.
const minSecretLen = 8

// maxSecrets bounds the exact-value registry.
const maxSecrets = 64

var (
	secretsMu sync.RWMutex
	secrets   []string
)

// AddSecret registers an exact credential value (typically an API key
// loaded from the environment) for literal redaction. Values shorter
// than minSecretLen are ignored. The registry is process-memory only,
// capped FIFO, and safe for concurrent use.
func AddSecret(value string) {
	if len(value) < minSecretLen {
		return
	}
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for _, s := range secrets {
		if s == value {
			return
		}
	}
	if len(secrets) >= maxSecrets {
		secrets = append([]string(nil), secrets[1:]...)
	}
	secrets = append(secrets, value)
}

// ResetSecrets clears the exact-value registry. It exists for tests;
// production code only ever adds.
func ResetSecrets() {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	secrets = nil
}

// snapshot returns registered values longest-first so overlapping
// values replace greedily on the longest match.
func snapshot() []string {
	secretsMu.RLock()
	defer secretsMu.RUnlock()
	out := append([]string(nil), secrets...)
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// Scrub redacts likely secrets from content before it is persisted,
// displayed, logged, or sent to a provider. It is conservative: any
// match is replaced with an explicit marker. It never relies on the
// model to recognize secrets, and it never claims perfect detection.
func Scrub(content string) string {
	if content == "" {
		return content
	}
	lower := strings.ToLower(content)
	hit := false
	for _, k := range keywords {
		if strings.Contains(lower, k) {
			hit = true
			break
		}
	}
	out := content
	if hit {
		for _, p := range patterns {
			out = p.re.ReplaceAllString(out, p.mask)
		}
	}
	for _, s := range snapshot() {
		if s != "" && strings.Contains(out, s) {
			out = strings.ReplaceAll(out, s, "[redacted]")
		}
	}
	return out
}

// ScrubMap returns a copy of args with every string value scrubbed.
// Use it for persisted argument maps (pending tool calls, audit
// records) so secrets in file paths, commands, or content never reach
// disk. Non-string values pass through untouched.
func ScrubMap(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if s, ok := v.(string); ok {
			out[k] = Scrub(s)
		} else {
			out[k] = v
		}
	}
	return out
}

// scrubError is an error with secrets scrubbed from its message. It
// unwraps to the original so errors.Is/As callers keep working.
type scrubError struct {
	msg string
	err error
}

func (e *scrubError) Error() string { return e.msg }
func (e *scrubError) Unwrap() error { return e.err }

// ScrubError redacts secrets from err's message for user-facing
// surfacing (transcript error lines, CLI output, diagnostics) while
// preserving the chain for errors.Is/As. Nil passes through.
func ScrubError(err error) error {
	if err == nil {
		return nil
	}
	msg := Scrub(err.Error())
	if msg == err.Error() {
		return err
	}
	return &scrubError{msg: msg, err: err}
}
