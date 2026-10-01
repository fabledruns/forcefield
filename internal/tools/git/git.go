// Package git provides the read-only inspection tool (fixed allowlist, no
// destructive subcommands; workspace-scoped argv). See docs/Tools.md.
package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"forcefield/internal/process"
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

// NewGit returns a ready-to-register Git tool. Repository resolution is
// always confined to the workspace root (the policy's Workspace, or the
// process working directory when unset).
func NewGit() *Git { return &Git{} }

// NewGitWithPolicy returns a Git tool confined to policy.Workspace. It
// behaves like NewGit: confinement is unconditional, and the policy
// only selects which root to cage to.
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

// gitWaitDelay bounds pipe drain after cancel/timeout, matching the
// shell tool: a descendant holding a pipe open cannot wedge reaping.
const gitWaitDelay = time.Second

// hardeningConfigArgs neutralizes repo-controlled execution on every
// invocation. -c overrides beat every config file, including the
// inspected repo's own .git/config. Verified: a repo setting
// core.fsmonitor executes on status without this; with
// `-c core.fsmonitor=` it does not. Textconv and external diff are
// disabled via --no-textconv/--no-ext-diff on the diff argv itself.
// Content-filter drivers (filter.<name>.clean/smudge/process, e.g.
// git-lfs) are enumerated per repository and neutralized the same way
// (see filterOverrideArgs): status and unstaged diff hash worktree
// content through the clean filter, so a repo-defined driver executes
// without it. Builtin command words (status/diff/log/…) are immune to
// [alias] overrides by git design (verified); only non-builtin words
// expand aliases, which this tool never invokes.
var hardeningConfigArgs = []string{"-c", "core.fsmonitor="}

// maxFilterDrivers bounds -c overrides per invocation so a config
// stuffed with driver sections cannot bloat argv.
const maxFilterDrivers = 32

// filterDriverKey matches filter driver keys whose values execute:
// filter.<name>.clean|smudge|process. Anything else under filter.*
// (e.g. .required booleans) cannot spawn a process.
var filterDriverKey = regexp.MustCompile(`^filter\.(.+)\.(clean|smudge|process)$`)

// filterOverrideArgs neutralizes every content-filter driver in the
// effective config by replacing its helpers with cat (identity
// conversion: content passes through byte-identical, nothing
// executes). Enumeration itself is safe plumbing: `git config
// --get-regexp` reads config without touching the worktree, and runs
// under the same hardened spawn. Drivers named only in .gitattributes
// need no override: with no configured helper there is nothing to run.
// Repos relying on real conversion (e.g. git-lfs process drivers)
// degrade to raw-pointer output or a soft diff error instead of
// executing repo-configured binaries — inspection stays read-only.
func (g Git) filterOverrideArgs(ctx context.Context, git, root string) []string {
	out, _, _, err := g.runCappedWith(ctx, git, root, nil, "config", "--get-regexp", `^filter\.`)
	if err != nil {
		// Exit 1 with empty output is the normal no-drivers case; a
		// real failure here also just means no overrides.
		if strings.TrimSpace(out) == "" {
			return nil
		}
	}
	seen := make(map[string]bool)
	var args []string
	for _, line := range strings.Split(out, "\n") {
		key, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		m := filterDriverKey.FindStringSubmatch(key)
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		if len(seen) > maxFilterDrivers {
			break
		}
		name := m[1]
		args = append(args,
			"-c", "filter."+name+".clean=cat",
			"-c", "filter."+name+".smudge=cat",
			"-c", "filter."+name+".process=cat",
		)
	}
	return args
}

// gitAllowedEnv names the only host variables a git child receives.
// Everything else (including GIT_DIR/GIT_WORK_TREE redirections,
// GIT_CONFIG_COUNT injections, and any secret-bearing variables the
// Forcefield process holds) is dropped. System and global configs keep
// working because HOME/SystemRoot resolution is preserved; only the
// repo's dangerous keys are overridden via -c above.
var gitAllowedEnv = []string{
	"PATH", "PATHEXT",
	"HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
	"SystemRoot", "TEMP", "TMP",
}

// gitSetEnv holds variables git always receives regardless of the host.
var gitSetEnv = []string{
	// Never take index locks: inspection must not mutate or contend.
	"GIT_OPTIONAL_LOCKS=0",
	// Never spawn an interactive pager, even if output ever reaches a tty.
	"GIT_PAGER=cat",
}

// gitEnv builds the child environment: the allowlisted host entries
// plus the forced neutralizations. Values are never expanded.
func gitEnv() []string {
	keep := func(name string) bool {
		for _, a := range gitAllowedEnv {
			if runtime.GOOS == "windows" {
				if strings.EqualFold(name, a) {
					return true
				}
			} else if name == a {
				return true
			}
		}
		return false
	}
	out := make([]string, 0, len(gitAllowedEnv)+len(gitSetEnv))
	for _, kv := range os.Environ() {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if keep(name) {
			out = append(out, kv)
		}
	}
	return append(out, gitSetEnv...)
}

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
	// read_file. The scope must stay under the root so output never
	// leaves it.
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
	// Neutralize content-filter drivers once per call; every worktree
	// action below reuses the same overrides.
	filterArgs := g.filterOverrideArgs(ctx, git, root)
	run := func(argv ...string) (string, int, bool, error) {
		return g.runCappedWith(ctx, git, root, filterArgs, argv...)
	}
	switch action {
	case ActionStatus:
		out, seen, cut, err = run("status", "--porcelain=v1", "-b", "--", scope)
	case ActionDiff:
		out, seen, cut, err = run("diff", "--no-color", "--no-ext-diff", "--no-textconv", "--", scope)
	case ActionStaged:
		out, seen, cut, err = run("diff", "--cached", "--no-color", "--no-ext-diff", "--no-textconv", "--", scope)
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
		out, seen, cut, err = run("log", "--oneline", "-n", strconv.Itoa(n), "--", scope)
	case ActionChanged:
		out, seen, cut, err = g.changed(ctx, git, root, scope, filterArgs)
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
func (g Git) changed(ctx context.Context, git, root, scope string, filterArgs []string) (string, int, bool, error) {
	tracked, seenT, cut1, err := g.runCappedWith(ctx, git, root, filterArgs, "diff", "--no-ext-diff", "--no-textconv", "--name-only", "HEAD", "--", scope)
	if err != nil {
		return "", 0, false, err
	}
	untrackedOut, seenU, cut2, err := g.runCappedWith(ctx, git, root, filterArgs, "ls-files", "--others", "--exclude-standard", "--", scope)
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
// reaped).
//
// Hardening per invocation (see hardeningConfigArgs/gitEnv):
//   - argv is prefixed with -c core.fsmonitor= so a repo-controlled
//     fsmonitor hook never executes;
//   - cwd is pinned to root (plus the historical -C root, kept);
//   - the environment is the allowlisted minimum plus forced
//     GIT_OPTIONAL_LOCKS=0 / GIT_PAGER=cat;
//   - the process joins the hardened lifecycle (Configure pre-Start,
//     group/job Kill on cancel, Track post-Start) exactly like the
//     shell and search_code tools.
//
// Stderr gets its own small bound; git writes progress and errors there
// and the model needs both. Cancellation aborts the whole tree via the
// context. Git's own output for binary blobs stays terse
// ("Binary files differ") because no --text flag is ever passed, and
// textconv/external drivers stay off via --no-textconv/--no-ext-diff on
// the diff argv, and content-filter drivers via per-invocation -c
// overrides (see filterOverrideArgs).
func (g Git) runCapped(ctx context.Context, git, root string, argv ...string) (string, int, bool, error) {
	return g.runCappedWith(ctx, git, root, nil, argv...)
}

// runCappedWith is runCapped plus extra -c config overrides (content-
// filter neutralizations computed once per Execute).
func (g Git) runCappedWith(ctx context.Context, git, root string, extraConfig []string, argv ...string) (string, int, bool, error) {
	maxOut := g.resolveLimits().MaxBytes
	stdout := &cappedWriter{max: maxOut}
	stderr := &cappedWriter{max: 64 << 10}
	full := make([]string, 0, len(argv)+len(hardeningConfigArgs)+len(extraConfig)+2)
	full = append(full, "-C", root)
	full = append(full, hardeningConfigArgs...)
	full = append(full, extraConfig...)
	full = append(full, argv...)
	cmd := exec.CommandContext(ctx, git, full...)
	cmd.Dir = root
	cmd.Env = gitEnv()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Never inherit surprising fds.
	cmd.Stdin = nil
	// Own the process tree exactly like the shell tool: its own group on
	// Unix, a kill-on-close job object on Windows, so timeout and
	// cancellation reap the whole tree including hook/driver children a
	// repo config might still reach.
	process.Configure(cmd)
	cmd.Cancel = func() error { return process.Kill(cmd) }
	cmd.WaitDelay = gitWaitDelay
	if err := cmd.Start(); err != nil {
		detail := strings.TrimRight(stderr.String(), "\n")
		if detail == "" {
			detail = err.Error()
		}
		return "", stdout.total, stdout.cut || stderr.cut, fmt.Errorf("%s", detail)
	}
	release := process.Track(cmd)
	defer release()
	if err := cmd.Wait(); err != nil {
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
// tools resolve theirs: always caged to the workspace (the policy's
// Workspace, or the process working directory when unset).
func resolveRepoRoot(policy sandbox.Policy) (string, error) {
	return sandbox.ResolveWithinWorkspace(policy.Workspace, ".")
}

// resolveInScope resolves a user-supplied path for the optional scope
// filter: always caged to the workspace, exactly like read_file.
func resolveInScope(policy sandbox.Policy, path string) (string, error) {
	return sandbox.ResolveWithinWorkspace(policy.Workspace, path)
}

// CheckBoundary implements tools.BoundaryChecker: it dry-runs the
// workspace boundary decision for a git inspection (canonicalize +
// resolve the scope or repository root, nothing executed) and returns
// the canonical path the inspection would be scoped to.
func (g Git) CheckBoundary(args map[string]any) (string, error) {
	if raw, _ := args["path"].(string); strings.TrimSpace(raw) != "" {
		return resolveInScope(g.policy, strings.TrimSpace(raw))
	}
	return resolveRepoRoot(g.policy)
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
