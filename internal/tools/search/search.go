// Package search provides the search_files tool: bounded literal/regex
// content search under a directory.
//
// Security model mirrors read_file/list_files: in confined modes the
// search root is caged via sandbox.ResolveWithinWorkspace; in permissive
// native mode the root is anchored to the process cwd. On top of that (both
// modes, because a recursive walker is a stronger primitive than a
// single-file read), every visited file is symlink-resolved and required
// to stay within the resolved root, sensitive files (see
// filesystem.IsSensitivePath) are skipped, and the .git subtree is
// skipped. Output, scope, and per-file size are all bounded.
package search

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
	"forcefield/internal/tools/filesystem"
)

const (
	// maxFilesVisited bounds the walk scope so a huge tree cannot spin
	// the tool (and the model turn) indefinitely.
	maxFilesVisited = 2000
	// maxFileBytes bounds how much of a single file is scanned. Larger
	// files are skipped with a note, not read partially and silently.
	maxFileBytes = 512 << 10 // 512 KiB
	// maxLineBytes caps a single reported line so a minified giant line
	// cannot flood the context.
	maxLineBytes = 2000
	// sniffBytes bounds the binary-detection read at the head of each
	// visited file. A NUL byte inside means binary: skipped with a note.
	sniffBytes = 8000
)

// excludedDirNames are never descended into: version-control metadata,
// dependency trees, and build output are noise for content search and
// huge enough to wedge a walk. The set is fixed so search behavior stays
// predictable; hidden files and directories outside this set are still
// visited, consistent with list_files.
var excludedDirNames = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"dist":         {},
	"build":        {},
	"target":       {},
	"vendor":       {},
	".next":        {},
	"__pycache__":  {},
}

// isExcludedDir reports whether a directory entry must be pruned.
func isExcludedDir(name string) bool {
	_, ok := excludedDirNames[name]
	return ok
}

// isLockFile reports whether name is a dependency lockfile. Lockfiles
// are machine-generated single-line blobs: matching inside them is
// noise, and they routinely exceed the per-file size cap anyway.
func isLockFile(name string) bool {
	matched, err := filepath.Match("*.lock", name)
	return err == nil && matched
}

// SearchFiles searches file contents under a directory.
type SearchFiles struct {
	policy sandbox.Policy
	// limits overrides the match bound. Zero values resolve via
	// tools.DefaultLimitsFor("search_files").
	limits tools.Limits
}

// NewSearchFiles returns a ready-to-register SearchFiles tool.
func NewSearchFiles() *SearchFiles { return &SearchFiles{} }

// NewSearchFilesWithPolicy returns a SearchFiles confined to
// policy.Workspace when policy.Mode is wsl; otherwise native behavior.
func NewSearchFilesWithPolicy(p sandbox.Policy) *SearchFiles { return &SearchFiles{policy: p} }

// SetLimits overrides the match bound. Only positive fields take effect;
// the rest resolve to the tool defaults.
func (s *SearchFiles) SetLimits(l tools.Limits) {
	s.limits = l
}

// ToolLimits reports the resolved bounds.
func (s *SearchFiles) ToolLimits() tools.Limits {
	return s.resolveLimits()
}

func (s *SearchFiles) resolveLimits() tools.Limits {
	if s == nil {
		return tools.DefaultLimitsFor("search_files")
	}
	return s.limits.WithDefaults(tools.DefaultLimitsFor("search_files"))
}

func (SearchFiles) Name() string { return "search_files" }

func (SearchFiles) Description() string {
	return "Search file contents under a directory for a literal string (or regex with regex:true). " +
		"Returns path:line matches. Skips .git, node_modules, dist, build, target, vendor, .next, __pycache__, *.lock, sensitive, and binary files, and files over 512 KiB. Max 100 matches."
}

func (SearchFiles) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Literal substring to find (or regex when regex:true).",
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
				"type":        "string",
				"description": "Optional: \"true\" to treat pattern as a Go regexp. Defaults to literal search.",
			},
			"case_insensitive": map[string]any{
				"type":        "string",
				"description": "Optional: \"true\" for case-insensitive literal search. Ignored when regex:true.",
			},
		},
		"required": []string{"pattern"},
	}
}

func (s SearchFiles) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	pattern, err := tools.StringArg(args, "pattern")
	if err != nil {
		return tools.Result{}, err
	}
	if strings.TrimSpace(pattern) == "" {
		return tools.Result{}, fmt.Errorf("search_files: pattern cannot be empty")
	}
	rootArg, err := tools.OptionalStringArg(args, "path", ".")
	if err != nil {
		return tools.Result{}, err
	}
	include, err := tools.OptionalStringArg(args, "include", "")
	if err != nil {
		return tools.Result{}, err
	}
	regexFlag, err := tools.OptionalStringArg(args, "regex", "")
	if err != nil {
		return tools.Result{}, err
	}
	ciFlag, err := tools.OptionalStringArg(args, "case_insensitive", "")
	if err != nil {
		return tools.Result{}, err
	}

	useRegex := strings.EqualFold(strings.TrimSpace(regexFlag), "true")
	caseInsensitive := strings.EqualFold(strings.TrimSpace(ciFlag), "true")

	var re *regexp.Regexp
	var needle string
	if useRegex {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("invalid regex %q: %v", pattern, err)}, nil
		}
	} else {
		needle = pattern
		if caseInsensitive {
			needle = strings.ToLower(pattern)
		}
	}

	root, err := s.resolveRoot(rootArg)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot search %s: %v", rootArg, err)}, nil
	}
	info, err := os.Stat(root)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot search %s: %v", rootArg, err)}, nil
	}
	if !info.IsDir() {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot search %s: not a directory", rootArg)}, nil
	}
	// Canonical root for containment checks below.
	resolvedRoot := root
	if eval, err := sandbox.EvalLinks(root); err == nil {
		resolvedRoot = eval
	}

	type match struct {
		path string
		line int
		text string
	}
	var matches []match
	filesVisited := 0
	skippedLarge := 0
	skippedBinary := 0
	truncated := false
	// matchCap bounds reported matches; it resolves from the shared
	// limits table (default 100) so configured max_lines overrides apply.
	matchCap := s.resolveLimits().MaxLines

	matchLine := func(line string) bool {
		if useRegex {
			return re.MatchString(line)
		}
		if caseInsensitive {
			return strings.Contains(strings.ToLower(line), needle)
		}
		return strings.Contains(line, needle)
	}

	scanFile := func(abs string, rel string) error {
		fi, err := os.Stat(abs)
		if err != nil {
			return nil // vanished mid-walk; skip
		}
		if fi.Size() > maxFileBytes {
			skippedLarge++
			return nil
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil // unreadable; skip
		}
		defer f.Close()
		if isBinary(f) {
			skippedBinary++
			return nil
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil // cannot rewind after sniffing; skip
		}
		r := bufio.NewReader(io.LimitReader(f, maxFileBytes+1))
		lineNo := 0
		for {
			raw, err := r.ReadString('\n')
			lineNo++
			text := strings.TrimRight(raw, "\r\n")
			if matchLine(text) {
				if len(text) > maxLineBytes {
					text = text[:maxLineBytes] + "…[line truncated]"
				}
				matches = append(matches, match{path: rel, line: lineNo, text: text})
				if len(matches) >= matchCap {
					truncated = true
					return errStop
				}
			}
			if err != nil {
				break
			}
		}
		return nil
	}

	walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, keep walking
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Never descend into excluded directories (version control,
		// dependencies, build output): noise for content search and big
		// enough to wedge a walk.
		if d.IsDir() && isExcludedDir(d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		// Lockfiles are machine-generated blobs; matching inside them is
		// noise (and they routinely exceed the per-file size cap anyway).
		if isLockFile(d.Name()) {
			return nil
		}
		// Never follow symlinked directories (WalkDir doesn't), and
		// require symlinked files to resolve inside the root.
		if d.Type()&os.ModeSymlink != 0 {
			resolved, err := sandbox.EvalLinks(p)
			if err != nil {
				return nil
			}
			if !within(resolvedRoot, resolved) {
				return nil
			}
			if fi, err := os.Stat(resolved); err != nil || fi.IsDir() {
				return nil
			}
		}
		// Containment: resolve (links and junctions) and require within
		// root (both modes).
		resolved := p
		if eval, err := sandbox.EvalLinks(p); err == nil {
			resolved = eval
		}
		if !within(resolvedRoot, resolved) {
			return nil
		}
		// Skip sensitive files (credentials, keys) — search must not
		// become a secret exfiltration primitive.
		if filesystem.IsSensitivePath(p) || filesystem.IsSensitivePath(resolved) {
			return nil
		}
		if include != "" {
			ok, err := filepath.Match(include, d.Name())
			if err != nil || !ok {
				return nil
			}
		}
		filesVisited++
		if filesVisited > maxFilesVisited {
			truncated = true
			return errStop
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			rel = p
		}
		return scanFile(p, filepath.ToSlash(rel))
	})
	if walkErr != nil && walkErr != errStop && walkErr != context.Canceled && walkErr != context.DeadlineExceeded {
		return tools.Result{IsError: true, Content: fmt.Sprintf("search %s failed: %v", rootArg, walkErr)}, nil
	}
	if ctx.Err() != nil {
		return tools.Result{IsError: true, Content: "search cancelled"}, nil
	}

	if len(matches) == 0 {
		note := ""
		if skippedLarge > 0 {
			note = fmt.Sprintf(" (%d large files skipped)", skippedLarge)
		}
		if skippedBinary > 0 {
			note += fmt.Sprintf(" (%d binary files skipped)", skippedBinary)
		}
		return tools.Result{Content: fmt.Sprintf("no matches for %q under %s%s", pattern, rootArg, note)}, nil
	}

	var b strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&b, "%s:%d: %s\n", m.path, m.line, m.text)
	}
	out := strings.TrimRight(b.String(), "\n")
	var meta map[string]any
	if truncated {
		out += fmt.Sprintf("\n\n[output truncated at %d matches / %d files visited; narrow pattern, path, or include glob]", matchCap, maxFilesVisited)
		meta = map[string]any{
			"truncated": true, "matches": len(matches),
			"match_limit": matchCap, "files_visited": filesVisited,
		}
	} else {
		if skippedLarge > 0 {
			out += fmt.Sprintf("\n\n[%d files over 512 KiB skipped]", skippedLarge)
		}
		if skippedBinary > 0 {
			out += fmt.Sprintf("\n\n[%d binary files skipped]", skippedBinary)
		}
	}
	return tools.Result{Content: out, Metadata: meta}, nil
}

// errStop is a sentinel to abort the walk on caps without reporting failure.
var errStop = fmt.Errorf("search limits reached")

// isBinary sniffs the head of an open file for NUL bytes. It reads at
// most sniffBytes and leaves the offset for the caller to rewind.
func isBinary(f *os.File) bool {
	head := make([]byte, sniffBytes)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return false // unreadable head: let the scanner decide
	}
	return bytes.IndexByte(head[:n], 0) >= 0
}

// resolveRoot confines the search root exactly like read_file/list_files:
// confined modes cage to the workspace; permissive native anchors to the cwd.
func (s SearchFiles) resolveRoot(rootArg string) (string, error) {
	return resolveSearchRoot(s.policy, rootArg)
}

// resolveSearchRoot is the shared root resolver for search_files and
// find_files, so both tools enforce the identical workspace boundary:
// confined modes (wsl, or native with strict) cage to the workspace,
// permissive native anchors to the process cwd. One function, no
// divergent path semantics.
func resolveSearchRoot(policy sandbox.Policy, rootArg string) (string, error) {
	if policy.Confines() {
		return sandbox.ResolveWithinWorkspace(policy.Workspace, rootArg)
	}
	if strings.TrimSpace(rootArg) == "" || rootArg == "." {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return wd, nil
	}
	if filepath.IsAbs(rootArg) {
		return filepath.Clean(rootArg), nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, filepath.Clean(rootArg)), nil
}

// within reports whether path equals root or lies underneath it.
func within(root, path string) bool {
	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(path)
	if cleanPath == cleanRoot {
		return true
	}
	sep := string(os.PathSeparator)
	prefix := cleanRoot
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(cleanPath, prefix)
}
