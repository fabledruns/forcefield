package filesystem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// ListFiles lists the immediate contents of a directory.
type ListFiles struct {
	policy sandbox.Policy
}

// NewListFiles returns a ready-to-register ListFiles tool.
func NewListFiles() *ListFiles { return &ListFiles{} }

// NewListFilesWithPolicy returns a ListFiles confined to policy.Workspace when
// policy.Mode is wsl; otherwise it behaves like NewListFiles (native).
func NewListFilesWithPolicy(p sandbox.Policy) *ListFiles { return &ListFiles{policy: p} }

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
	if l.policy.Mode == sandbox.ModeWSL {
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
	if l.policy.Mode == sandbox.ModeWSL {
		if real, err := filepath.EvalSymlinks(resolved); err == nil {
			if _, err := sandbox.EnsureWithinWorkspace(l.policy.Workspace, real); err != nil {
				return tools.Result{IsError: true, Content: fmt.Sprintf("cannot list %s: %v", path, err)}, nil
			}
		}
	}

	if len(entries) == 0 {
		return tools.Result{Content: fmt.Sprintf("%s is empty", path)}, nil
	}

	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() {
			fmt.Fprintf(&b, "%s/\n", e.Name())
		} else {
			fmt.Fprintf(&b, "%s\n", e.Name())
		}
	}

	return tools.Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}
