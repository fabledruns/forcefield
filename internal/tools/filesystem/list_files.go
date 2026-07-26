package filesystem

import (
	"context"
	"fmt"
	"os"
	"strings"

	"forcefield/internal/tools"
)

// ListFiles lists the immediate contents of a directory.
type ListFiles struct{}

// NewListFiles returns a ready-to-register ListFiles tool.
func NewListFiles() *ListFiles { return &ListFiles{} }

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

func (ListFiles) Execute(_ context.Context, args map[string]any) (tools.Result, error) {
	path := tools.OptionalStringArg(args, "path", ".")

	entries, err := os.ReadDir(path)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot list %s: %v", path, err)}, nil
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
