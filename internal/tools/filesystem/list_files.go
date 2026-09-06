package filesystem

import (
	"context"
	"fmt"
	"os"
	"strings"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// ListFiles lists the immediate contents of a directory.
type ListFiles struct {
	policy sandbox.Policy
	// limits overrides the entry bound. Zero values resolve via
	// tools.DefaultLimitsFor("list_files").
	limits tools.Limits
}

// NewListFiles returns a ready-to-register ListFiles tool.
func NewListFiles() *ListFiles { return &ListFiles{} }

// NewListFilesWithPolicy returns a ListFiles confined to policy.Workspace when
// policy.Mode is wsl; otherwise it behaves like NewListFiles (native).
func NewListFilesWithPolicy(p sandbox.Policy) *ListFiles { return &ListFiles{policy: p} }

// SetLimits overrides the entry bound. Only positive fields take effect;
// the rest resolve to the tool defaults.
func (l *ListFiles) SetLimits(lim tools.Limits) {
	l.limits = lim
}

// ToolLimits reports the resolved bounds.
func (l *ListFiles) ToolLimits() tools.Limits {
	return l.resolveLimits()
}

func (l *ListFiles) resolveLimits() tools.Limits {
	if l == nil {
		return tools.DefaultLimitsFor("list_files")
	}
	return l.limits.WithDefaults(tools.DefaultLimitsFor("list_files"))
}

func (ListFiles) Name() string { return "list_files" }

func (ListFiles) Description() string {
	return "List the files and directories directly inside the given path (not recursive). " +
		"Defaults to the current working directory if no path is given."
}

func (ListFiles) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Directory to list, absolute or relative. Defaults to the current working directory.",
			},
		},
	}
}

func (l ListFiles) Execute(_ context.Context, args map[string]any) (tools.Result, error) {
	path, err := tools.OptionalStringArg(args, "path", ".")
	if err != nil {
		return tools.Result{}, err
	}

	resolved := path
	if l.policy.Confines() {
		rp, err := sandbox.ResolveWithinWorkspace(l.policy.Workspace, path)
		if err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot list %s: %v", path, err)}, nil
		}
		resolved = rp
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot list %s: %v", path, err)}, nil
	}

	// TOCTOU mitigation: re-validate after ReadDir. A concurrent writer
	// could have swapped the directory for a symlink to outside between
	// the initial ResolveWithinWorkspace and the ReadDir.
	if l.policy.Confines() {
		if real, err := sandbox.EvalLinks(resolved); err == nil {
			if _, err := sandbox.EnsureWithinWorkspace(l.policy.Workspace, real); err != nil {
				return tools.Result{IsError: true, Content: fmt.Sprintf("cannot list %s: %v", path, err)}, nil
			}
		}
	}

	if len(entries) == 0 {
		return tools.Result{Content: fmt.Sprintf("%s is empty", path)}, nil
	}

	// Bound the listing so a huge directory (build output, dependency
	// trees) cannot flood the model context. Dropped entries are named
	// by the marker, never silently cut.
	maxEntries := l.resolveLimits().MaxLines
	var b strings.Builder
	shown := 0
	for _, e := range entries {
		if maxEntries > 0 && shown >= maxEntries {
			break
		}
		if e.IsDir() {
			fmt.Fprintf(&b, "%s/\n", e.Name())
		} else {
			fmt.Fprintf(&b, "%s\n", e.Name())
		}
		shown++
	}
	out := strings.TrimRight(b.String(), "\n")
	if shown < len(entries) {
		out += fmt.Sprintf("\n\n[...listing truncated at %d of %d entries. List a subdirectory or use search_files to narrow.]", shown, len(entries))
	}

	return tools.Result{Content: out, Metadata: listTruncMeta(shown, len(entries))}, nil
}

// listTruncMeta reports structured truncation info only when entries
// were actually dropped; nil otherwise.
func listTruncMeta(shown, total int) map[string]any {
	if shown >= total {
		return nil
	}
	return map[string]any{
		"truncated":       true,
		"shown_entries":   shown,
		"total_entries":   total,
		"dropped_entries": total - shown,
	}
}
