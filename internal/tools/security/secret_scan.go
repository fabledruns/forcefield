// Package security provides the secret_scan tool: a strictly defensive,
// deterministic, local-only heuristic scanner that REPORTS possible
// hardcoded secrets. It never transmits anything, never uses the network,
// never validates credentials against services, and never uses findings.
//
// Confinement mirrors read_file: WSL mode cages paths to the workspace;
// native mode uses the path as given. Findings are reported with redacted
// snippets (match middle masked) as defense-in-depth on top of the
// scheduler's output scrubbing.
package security

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

const (
	// maxScanBytes bounds a single scanned file. Larger files are refused
	// with a note, not read partially.
	maxScanBytes = 1 << 20 // 1 MiB
	// maxFindings bounds reported findings.
	maxFindings = 50
)

// rule is one deterministic detection pattern.
type rule struct {
	id      string
	pattern *regexp.Regexp
}

func mustRule(id, expr string) rule {
	return rule{id: id, pattern: regexp.MustCompile(expr)}
}

// rules is the fixed built-in pattern list. Deliberately small and
// high-signal; this is a hygiene helper, not a detection framework.
var rules = []rule{
	mustRule("aws-access-key", `AKIA[0-9A-Z]{16}`),
	mustRule("github-token", `gh[pousr]_[A-Za-z0-9_]{36,}`),
	mustRule("slack-token", `xox[bpas]-[A-Za-z0-9-]{10,}`),
	mustRule("google-api-key", `AIza[0-9A-Za-z_-]{35}`),
	mustRule("private-key-block", `-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`),
	mustRule("generic-secret-assign", `(?i)(?:api[_-]?key|api[_-]?secret|secret|password|passwd|pwd|token)\s*[:=]\s*['"]?[^'"'\s]{8,}['"]?`),
	mustRule("bearer-token", `(?i)bearer\s+[A-Za-z0-9\-._~+/]{20,}`),
	mustRule("connection-string-secret", `(?i)(?:mongodb|postgres|mysql|redis|amqp)://[^:]+:[^@\s]+@[^\s]+`),
}

// SecretScan scans one file (or inline text) for hardcoded secrets.
type SecretScan struct {
	policy sandbox.Policy
}

// NewSecretScan returns a ready-to-register SecretScan tool.
func NewSecretScan() *SecretScan { return &SecretScan{} }

// NewSecretScanWithPolicy returns a SecretScan confined to
// policy.Workspace when policy.Mode is wsl.
func NewSecretScanWithPolicy(p sandbox.Policy) *SecretScan { return &SecretScan{policy: p} }

func (SecretScan) Name() string { return "secret_scan" }

func (SecretScan) Description() string {
	return "Defensively scan one file (or inline text) for hardcoded secrets and report matches with redacted snippets. " +
		"Local-only: never transmits, validates, or uses findings. Max 50 findings."
}

func (SecretScan) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File to scan, absolute or relative. Exactly one of path or text is required.",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "Inline text to scan. Exactly one of path or text is required.",
			},
		},
	}
}

type finding struct {
	line int
	rule string
	snip string
}

func (s SecretScan) Execute(_ context.Context, args map[string]any) (tools.Result, error) {
	path, _ := args["path"].(string)
	text, _ := args["text"].(string)
	hasPath := strings.TrimSpace(path) != ""
	hasText := text != ""
	if hasPath == hasText {
		return tools.Result{}, fmt.Errorf("secret_scan: exactly one of \"path\" or \"text\" is required")
	}

	var (
		data []byte
		name string
	)
	if hasText {
		if len(text) > maxScanBytes {
			return tools.Result{IsError: true, Content: fmt.Sprintf("text is %d bytes, exceeds %d byte limit", len(text), maxScanBytes)}, nil
		}
		data = []byte(text)
		name = "<text>"
	} else {
		resolved := path
		if s.policy.Mode == sandbox.ModeWSL {
			rp, err := sandbox.ResolveWithinWorkspace(s.policy.Workspace, path)
			if err != nil {
				return tools.Result{IsError: true, Content: fmt.Sprintf("cannot scan %s: %v", path, err)}, nil
			}
			resolved = rp
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot scan %s: %v", path, err)}, nil
		}
		if info.IsDir() {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot scan %s: is a directory", path)}, nil
		}
		if info.Size() > maxScanBytes {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot scan %s: file is %d bytes, exceeds %d byte limit", path, info.Size(), maxScanBytes)}, nil
		}
		raw, err := os.ReadFile(resolved)
		if err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot scan %s: %v", path, err)}, nil
		}
		data = raw
		name = path
	}

	var findings []finding
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		for _, r := range rules {
			loc := r.pattern.FindStringIndex(line)
			if loc == nil {
				continue
			}
			findings = append(findings, finding{line: i + 1, rule: r.id, snip: redact(line, loc[0], loc[1])})
			if len(findings) >= maxFindings {
				break
			}
		}
		if len(findings) >= maxFindings {
			break
		}
	}

	if len(findings) == 0 {
		return tools.Result{Content: fmt.Sprintf("no hardcoded secrets detected in %s (%d rules checked)", name, len(rules))}, nil
	}

	var b strings.Builder
	for _, f := range findings {
		fmt.Fprintf(&b, "%s:%d: %s: %s\n", name, f.line, f.rule, f.snip)
	}
	out := strings.TrimRight(b.String(), "\n")
	if len(findings) >= maxFindings {
		out += fmt.Sprintf("\n\n[truncated at %d findings]", maxFindings)
	}
	out += "\n\nThese are heuristic matches for review only. Rotate any real credentials; do not paste them elsewhere."
	return tools.Result{Content: out}, nil
}

// redact masks the middle of a matched region, keeping 2 chars on each
// side for identifiability without exposing the secret.
func redact(line string, start, end int) string {
	if start < 0 || end > len(line) || start >= end {
		return "[redacted]"
	}
	const keep = 2
	matched := line[start:end]
	if len(matched) <= keep*2 {
		return line[:start] + "[redacted]" + line[end:]
	}
	masked := matched[:keep] + strings.Repeat("•", len(matched)-keep*2) + matched[len(matched)-keep:]
	snip := line[:start] + masked + line[end:]
	if len(snip) > 300 {
		snip = snip[:300] + "…[truncated]"
	}
	return snip
}
