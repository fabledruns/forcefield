package redact

import (
	"errors"
	"strings"
	"testing"
)

func TestPatterns(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		absent    []string // must not survive scrubbing
		marker    string   // must appear when absent is non-empty
		unchanged bool     // input must pass through byte-identical
	}{
		{name: "sk", input: "key is sk-12345678901234567890abcdef", absent: []string{"sk-12345678901234567890abcdef"}, marker: "[redacted]"},
		{name: "gsk", input: "token gsk_abc12345678901234567890", absent: []string{"gsk_abc12345678901234567890"}, marker: "[redacted]"},
		{name: "api assign", input: `api_key="secretvalue1234567890"`, absent: []string{"secretvalue1234567890"}, marker: "redacted"},
		{name: "bearer", input: "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.abc123456789012345", absent: []string{"eyJhbGci"}, marker: "[redacted bearer]"},
		{name: "private key", input: "-----BEGIN PRIVATE KEY-----\nMIIEvQ\n-----END PRIVATE KEY-----", absent: []string{"MIIEvQ"}, marker: "[redacted private key]"},
		{name: "github", input: "ghp_abcdefghijklmnopqrstuvwxyz0123456789ABCD", absent: []string{"ghp_"}, marker: "[redacted github token]"},
		{name: "slack", input: "xoxb-123456789012-abcdefghij", absent: []string{"xoxb-"}, marker: "[redacted slack token]"},
		{name: "aws", input: "AKIAIOSFODNN7EXAMPLE", absent: []string{"AKIAIOSFODNN7EXAMPLE"}, marker: "[redacted aws key]"},
		{name: "google", input: "AIzaSyB-CbdlD_EFGhIjKlMnOpQrStUvWxYz123", absent: []string{"AIzaSy"}, marker: "[redacted google key]"},
		{name: "password quoted", input: `password = "hunter2"`, absent: []string{"hunter2"}, marker: "[redacted credential]"},
		{name: "secret unquoted", input: "secret: supersecretvalue", absent: []string{"supersecretvalue"}, marker: "[redacted credential]"},
		{name: "token env style", input: "GITHUB_TOKEN=ghp_shortbutquotedvalue", absent: []string{"ghp_shortbutquotedvalue"}, marker: "[redacted credential]"},
		{name: "api secret", input: "api_secret: abcdefgh12345678", absent: []string{"abcdefgh12345678"}, marker: "[redacted credential]"},
		// Non-secrets must survive byte-identical (no aggressive redaction).
		{name: "plain", input: "normal text without secrets", unchanged: true},
		{name: "short sk", input: "prefix sk-short here", unchanged: true},
		{name: "short token assign", input: "token = x", unchanged: true},
		{name: "bare words", input: "the password policy requires a token", unchanged: true},
		{name: "code identifiers", input: "const tokenTTL = 3600; getToken(); tokenTTL", unchanged: true},
		{name: "auth prose", input: "authorization is required for this endpoint", unchanged: true},
		{name: "short aws-like", input: "AKIA123", unchanged: true},
		{name: "short github-like", input: "ghp_short", unchanged: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Scrub(tc.input)
			if tc.unchanged {
				if got != tc.input {
					t.Errorf("Scrub(%q) = %q, want unchanged", tc.input, got)
				}
				return
			}
			for _, s := range tc.absent {
				if strings.Contains(got, s) {
					t.Errorf("Scrub(%q) = %q, still contains %q", tc.input, got, s)
				}
			}
			if tc.marker != "" && !strings.Contains(got, tc.marker) {
				t.Errorf("Scrub(%q) = %q, want marker %q", tc.input, got, tc.marker)
			}
		})
	}
}

func TestEnvRegistry(t *testing.T) {
	ResetSecrets()
	defer ResetSecrets()
	AddSecret("nvapi-super-secret-value-12345")
	got := Scrub("key=nvapi-super-secret-value-12345 end")
	if strings.Contains(got, "nvapi-super-secret-value-12345") {
		t.Errorf("registered value survived: %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Errorf("missing marker: %q", got)
	}
	// Short values are ignored (too likely ordinary words).
	AddSecret("short")
	if got := Scrub("a short word"); got != "a short word" {
		t.Errorf("short value redacted ordinary text: %q", got)
	}
	// Duplicates and empty input are safe.
	AddSecret("nvapi-super-secret-value-12345")
	if got := Scrub(""); got != "" {
		t.Errorf("empty = %q", got)
	}
}

func TestScrubMap(t *testing.T) {
	in := map[string]any{
		"command": `curl -H "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc12345678901234567890" https://x`,
		"count":   3,
		"nested":  map[string]any{"k": "v"},
	}
	out := ScrubMap(in)
	if strings.Contains(out["command"].(string), "eyJhbGci") {
		t.Errorf("command not scrubbed: %q", out["command"])
	}
	if out["count"] != 3 {
		t.Errorf("non-string changed: %v", out["count"])
	}
	if in["command"] == out["command"] {
		t.Error("ScrubMap mutated the input map instead of copying")
	}
	if ScrubMap(nil) != nil {
		t.Error("ScrubMap(nil) != nil")
	}
}

func TestScrubError(t *testing.T) {
	if ScrubError(nil) != nil {
		t.Error("ScrubError(nil) != nil")
	}
	plain := errors.New("boom")
	if ScrubError(plain) != plain {
		t.Error("clean error should pass through identical")
	}
	inner := errors.New("read sk-12345678901234567890abcdef failed")
	wrapped := errors.New("wrap: " + inner.Error())
	// Simulate a wrapped chain.
	err := &chainErr{msg: "call failed: " + inner.Error(), cause: inner}
	got := ScrubError(err)
	if strings.Contains(got.Error(), "sk-12345678901234567890abcdef") {
		t.Errorf("secret survived: %q", got)
	}
	if !errors.Is(got, inner) {
		t.Error("ScrubError broke errors.Is unwrapping")
	}
	_ = wrapped
}

type chainErr struct {
	msg   string
	cause error
}

func (e *chainErr) Error() string { return e.msg }
func (e *chainErr) Unwrap() error { return e.cause }
