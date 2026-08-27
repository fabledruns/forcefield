package filesystem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"forcefield/internal/tools"
)

// WriteFile writes text content to a file, creating it (and any missing
// parent directories) if necessary and overwriting it if it exists.
type WriteFile struct{}

// NewWriteFile returns a ready-to-register WriteFile tool.
func NewWriteFile() *WriteFile { return &WriteFile{} }

func (WriteFile) Name() string { return "write_file" }

func (WriteFile) Description() string {
	return "Write text content to a file at the given path, creating parent directories and the file " +
		"itself if needed, overwriting the file if it already exists."
}

func (WriteFile) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to write, absolute or relative to the current working directory.",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Text content to write to the file, replacing any existing content.",
			},
		},
		"required": []string{"path", "content"},
	}
}

func (WriteFile) Execute(_ context.Context, args map[string]any) (tools.Result, error) {
	path, err := tools.StringArg(args, "path")
	if err != nil {
		return tools.Result{}, err
	}
	content, err := tools.StringArg(args, "content")
	if err != nil {
		return tools.Result{}, err
	}

	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
		}
	}

	// Preserve existing file mode when overwriting; otherwise default to 0600.
	targetPerm := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		targetPerm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
	}

	if err := os.WriteFile(path, []byte(content), targetPerm); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
	}
	// Ensure restrictive permissions even if umask is permissive.
	_ = os.Chmod(path, targetPerm)

	return tools.Result{Content: fmt.Sprintf("wrote %d bytes to %s", len(content), path)}, nil
}
