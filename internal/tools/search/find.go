// Package search provides file discovery tools: search_files for
// bounded content search and find_files for bounded filename/glob
// discovery. Both share the workspace boundary (resolveSearchRoot),
// directory exclusions, symlink containment, sensitive-file skipping,
// and traversal/output caps, so neither can become an unbounded walk
// or escape the workspace in strict mode.
package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
	"forcefield/internal/tools/filesystem"
)

// FindFiles discovers filenames under a directory by glob or substring.
type FindFiles struct {
	policy sandbox.Policy
	// limits overrides the result bound. Zero values resolve via
	// tools.DefaultLimitsFor("find_files").
	limits tools.Limits
}

// NewFindFiles returns a ready-to-register FindFiles tool.
func NewFindFiles() *FindFiles { return &FindFiles{} }

// NewFindFilesWithPolicy returns a FindFiles confined to
// policy.Workspace when policy.Mode is wsl; otherwise native behavior.
func NewFindFilesWithPolicy(p sandbox.Policy) *FindFiles { return &FindFiles{policy: p} }

// SetLimits overrides the result bound. Only positive fields take
// effect; the rest resolve to the tool defaults.
func (s *FindFiles) SetLimits(l tools.Limits) {
	s.limits = l
}

// ToolLimits reports the resolved bounds.
func (s *FindFiles) ToolLimits() tools.Limits {
	return s.resolveLimits()
}

func (s *FindFiles) resolveLimits() tools.Limits {
	if s == nil {
		return tools.DefaultLimitsFor("find_files")
	}
	return s.limits.WithDefaults(tools.DefaultLimitsFor("find_files"))
}

func (FindFiles) Name() string { return "find_files" }

func (FindFiles) Description() string {
	return "Find files and directories under a directory by filename glob (e.g. \"*.go\") or substring. " +
		"Returns workspace-relative paths, sorted, one per line (directories end with /). " +
		"Skips excluded directories (.git, node_modules, dist, build, target, vendor, .next, __pycache__) and sensitive files. Max 50 results."
}

func (FindFiles) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Filename glob (e.g. \"*.go\", \"config.*\") or plain substring matched against file names. A pattern containing \"/\" matches against the workspace-relative path instead.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory to search under, absolute or relative. Defaults to the current working directory.",
			},
		},
		"required": []string{"pattern"},
	}
}

// hasGlobMeta reports whether pattern uses glob metacharacters.
func hasGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[\\")
}

func (s FindFiles) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	pattern, err := tools.StringArg(args, "pattern")
	if err != nil {
		return tools.Result{}, err
	}
	if strings.TrimSpace(pattern) == "" {
		return tools.Result{}, fmt.Errorf("find_files: pattern cannot be empty")
	}
	rootArg, err := tools.OptionalStringArg(args, "path", ".")
	if err != nil {
		return tools.Result{}, err
	}
	// A malformed glob would otherwise match nothing and look like an
	// empty directory: fail soft with a clear message instead.
	if hasGlobMeta(pattern) {
		if _, err := filepath.Match(pattern, ""); err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("invalid glob %q: %v", pattern, err)}, nil
		}
	}

	root, err := resolveSearchRoot(s.policy, rootArg)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot find under %s: %v", rootArg, err)}, nil
	}
	info, err := os.Stat(root)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot find under %s: %v", rootArg, err)}, nil
	}
	if !info.IsDir() {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot find under %s: not a directory", rootArg)}, nil
	}
	// Canonical root for containment checks below.
	resolvedRoot := root
	if eval, err := sandbox.EvalLinks(root); err == nil {
		resolvedRoot = eval
	}

	// resultCap bounds reported paths; it resolves from the shared
	// limits table (default 50) so configured max_lines overrides apply.
	resultCap := s.resolveLimits().MaxLines
	// Lockfile contents are noise for content search, but a filename
	// query naming lockfiles is explicit: honor it.
	allowLock := strings.Contains(strings.ToLower(pattern), "lock")

	matchName := func(name, rel string) bool {
		if strings.Contains(pattern, "/") {
			ok, err := filepath.Match(pattern, filepath.ToSlash(rel))
			return err == nil && ok
		}
		if hasGlobMeta(pattern) {
			ok, err := filepath.Match(pattern, name)
			return err == nil && ok
		}
		return strings.Contains(name, pattern)
	}

	var found []string
	filesVisited := 0
	truncated := false

	walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, keep walking
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Never descend into excluded directories.
		if d.IsDir() && isExcludedDir(d.Name()) {
			return filepath.SkipDir
		}
		if p == root {
			return nil // the root itself is never a result
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
		// Containment: resolve and require within root (both modes).
		resolved := p
		if eval, err := sandbox.EvalLinks(p); err == nil {
			resolved = eval
		}
		if !within(resolvedRoot, resolved) {
			return nil
		}
		// Skip sensitive files — discovery must not become a secret
		// exfiltration primitive.
		if filesystem.IsSensitivePath(p) || filesystem.IsSensitivePath(resolved) {
			return nil
		}
		if !allowLock && !d.IsDir() && isLockFile(d.Name()) {
			return nil
		}
		if !d.IsDir() {
			filesVisited++
			if filesVisited > maxFilesVisited {
				truncated = true
				return errStop
			}
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			rel = p
		}
		rel = filepath.ToSlash(rel)
		name := d.Name()
		if !matchName(name, rel) {
			return nil
		}
		if d.IsDir() {
			rel += "/"
		}
		found = append(found, rel)
		return nil
	})
	if walkErr != nil && walkErr != errStop && walkErr != context.Canceled && walkErr != context.DeadlineExceeded {
		return tools.Result{IsError: true, Content: fmt.Sprintf("find under %s failed: %v", rootArg, walkErr)}, nil
	}
	if ctx.Err() != nil {
		return tools.Result{IsError: true, Content: "find cancelled"}, nil
	}

	// Deterministic output regardless of walk order.
	sort.Strings(found)
	if len(found) == 0 {
		return tools.Result{Content: fmt.Sprintf("no files matching %q under %s", pattern, rootArg)}, nil
	}
	total := len(found)
	if total > resultCap {
		found = found[:resultCap]
		truncated = true
	}
	out := strings.Join(found, "\n")
	var meta map[string]any
	if truncated {
		out += fmt.Sprintf("\n\n[output truncated at %d matches; narrow pattern or path]", resultCap)
		meta = map[string]any{
			"truncated": true, "matches": len(found),
			"match_limit": resultCap, "files_visited": filesVisited,
		}
	}
	return tools.Result{Content: out, Metadata: meta}, nil
}
