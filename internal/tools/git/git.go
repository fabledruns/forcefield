// Package git provides the read-only git inspection tool: status,
// diffs, and history for repositories under the workspace.
//
// The tool is intentionally read-only: only a fixed allowlist of
// inspection subcommands can run (status, diff, staged, log, changed).
// Anything else — including commit, add, checkout, reset, and clean —
// is rejected before any process starts. Destructive work stays in the
// shell tool behind the permission system.
//
// Path arguments resolve through the shared workspace boundary
// (sandbox.ResolveWithinWorkspace when the policy confines, otherwise
// like read_file), and every git invocation runs with the resolved
// workspace root as its working directory, scoped to it via pathspec,
// so output never escapes the project. No destructive flag can reach
// the command line because argv is built from constants per action.
package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// Actions understood by the git tool. This closed set is the read-only
// guarantee: adding a mutating action requires editing this list.
const (
	ActionStatus  = "status"
	ActionDiff    = "diff"
	ActionStaged  = "staged"
	ActionLog     = "log"
	ActionChanged = "changed"
)

// defaultLogLimit bounds `git log` entries per call.
const defaultLogLimit = 10

// maxLogLimit caps the caller-supplied log limit.
const maxLogLimit = 50

// Git inspects a git repository under the workspace.
type Git struct {
	policy sandbox.Policy
	// limits overrides the output bound. Zero values resolve via
	// tools.DefaultLimitsFor("git").
	limits tools.Limits
}

// NewGit returns a ready-to-register Git tool.
func NewGit() *Git { return &Git{} }

// NewGitWithPolicy returns a Git tool confined to policy.Workspace when
// the policy confines; otherwise native behavior.
func NewGitWithPolicy(p sandbox.Policy) *Git { return &Git{policy: p} }

// SetLimits overrides the output bound. Only positive fields take
// effect; the rest resolve to the tool defaults.
func (g *Git) SetLimits(l tools.Limits) {
	g.limits = l
}

// ToolLimits reports the resolved bounds.
func (g *Git) ToolLimits() tools.Limits {
	return g.resolveLimits()
}

func (g *Git) resolveLimits() tools.Limits {
	if g == nil {
		return tools.DefaultLimitsFor("git")
	}
	return g.limits.WithDefaults(tools.DefaultLimitsFor("git"))
}

func (Git) Name() string { return "git" }

func (Git) Description() string {
	return "Inspect a git repository (read-only): status, unstaged diff, staged diff, recent log, or changed files. " +
		"Never commits, stages, or modifies anything. Paths resolve inside the workspace."
}

func (Git) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "One of \"status\", \"diff\" (unstaged), \"staged\", \"log\", \"changed\".",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional file or directory to scope the action to, inside the workspace.",
			},
			"limit": map[string]any{
				"type":        "string",
				"description": "Optional log entry count for \"log\" (1-50, default 10).",
			},
		},
		"required": []string{"action"},
	}
}

// gitBinary is a seam over exec.LookPath so tests can simulate git
// being absent without touching the real PATH.
var gitBinary = exec.LookPath

func (g Git) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	started := time.Now()
	action, err := tools.StringArg(args, "action")
	if err != nil {
		return tools.Result{}, err
	}
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case ActionStatus, ActionDiff, ActionStaged, ActionLog, ActionChanged:
	default:
		return tools.Result{IsError: true, Content: fmt.Sprintf(
			"unsupported git action %q (supported: status, diff, staged, log, changed); the git tool is read-only", action)}, nil
	}

	git, err := gitBinary("git")
	if err != nil {
		return tools.Result{IsError: true, Content: "git is not available on PATH"}, nil
	}

	// The repository under inspection is the resolved workspace root:
	// one root, resolved once, shared by every action.
	root, err := resolveRepoRoot(g.policy)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot inspect git repository: %v", err)}, nil
	}
	if _, _, _, err := g.runCapped(ctx, git, root, "rev-parse", "--is-inside-work-tree"); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("%s is not a git repository", root)}, nil
	}

	// Optional path scoping, resolved inside the workspace like
	// read_file: confined modes cage, permissive anchors to cwd. The
	// scope must stay under the root so output never leaves it.
	scope := "."
	if raw, _ := args["path"].(string); strings.TrimSpace(raw) != "" {
		p, err := resolveInScope(g.policy, strings.TrimSpace(raw))
		if err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot scope git %s to %s: %v", action, raw, err)}, nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return tools.Result{IsError: true, Content: fmt.Sprintf("path %q is outside the repository %s", raw, root)}, nil
		}
		scope = rel
	}

	var out string
	var seen int
	var cut bool
	switch action {
	case ActionStatus:
		out, seen, cut, err = g.runCapped(ctx, git, root, "status", "--porcelain=v1", "-b", "--", scope)
	case ActionDiff:
		out, seen, cut, err = g.runCapped(ctx, git, root, "diff", "--no-color", "--no-ext-diff", "--", scope)
	case ActionStaged:
		out, seen, cut, err = g.runCapped(ctx, git, root, "diff", "--cached", "--no-color", "--no-ext-diff", "--", scope)
	case ActionLog:
		n := defaultLogLimit
		if raw, _ := args["limit"].(string); strings.TrimSpace(raw) != "" {
			v, verr := strconv.Atoi(strings.TrimSpace(raw))
			if verr != nil || v < 1 || v > maxLogLimit {
				return tools.Result{IsError: true, Content: fmt.Sprintf(
					"invalid limit %q (want 1-%d)", raw, maxLogLimit)}, nil
			}
			n = v
		}
		out, seen, cut, err = g.runCapped(ctx, git, root, "log", "--oneline", "-n", strconv.Itoa(n), "--", scope)
	case ActionChanged:
		out, seen, cut, err = g.changed(ctx, git, root, scope)
	}
	if err != nil {
		// Scheduler-level redaction still applies downstream; git's
		// own errors echo paths and refs, never file contents.
		return tools.Result{IsError: true, Content: fmt.Sprintf("git %s failed: %v", action, err),
			Tool: "git", DurationMs: time.Since(started).Milliseconds()}, nil
	}

	content, truncMeta := boundOutput(out, seen, cut, g.resolveLimits().MaxBytes, action)
	return tools.Result{
		Content:    content,
		ExitCode:   0,
		Stdout:     content,
		Tool:       "git",
		Metadata:   truncMeta,
		DurationMs: time.Since(started).Milliseconds(),
	}, nil
}

// changed reports tracked modifications versus HEAD plus untracked
// files, both scoped. Two fixed read-only commands; either failing
// surfaces as a soft error. Output is already capture-bounded; the cut
// flags OR together.
func (g Git) changed(ctx context.Context, git, root, scope string) (string, int, bool, error) {
	tracked, seenT, cut1, err := g.runCapped(ctx, git, root, "diff", "--name-only", "HEAD", "--", scope)
	if err != nil {
		return "", 0, false, err
	}
	untrackedOut, seenU, cut2, err := g.runCapped(ctx, git, root, "ls-files", "--others", "--exclude-standard", "--", scope)
	if err != nil {
		return "", 0, false, err
	}
	var b strings.Builder
	if strings.TrimSpace(tracked) != "" {
		b.WriteString("Modified:\n" + tracked + "\n")
	}
	if strings.TrimSpace(untrackedOut) != "" {
		b.WriteString("Untracked:\n" + untrackedOut + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), seenT + seenU, cut1 || cut2, nil
}

// runCapped runs one fixed git argv with root as cwd, capturing stdout
// through a bounded writer so a pathological diff cannot grow memory
// without bound: bytes past the resolved cap are counted and discarded
// while the process still drains to completion (so it is always
// reaped). Stderr gets its own small bound; git writes progress and
// errors there and the model needs both. Cancellation aborts the
// process via the context. Git's own output for binary blobs stays
// terse ("Binary files differ") because no --text flag is ever passed.
func (g Git) runCapped(ctx context.Context, git, root string, argv ...string) (string, int, bool, error) {
	maxOut := g.resolveLimits().MaxBytes
	stdout := &cappedWriter{max: maxOut}
	stderr := &cappedWriter{max: 64 << 10}
	cmd := exec.CommandContext(ctx, git, append([]string{"-C", root}, argv...)...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Never inherit surprising fds; the environment passes through (git
	// needs HOME/SystemRoot for config lookups).
	cmd.Stdin = nil
	if err := cmd.Run(); err != nil {
		detail := strings.TrimRight(stderr.String(), "\n")
		if detail == "" {
			detail = err.Error()
		}
		return "", stdout.total, stdout.cut || stderr.cut, fmt.Errorf("%s", detail)
	}
	out := strings.TrimRight(stdout.String(), "\n")
	if errText := strings.TrimRight(stderr.String(), "\n"); errText != "" {
		out += "\n[stderr: " + errText + "]"
	}
	return out, stdout.total, stdout.cut || stderr.cut, nil
}

// cappedWriter keeps the first max bytes and counts everything seen,
// discarding the rest, so producers never block and memory stays
// bounded while truncation metadata stays exact.
type cappedWriter struct {
	buf   []byte
	max   int
	total int
	cut   bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.total += len(p)
	if w.cut {
		return len(p), nil
	}
	if w.max <= 0 || len(w.buf)+len(p) <= w.max {
		w.buf = append(w.buf, p...)
		return len(p), nil
	}
	keep := w.max - len(w.buf)
	if keep > 0 {
		w.buf = append(w.buf, p[:keep]...)
	}
	w.cut = true
	return len(p), nil
}

func (w *cappedWriter) String() string { return string(w.buf) }

// resolveRepoRoot resolves the repository root the same way the search
// tools resolve theirs: confined modes cage to the workspace,
// permissive native anchors at the process cwd (or an explicitly
// configured workspace).
func resolveRepoRoot(policy sandbox.Policy) (string, error) {
	if policy.Confines() {
		return sandbox.ResolveWithinWorkspace(policy.Workspace, ".")
	}
	if strings.TrimSpace(policy.Workspace) != "" {
		abs, err := filepath.Abs(policy.Workspace)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	return os.Getwd()
}

// resolveInScope resolves a user-supplied path for the optional scope
// filter: confined modes cage to the workspace, permissive mode treats
// it like read_file (absolute as-is, relative anchored at cwd).
func resolveInScope(policy sandbox.Policy, path string) (string, error) {
	if policy.Confines() {
		return sandbox.ResolveWithinWorkspace(policy.Workspace, path)
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, filepath.Clean(path)), nil
}

// runGit executes one fixed git argv with root as cwd, combining
// stdout and stderr (git writes progress and errors to stderr, and the
// model needs both). Cancellation aborts the process via the context.
func runGit(ctx context.Context, git, root string, argv ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, git, append([]string{"-C", root}, argv...)...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	// Never inherit surprising fds; the environment passes through (git
	// needs HOME/SystemRoot for config lookups).
	cmd.Stdin = nil
	err := cmd.Run()
	out := strings.TrimRight(buf.String(), "\n")
	if err == nil {
		return out, 0, nil
	}
	code := 1
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	}
	return out, code, err
}

// boundOutput turns captured command output into model content: empty
// output becomes an explicit "clean" note (so "no changes" is
// distinguishable from silent failure), and capture-cut output carries
// the truncation marker plus structured metadata (nil when uncut).
func boundOutput(out string, seen int, cut bool, maxBytes int, action string) (string, map[string]any) {
	if strings.TrimSpace(out) == "" {
		return fmt.Sprintf("git %s: clean (no output)", action), nil
	}
	// Porcelain status always prints the ## branch header, even when
	// nothing changed: a header alone means a clean tree.
	if action == "status" && statusIsClean(out) {
		header := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
		return fmt.Sprintf("%s\n\n[working tree clean]", header), nil
	}
	if !cut {
		return out, nil
	}
	meta := tools.Truncation{Truncated: true, OriginalBytes: seen, KeptBytes: len(out), Limit: maxBytes}.Fields()
	out += fmt.Sprintf("\n\n[...git output truncated at %d bytes; narrow with path or limit]", maxBytes)
	return out, meta
}

// statusIsClean reports whether porcelain status output holds only the
// ## branch header (no changed, staged, or untracked entries).
func statusIsClean(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, "##") {
			return false
		}
	}
	return true
}
