// Package search provides the search_code tool: fast repository code
// search powered by the external ripgrep binary, with the built-in
// search_files walker as the fallback when rg is unavailable.
//
// Security model mirrors search_files: the search root is caged via
// resolveSearchRoot (sandbox.ResolveWithinWorkspace when the policy
// confines, otherwise cwd-anchored), every reported file is
// symlink-resolved and required to stay within the root, sensitive
// files (see filesystem.IsSensitivePath) are dropped from results, and
// output, scope, and execution time are all bounded.
//
// The rg subprocess is spawned directly via exec.CommandContext with a
// fixed argv built from validated parameters. The pattern travels via
// "-e" and paths behind "--" so model input is always data, never
// flags or shell text. There is deliberately no shell, no free-form
// flags parameter, and no PCRE2/backtracking engine.
package search

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"forcefield/internal/process"
	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
	"forcefield/internal/tools/filesystem"
)

const (
	// maxSearchCodePattern caps the pattern length so a pathological
	// model argument cannot blow up argv or capture before rg runs.
	maxSearchCodePattern = 500
	// maxSearchCodeResults is the hard ceiling for per-call max_results.
	// The configured MaxLines default applies when the caller omits it.
	maxSearchCodeResults = 200
	// searchCodeStderrMax bounds captured rg stderr (warnings and error
	// tails); matches on stdout are bounded by the tool MaxBytes limit.
	searchCodeStderrMax = 64 << 10
	// searchCodeWaitDelay bounds how long Wait lingers on pipe drain
	// after the process group has been killed, mirroring the shell tool.
	searchCodeWaitDelay = time.Second
)

// rgExclusions are never searched: version-control metadata,
// dependency trees, build output, and lockfiles are noise for code
// search. The set mirrors excludedDirNames/isLockFile in search.go so
// rg results stay consistent with the built-in walker. .git is always
// excluded, even when hidden:true.
var rgExclusions = []string{
	"!.git/**",
	"!node_modules/**",
	"!dist/**",
	"!build/**",
	"!target/**",
	"!vendor/**",
	"!.next/**",
	"!__pycache__/**",
	"!*.lock",
}

// rgBinary is a seam over exec.LookPath so tests can simulate rg being
// absent (or stub it) without touching the real PATH.
var rgBinary = exec.LookPath

// SearchCode is the ripgrep-powered code search tool.
type SearchCode struct {
	policy sandbox.Policy
	// limits overrides the match/byte/timeout bounds. Zero values
	// resolve via tools.DefaultLimitsFor("search_code").
	limits tools.Limits
}

// NewSearchCode returns a ready-to-register SearchCode tool.
func NewSearchCode() *SearchCode { return &SearchCode{} }

// NewSearchCodeWithPolicy returns a SearchCode confined to
// policy.Workspace when the policy confines; otherwise native behavior.
func NewSearchCodeWithPolicy(p sandbox.Policy) *SearchCode { return &SearchCode{policy: p} }

// SetLimits overrides the bounds. Only positive fields take effect;
// the rest resolve to the tool defaults.
func (s *SearchCode) SetLimits(l tools.Limits) {
	s.limits = l
}

// ToolLimits reports the resolved bounds.
func (s *SearchCode) ToolLimits() tools.Limits {
	return s.resolveLimits()
}

func (s *SearchCode) resolveLimits() tools.Limits {
	if s == nil {
		return tools.DefaultLimitsFor("search_code")
	}
	return s.limits.WithDefaults(tools.DefaultLimitsFor("search_code"))
}

func (SearchCode) Name() string { return "search_code" }

func (SearchCode) Description() string {
	return "Fast repository code search using ripgrep. Use this instead of reading files one by one when you need to find a symbol, reference, config key, or error string. " +
		"Searches file contents under a directory and returns path:line:column matches. Respects .gitignore, skips .git, dependencies, build output, lockfiles, sensitive and binary files, and files over 512 KiB. " +
		"Falls back to built-in search when rg is not installed. Prefer a narrow path and include glob for large repositories. Max 100 matches by default."
}

func (SearchCode) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Text or regex pattern to find (literal unless regex:true).",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory to search under, absolute or relative. Defaults to the current working directory.",
			},
			"include": map[string]any{
				"type":        "string",
				"description": "Optional file glob filter, e.g. \"*.go\". Defaults to all files.",
			},
			"regex": map[string]any{
				"type":        "boolean",
				"description": "Treat pattern as a regex. Defaults to literal search.",
			},
			"case_insensitive": map[string]any{
				"type":        "boolean",
				"description": "Case-insensitive search. Defaults to case-sensitive.",
			},
			"hidden": map[string]any{
				"type":        "boolean",
				"description": "Search hidden files too. .git is always skipped. Defaults to false.",
			},
			"max_results": map[string]any{
				"type":        "number",
				"description": "Maximum matches to report (1-200). Defaults to 100.",
			},
			"timeout_seconds": map[string]any{
				"type":        "number",
				"description": "Maximum seconds to allow the search to run before it is killed. Defaults to 30.",
			},
		},
		"required": []string{"pattern"},
	}
}

// Metadata advertises search_code's execution characteristics to the
// scheduler. Search is read-only but not retryable by default: a second
// attempt costs a second full traversal.
func (s *SearchCode) Metadata() tools.Metadata {
	return tools.Metadata{
		Timeout:              s.resolveLimits().Timeout,
		SupportsCancellation: true,
		SupportsParallel:     true,
		Retryable:            false,
	}
}

// searchCodeParams carries validated Execute arguments.
type searchCodeParams struct {
	pattern         string
	rootArg         string
	include         string
	useRegex        bool
	caseInsensitive bool
	hidden          bool
	maxResults      int // 0 means unset; resolved against limits later
	timeout         time.Duration
	hasTimeout      bool
}

func (s SearchCode) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	params, err := parseSearchCodeArgs(args)
	if err != nil {
		return tools.Result{}, err
	}

	root, err := resolveSearchRoot(s.policy, params.rootArg)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot search %s: %v", params.rootArg, err)}, nil
	}
	info, err := os.Stat(root)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot search %s: %v", params.rootArg, err)}, nil
	}
	if !info.IsDir() {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot search %s: not a directory", params.rootArg)}, nil
	}
	// Canonical root for containment checks below.
	resolvedRoot := root
	if eval, err := sandbox.EvalLinks(root); err == nil {
		resolvedRoot = eval
	}

	bounds := s.resolveLimits()
	matchCap := bounds.MaxLines
	if params.maxResults > 0 {
		// An explicit per-call max_results wins within the hard
		// ceiling; the configured default applies when omitted.
		// The byte cap always comes from the resolved limits.
		matchCap = params.maxResults
	}
	timeout := bounds.Timeout
	if params.hasTimeout {
		timeout = tools.ClampTimeout(params.timeout, bounds.Timeout)
	}

	if _, err := rgBinary("rg"); err != nil {
		return s.fallback(ctx, params, matchCap), nil
	}

	return s.runRg(ctx, params, root, resolvedRoot, matchCap, bounds.MaxBytes, timeout)
}

// parseSearchCodeArgs validates the raw argument map. Missing or empty
// pattern, wrong types, out-of-range numbers, and overlong patterns are
// hard ArgumentErrors; a malformed include glob is a soft error at the
// call site so the model can retry with a fixed glob.
func parseSearchCodeArgs(args map[string]any) (searchCodeParams, error) {
	var p searchCodeParams
	pattern, err := tools.StringArg(args, "pattern")
	if err != nil {
		return p, err
	}
	if strings.TrimSpace(pattern) == "" {
		return p, &tools.ArgumentError{Field: "pattern", Reason: "must not be empty"}
	}
	if len(pattern) > maxSearchCodePattern {
		return p, &tools.ArgumentError{Field: "pattern", Reason: fmt.Sprintf("must be at most %d characters", maxSearchCodePattern)}
	}
	p.pattern = pattern

	rootArg, err := tools.OptionalStringArg(args, "path", ".")
	if err != nil {
		return p, err
	}
	p.rootArg = rootArg

	include, err := tools.OptionalStringArg(args, "include", "")
	if err != nil {
		return p, err
	}
	p.include = include

	if p.useRegex, err = optionalBoolArg(args, "regex", false); err != nil {
		return p, err
	}
	if p.caseInsensitive, err = optionalBoolArg(args, "case_insensitive", false); err != nil {
		return p, err
	}
	if p.hidden, err = optionalBoolArg(args, "hidden", false); err != nil {
		return p, err
	}
	if n, set, err := optionalIntArg(args, "max_results", 1, maxSearchCodeResults); err != nil {
		return p, err
	} else if set {
		p.maxResults = n
	}
	if secs, set, err := optionalTimeoutArg(args); err != nil {
		return p, err
	} else if set {
		p.timeout = time.Duration(secs * float64(time.Second))
		p.hasTimeout = true
	}
	return p, nil
}

// optionalBoolArg reads an optional boolean argument. Absent means def.
// Present-but-not-bool is an ArgumentError, never a silent default.
func optionalBoolArg(args map[string]any, key string, def bool) (bool, error) {
	raw, ok := args[key]
	if !ok {
		return def, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return false, &tools.ArgumentError{Field: key, Reason: "must be a boolean"}
	}
	return b, nil
}

// optionalIntArg reads an optional integer-valued number argument in
// [min,max]. It reports whether the key was present.
func optionalIntArg(args map[string]any, key string, min, max int) (int, bool, error) {
	raw, ok := args[key]
	if !ok {
		return 0, false, nil
	}
	f, ok := toFloat64(raw)
	if !ok || f != math.Trunc(f) {
		return 0, false, &tools.ArgumentError{Field: key, Reason: fmt.Sprintf("must be an integer between %d and %d", min, max)}
	}
	n := int(f)
	if n < min || n > max {
		return 0, false, &tools.ArgumentError{Field: key, Reason: fmt.Sprintf("must be between %d and %d", min, max)}
	}
	return n, true, nil
}

// optionalTimeoutArg reads an optional timeout_seconds number argument
// in (0, MaxTimeout]. It reports whether the key was present.
func optionalTimeoutArg(args map[string]any) (float64, bool, error) {
	raw, ok := args["timeout_seconds"]
	if !ok {
		return 0, false, nil
	}
	secs, ok := toFloat64(raw)
	if !ok || secs <= 0 {
		return 0, false, &tools.ArgumentError{Field: "timeout_seconds", Reason: "must be a positive number of seconds"}
	}
	if secs > float64(tools.MaxTimeout/time.Second) {
		return 0, false, &tools.ArgumentError{Field: "timeout_seconds", Reason: fmt.Sprintf("must be at most %d seconds", int(tools.MaxTimeout/time.Second))}
	}
	return secs, true, nil
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// buildRgArgv assembles the fixed ripgrep invocation for validated
// params. Every flag is a constant chosen by the tool; the model only
// influences the pattern (via -e), the include glob, and booleans that
// map to constants. Paths always travel behind "--".
func buildRgArgv(pattern, include string, useRegex, caseInsensitive, hidden bool) []string {
	argv := []string{
		"--vimgrep",
		"--color", "never",
		"--path-separator", "/",
		// Never follow symlinks: the workspace boundary is enforced
		// Go-side, and followed links could escape the root.
		// (ripgrep's default; stated explicitly in spirit via the
		// containment check below rather than a version-sensitive
		// negation flag.)
	}
	if !useRegex {
		argv = append(argv, "-F")
	}
	if caseInsensitive {
		argv = append(argv, "-i")
	}
	if hidden {
		argv = append(argv, "--hidden")
	}
	for _, g := range rgExclusions {
		argv = append(argv, "--glob", g)
	}
	// Mirror the built-in walker's per-file size bound.
	argv = append(argv, "--max-filesize", fmt.Sprintf("%dK", maxFileBytes>>10))
	if include != "" {
		argv = append(argv, "--glob", include)
	}
	// -e keeps a leading-dash pattern as data; "--" keeps the path as data.
	argv = append(argv, "-e", pattern, "--", ".")
	return argv
}

// rgEnv returns the child environment: the host environment minus any
// RIPGREP_CONFIG* variables, so a user config file cannot inject flags
// (like --follow or --no-ignore) that would undo the fixed argv above.
// rg still reads HOME/SystemRoot for ignore-file handling.
func rgEnv() []string {
	env := os.Environ()
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, "RIPGREP_CONFIG") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// rgMatch is one parsed --vimgrep line: path:line:column:text.
type rgMatch struct {
	path string
	line int
	col  int
	text string
}

// parseVimgrepLine parses one rg --vimgrep output line. Binary-match
// notices and other non-conforming lines report ok=false and are
// dropped by the caller.
func parseVimgrepLine(raw string) (m rgMatch, ok bool) {
	line := strings.TrimRight(raw, "\r\n")
	first := strings.IndexByte(line, ':')
	if first <= 0 {
		return m, false
	}
	rest := line[first+1:]
	second := strings.IndexByte(rest, ':')
	if second <= 0 {
		return m, false
	}
	third := strings.IndexByte(rest[second+1:], ':')
	if third <= 0 {
		return m, false
	}
	var lineNo, colNo int
	if _, err := fmt.Sscanf(rest[:second], "%d", &lineNo); err != nil || lineNo < 1 {
		return m, false
	}
	if _, err := fmt.Sscanf(rest[second+1:second+1+third], "%d", &colNo); err != nil || colNo < 1 {
		return m, false
	}
	m = rgMatch{
		path: line[:first],
		line: lineNo,
		col:  colNo,
		text: rest[second+1+third+1:],
	}
	if m.path == "" {
		return m, false
	}
	return m, true
}

// runRg executes the fixed rg argv under root and formats bounded,
// workspace-relative results.
func (s SearchCode) runRg(ctx context.Context, params searchCodeParams, root, resolvedRoot string, matchCap, maxBytes int, timeout time.Duration) (tools.Result, error) {
	started := time.Now()
	if params.include != "" {
		if _, err := filepath.Match(params.include, ""); err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("invalid glob %q: %v", params.include, err)}, nil
		}
	}
	rg, err := rgBinary("rg")
	if err != nil {
		return s.fallback(ctx, params, matchCap), nil
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	argv := buildRgArgv(params.pattern, params.include, params.useRegex, params.caseInsensitive, params.hidden)
	cmd := exec.CommandContext(runCtx, rg, argv...)
	// Scope output, not just intent: rg runs with the resolved root as
	// its working directory over ".", so reported paths are root-relative.
	cmd.Dir = root
	cmd.Env = rgEnv()
	// Never inherit surprising fds.
	cmd.Stdin = nil
	// Own the process tree exactly like the shell tool: its own group on
	// Unix, a kill-on-close job object on Windows, so timeout and
	// cancellation reap the whole tree.
	process.Configure(cmd)
	cmd.Cancel = func() error { return process.Kill(cmd) }
	cmd.WaitDelay = searchCodeWaitDelay
	stdout := &rgCappedWriter{max: maxBytes}
	stderr := &rgCappedWriter{max: searchCodeStderrMax}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	release := process.Track(cmd)
	defer release()

	runErr := cmd.Run()
	durationMs := time.Since(started).Milliseconds()

	if runCtx.Err() == context.DeadlineExceeded {
		return tools.Result{IsError: true, Content: fmt.Sprintf("search timed out after %s", timeout), DurationMs: durationMs}, nil
	}
	if runCtx.Err() == context.Canceled || ctx.Err() != nil {
		return tools.Result{IsError: true, Content: "search cancelled", DurationMs: durationMs}, nil
	}

	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			switch exitErr.ExitCode() {
			case 1:
				// ripgrep's "no matches" is a success shape for the
				// model, not an error.
				return tools.Result{Content: fmt.Sprintf("no matches for %q under %s", params.pattern, params.rootArg), DurationMs: durationMs}, nil
			case 2:
				tail := firstLine(stderr.String())
				if params.useRegex {
					return tools.Result{IsError: true, Content: fmt.Sprintf("invalid regex %q: %s", params.pattern, tail), DurationMs: durationMs}, nil
				}
				return tools.Result{IsError: true, Content: fmt.Sprintf("rg search failed: %s", tail), DurationMs: durationMs}, nil
			}
		}
		return tools.Result{IsError: true, Content: fmt.Sprintf("rg search failed: %v", runErr), DurationMs: durationMs}, nil
	}

	out := strings.TrimRight(stdout.String(), "\n")
	var matches []rgMatch
	if out != "" {
		for _, raw := range strings.Split(out, "\n") {
			m, ok := parseVimgrepLine(raw)
			if !ok {
				continue
			}
			matches = append(matches, m)
		}
	}

	kept := filterRgMatches(matches, root, resolvedRoot)
	// Deterministic output regardless of rg's traversal order.
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].path != kept[j].path {
			return kept[i].path < kept[j].path
		}
		if kept[i].line != kept[j].line {
			return kept[i].line < kept[j].line
		}
		return kept[i].col < kept[j].col
	})

	truncated := false
	if len(kept) > matchCap {
		kept = kept[:matchCap]
		truncated = true
	}
	if len(kept) == 0 {
		note := ""
		if stdout.cut {
			note = " (output exceeded the byte limit)"
		}
		return tools.Result{Content: fmt.Sprintf("no matches for %q under %s%s", params.pattern, params.rootArg, note), DurationMs: durationMs}, nil
	}

	var b strings.Builder
	for _, m := range kept {
		text := m.text
		if len(text) > maxLineBytes {
			text = text[:maxLineBytes] + "…[line truncated]"
		}
		fmt.Fprintf(&b, "%s:%d:%d: %s\n", m.path, m.line, m.col, text)
	}
	content := strings.TrimRight(b.String(), "\n")
	var meta map[string]any
	if truncated || stdout.cut {
		meta = map[string]any{
			"truncated": true, "matches": len(kept),
			"match_limit": matchCap, "rg_used": true,
		}
		if truncated {
			content += fmt.Sprintf("\n\n[output truncated at %d matches; narrow pattern, path, or include glob]", matchCap)
		}
		if stdout.cut {
			tMeta := tools.Truncation{Truncated: true, OriginalBytes: stdout.total, KeptBytes: len(stdout.buf), Limit: maxBytes}.Fields()
			for k, v := range tMeta {
				meta[k] = v
			}
			content += fmt.Sprintf("\n\n[...rg output truncated at %d bytes; narrow pattern, path, or include glob]", maxBytes)
		}
	}
	return tools.Result{Content: content, Metadata: meta, DurationMs: durationMs}, nil
}

// filterRgMatches drops anything that must never reach the model:
// unresolvable paths, symlink escapes outside the root, and sensitive
// files. rg runs with its default no-follow behavior over a caged
// root, so drops should be rare; the check is defense-in-depth
// mirroring the built-in walker's containment.
func filterRgMatches(in []rgMatch, root, resolvedRoot string) []rgMatch {
	kept := in[:0:0]
	for _, m := range in {
		rel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimPrefix(m.path, "./"))))
		if rel == "" || rel == "." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		resolved := abs
		if eval, err := sandbox.EvalLinks(abs); err == nil {
			resolved = eval
		} else if _, err := os.Lstat(abs); err != nil {
			continue // vanished mid-search; skip
		}
		if !within(resolvedRoot, resolved) {
			continue
		}
		if filesystem.IsSensitivePath(abs) || filesystem.IsSensitivePath(resolved) || filesystem.IsSensitivePath(rel) {
			continue
		}
		m.path = rel
		kept = append(kept, m)
	}
	return kept
}

// fallback runs the built-in search_files walker with the equivalent
// arguments when rg is not installed. The tool never hard-fails for a
// missing dependency; the note tells the model why columns are absent.
// The hidden flag needs no mapping: the walker already visits hidden
// files (outside the fixed exclusion set), consistent with list_files.
func (s SearchCode) fallback(ctx context.Context, params searchCodeParams, matchCap int) tools.Result {
	fb := NewSearchFilesWithPolicy(s.policy)
	fb.SetLimits(tools.Limits{MaxLines: matchCap})
	boolFlag := func(b bool) string {
		if b {
			return "true"
		}
		return ""
	}
	res, err := fb.Execute(ctx, map[string]any{
		"pattern":          params.pattern,
		"path":             params.rootArg,
		"include":          params.include,
		"regex":            boolFlag(params.useRegex),
		"case_insensitive": boolFlag(params.caseInsensitive),
	})
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("[note: rg not found on PATH; used built-in search]\n%v", err)}
	}
	meta := map[string]any{"rg_used": false, "fallback": true}
	for k, v := range res.Metadata {
		meta[k] = v
	}
	// The fallback walker has no columns; keep its path:line shape and
	// only annotate the provenance.
	content := "[note: rg not found on PATH; used built-in search]\n" + res.Content
	return tools.Result{Content: content, IsError: res.IsError, Metadata: meta}
}

// rgCappedWriter keeps the first max bytes and counts everything seen,
// discarding the rest, so producers never block and memory stays
// bounded while truncation metadata stays exact.
type rgCappedWriter struct {
	buf   []byte
	max   int
	total int
	cut   bool
}

func (w *rgCappedWriter) Write(p []byte) (int, error) {
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

func (w *rgCappedWriter) String() string { return string(w.buf) }

// firstLine returns the first line of s for compact error tails.
// Empty input reports the input unchanged.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
