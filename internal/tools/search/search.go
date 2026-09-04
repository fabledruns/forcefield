// Package search provides the search_files tool: bounded literal/regex
// content search under a directory.
//
// Security model mirrors read_file/list_files: in WSL sandbox mode the
// search root is confined via sandbox.ResolveWithinWorkspace; in native
// mode the root is anchored to the process cwd. On top of that (both
// modes, because a recursive walker is a stronger primitive than a
// single-file read), every visited file is symlink-resolved and required
// to stay within the resolved root, sensitive files (see
// filesystem.IsSensitivePath) are skipped, and the .git subtree is
// skipped. Output, scope, and per-file size are all bounded.
package search

import (
	"bufio"
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
	// maxMatches bounds the reported matches; output never grows past this.
	maxMatches = 100
	// maxLineBytes caps a single reported line so a minified giant line
	// cannot flood the context.
	maxLineBytes = 2000
)

// SearchFiles searches file contents under a directory.
type SearchFiles struct {
	policy sandbox.Policy
}

// NewSearchFiles returns a ready-to-register SearchFiles tool.
func NewSearchFiles() *SearchFiles { return &SearchFiles{} }

// NewSearchFilesWithPolicy returns a SearchFiles confined to
// policy.Workspace when policy.Mode is wsl; otherwise native behavior.
func NewSearchFilesWithPolicy(p sandbox.Policy) *SearchFiles { return &SearchFiles{policy: p} }

func (SearchFiles) Name() string { return "search_files" }

func (SearchFiles) Description() string {
	return "Search file contents under a directory for a literal string (or regex with regex:true). " +
		"Returns path:line matches. Skips .git, sensitive files, and files over 512 KiB. Max 100 matches."
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
	if eval, err := filepath.EvalSymlinks(root); err == nil {
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
	truncated := false

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
				if len(matches) >= maxMatches {
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
		// Never descend into .git (history blobs are noise and huge).
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		// Never follow symlinked directories (WalkDir doesn't), and
		// require symlinked files to resolve inside the root.
		if d.Type()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(p)
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
		// Containment: resolve and require within root (both modes).
		resolved := p
		if eval, err := filepath.EvalSymlinks(p); err == nil {
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
		return tools.Result{Content: fmt.Sprintf("no matches for %q under %s%s", pattern, rootArg, note)}, nil
	}

	var b strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&b, "%s:%d: %s\n", m.path, m.line, m.text)
	}
	out := strings.TrimRight(b.String(), "\n")
	if truncated {
		out += fmt.Sprintf("\n\n[output truncated at %d matches / %d files visited; narrow pattern, path, or include glob]", maxMatches, maxFilesVisited)
	} else if skippedLarge > 0 {
		out += fmt.Sprintf("\n\n[%d files over 512 KiB skipped]", skippedLarge)
	}
	return tools.Result{Content: out}, nil
}

// errStop is a sentinel to abort the walk on caps without reporting failure.
var errStop = fmt.Errorf("search limits reached")

// resolveRoot confines the search root exactly like read_file/list_files:
// WSL mode cages to the workspace; native mode anchors to the cwd.
func (s SearchFiles) resolveRoot(rootArg string) (string, error) {
	if s.policy.Mode == sandbox.ModeWSL {
		return sandbox.ResolveWithinWorkspace(s.policy.Workspace, rootArg)
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
