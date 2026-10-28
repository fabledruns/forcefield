// search_code runs host rg directly (argv-built, no shell) with the same
// workspace cage as search_files, plus fallback. See docs/Tools.md.
package search

import (
	"context"
	"encoding/json"
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

// search_code truncation strings. Gating: a fired capture bound
// (stdout.cut) forces byte-truth summaries (S2) and tail (T2) because
// rg's begin/end byte stream was capped and parsed totals can no longer
// be trusted; otherwise row-capped runs report totals from the parsed
// rows (S1/T1). Summaries lead the content and the tail note closes it,
// so a later runtime head-window truncation cannot erase either.
const (
	// rgSummaryRowCapFmt (S1) opens row-capped output when the capture
	// bound never fired: args are matches kept and total parsed matches.
	rgSummaryRowCapFmt = "[search truncated: showing first %d of %d matches; narrow pattern, path, or include glob]\n"
	// rgSummaryCutFmt (S2) opens output whenever the capture bound
	// fired — matches or none: args are the byte cap, bytes produced,
	// and matches shown so far. Never claims a total.
	rgSummaryCutFmt = "[search truncated: rg output capped at %d bytes, %d bytes produced; %d matches shown so far; more may exist; narrow pattern, path, or include glob]\n"
	// rgTailRowCapFmt (T1) closes row-capped output that was not cut.
	rgTailRowCapFmt = "[output truncated: showing %d of %d matches; narrow pattern, path, or include glob]"
	// rgTailCutFmt (T2) closes output the capture bound fired on.
	rgTailCutFmt = "[...rg output truncated at %d bytes, %d bytes produced; narrow pattern, path, or include glob]"
	// searchCodeZeroNote states the per-file size policy on every
	// zero-row result and points at read_file for content the search
	// skipped by construction.
	searchCodeZeroNote = " (files over 512 KiB are not searched; if a match may be inside one, use read_file)"
	// rgZeroCutNote (Z3 second line) joins a zero-row rg exit 1 whose
	// begin/end byte stream was consumed by the capture bound: the
	// search itself ran to completion, only the report was capped.
	rgZeroCutNote = "\n[search truncated: rg output capped at %d bytes, %d bytes produced; the search itself ran to completion; narrow pattern, path, or include glob]"
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

// NewSearchCode returns a ready-to-register SearchCode tool. The search
// root is always confined to the workspace root (the policy's
// Workspace, or the process working directory when unset).
func NewSearchCode() *SearchCode { return &SearchCode{} }

// NewSearchCodeWithPolicy returns a SearchCode confined to
// policy.Workspace. It behaves like NewSearchCode: confinement is
// unconditional, and the policy only selects which root to cage to.
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
		// --json reports every submatch with exact byte offsets, so
		// the elision path knows the match end instead of guessing it
		// from the pattern. One event per matching line, one row per
		// submatch below.
		"--json",
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

// rgMatch is one match row: path:line:column:text, plus the match's
// exact byte length within text so the elider knows the match span.
type rgMatch struct {
	path     string
	line     int
	col      int
	matchLen int
	text     string
}

// rgJSONEvent mirrors the one `rg --json` event the tool consumes.
// Match events carry the line text (with its trailing newline as
// lines.text; as base64 in lines.bytes when the content is not valid
// UTF-8) and the byte offsets of every submatch within that line;
// begin, end, and summary events are ignored.
type rgJSONEvent struct {
	Type string `json:"type"`
	Data *struct {
		Path struct {
			Text  string `json:"text"`
			Bytes []byte `json:"bytes"`
		} `json:"path"`
		LineNumber int `json:"line_number"`
		Lines      struct {
			Text  string `json:"text"`
			Bytes []byte `json:"bytes"`
		} `json:"lines"`
		Submatches []struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"submatches"`
	} `json:"data"`
}

// parseRgJSONLine converts one `rg --json` line into one row per
// submatch, each carrying its exact match span. Non-match events and
// malformed lines report nothing, like --vimgrep's unparsable lines
// did.
//
// Submatch offsets are 0-based byte offsets within the \n-stripped
// line content, and they include a CRLF line's trailing \r, so both
// the span check and the reported column stay in that content. When
// the line content is not valid UTF-8, rg omits lines.text and sends
// lines.bytes instead: the base64 encoding of the full line, line
// terminator included. The display text keeps vimgrep parity by
// trimming exactly one trailing \r with TrimSuffix (contrast the
// built-in walker's TrimRight, which strips every trailing \r or \n);
// spans, though, are still validated against the un-\r-trimmed
// content, so a match may legitimately span the trailing carriage
// return. A submatch reaching past the content is dropped rather than
// distorted.
func parseRgJSONLine(raw string) []rgMatch {
	var ev rgJSONEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil || ev.Type != "match" || ev.Data == nil {
		return nil
	}
	path := ev.Data.Path.Text
	if path == "" && ev.Data.Path.Bytes != nil {
		path = string(ev.Data.Path.Bytes)
	}
	content := ""
	if ev.Data.Lines.Text != "" {
		content = strings.TrimSuffix(ev.Data.Lines.Text, "\n")
	} else if ev.Data.Lines.Bytes != nil {
		content = strings.TrimSuffix(string(ev.Data.Lines.Bytes), "\n")
	}
	// Display text (vimgrep parity): a single trailing \r can go,
	// because no submatch is reported inside the trimmed terminator
	// beyond it; the spans below stay in content space.
	text := strings.TrimSuffix(content, "\r")
	if path == "" || ev.Data.LineNumber < 1 {
		return nil
	}
	rows := make([]rgMatch, 0, len(ev.Data.Submatches))
	for _, sm := range ev.Data.Submatches {
		// Validate against content, not text: a submatch covering the
		// trailing \r of a CRLF line is a real byte of the line and
		// must survive (the old check against the \r-trimmed text
		// dropped it and its whole row).
		if sm.Start < 0 || sm.End > len(content) || sm.End < sm.Start {
			continue
		}
		rows = append(rows, rgMatch{
			path:     path,
			line:     ev.Data.LineNumber,
			col:      sm.Start + 1,
			matchLen: sm.End - sm.Start,
			text:     text,
		})
	}
	return rows
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
	// cancellation reap the whole tree. Track runs after Start: before
	// Start there is no process to assign, and the assignment would be a
	// silent no-op on Windows.
	process.Configure(cmd)
	cmd.Cancel = func() error { return process.Kill(cmd) }
	cmd.WaitDelay = searchCodeWaitDelay
	stdout := &rgCappedWriter{max: maxBytes}
	stderr := &rgCappedWriter{max: searchCodeStderrMax}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		durationMs := time.Since(started).Milliseconds()
		if runCtx.Err() == context.DeadlineExceeded {
			return tools.Result{IsError: true, Content: fmt.Sprintf("search timed out after %s", timeout), DurationMs: durationMs}, nil
		}
		if runCtx.Err() == context.Canceled || ctx.Err() != nil {
			return tools.Result{IsError: true, Content: "search cancelled", DurationMs: durationMs}, nil
		}
		return tools.Result{IsError: true, Content: fmt.Sprintf("search failed to start: %v", err), DurationMs: durationMs}, nil
	}
	release := process.Track(cmd)
	defer release()

	runErr := cmd.Wait()
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
				// model, not an error. State the per-file size policy
				// so a match inside a skipped large file is still
				// findable via read_file. When the capture bound fired
				// on a huge tree the begin/end byte stream was lost,
				// so name the cap on a second line — the search itself
				// ran to completion, only the report was capped.
				content := fmt.Sprintf("no matches for %q under %s%s", params.pattern, params.rootArg, searchCodeZeroNote)
				if stdout.cut {
					content += fmt.Sprintf(rgZeroCutNote, maxBytes, stdout.total)
				}
				return tools.Result{Content: content, DurationMs: durationMs}, nil
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
			matches = append(matches, parseRgJSONLine(raw)...)
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

	// Capture the total before the cap so the row-cap summary and tail
	// can state exact parsed totals. totalFound is unreliable only when
	// the capture bound fired — parsed rows are incomplete then, and
	// the gating rule switches to byte truth (S2/T2).
	totalFound := len(kept)
	truncated := false
	if len(kept) > matchCap {
		kept = kept[:matchCap]
		truncated = true
	}
	if len(kept) == 0 {
		content := fmt.Sprintf("no matches for %q under %s%s", params.pattern, params.rootArg, searchCodeZeroNote)
		if stdout.cut {
			// Byte truth first: the capture-bound summary leads even
			// the no-matches sentence, because the cap hid rows this
			// branch can no longer see. Metadata stays nil, as today.
			content = fmt.Sprintf(rgSummaryCutFmt, maxBytes, stdout.total, 0) + content
		}
		return tools.Result{Content: content, DurationMs: durationMs}, nil
	}

	var b strings.Builder
	for _, m := range kept {
		// A long line is elided around the match rather than topped
		// off: rg reported the exact span, so the match is inside the
		// emitted text and the reported column still locates it
		// exactly. A match bigger than the budget keeps its head and
		// marks the remainder; the column still points at the match.
		text := elideLine(m.text, m.col, m.matchLen, maxLineBytes)
		fmt.Fprintf(&b, "%s:%d:%d: %s\n", m.path, m.line, m.col, text)
	}
	content := strings.TrimRight(b.String(), "\n")

	// Summary first, loss last: the runtime guard keeps only a head
	// window plus a small tail of long tool results, so the summary
	// that names the truncation must lead the content to survive it.
	// Byte truth (S2) always wins over parsed totals (S1).
	if stdout.cut {
		content = fmt.Sprintf(rgSummaryCutFmt, maxBytes, stdout.total, len(kept)) + content
	} else if truncated {
		content = fmt.Sprintf(rgSummaryRowCapFmt, len(kept), totalFound) + content
	}

	var meta map[string]any
	if truncated || stdout.cut {
		meta = map[string]any{
			"truncated": true, "matches": len(kept),
			"match_limit": matchCap, "rg_used": true,
		}
		if stdout.cut {
			tMeta := tools.Truncation{Truncated: true, OriginalBytes: stdout.total, KeptBytes: len(stdout.buf), Limit: maxBytes}.Fields()
			for k, v := range tMeta {
				meta[k] = v
			}
			content += "\n\n" + fmt.Sprintf(rgTailCutFmt, maxBytes, stdout.total)
		} else {
			content += "\n\n" + fmt.Sprintf(rgTailRowCapFmt, len(kept), totalFound)
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

// CheckBoundary implements tools.BoundaryChecker: it dry-runs the
// workspace boundary decision for a code search (canonicalize +
// resolve the root, nothing executed) and returns the canonical root
// that would be searched.
func (s SearchCode) CheckBoundary(args map[string]any) (string, error) {
	rootArg, err := tools.OptionalStringArg(args, "path", ".")
	if err != nil {
		return "", err
	}
	return resolveSearchRoot(s.policy, rootArg)
}
