// Package filesystem provides built-in tools for reading and writing
// local files.
package filesystem

import (
	"context"
	"fmt"
	"os"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// maxReadSize caps how much of a file read_file will return, so a model
// accidentally pointed at a huge file can't blow up memory or flood the
// context window.
const maxReadSize = 5 << 20 // 5 MiB

// ReadFile reads the contents of a file at a given path.
type ReadFile struct {
	policy sandbox.Policy
}

// NewReadFile returns a ready-to-register ReadFile tool.
func NewReadFile() *ReadFile { return &ReadFile{} }

// NewReadFileWithPolicy returns a ReadFile confined to policy.Workspace when
// policy.Mode is wsl; otherwise it behaves like NewReadFile (native).
func NewReadFileWithPolicy(p sandbox.Policy) *ReadFile { return &ReadFile{policy: p} }

func (ReadFile) Name() string { return "read_file" }
func (ReadFile) Description() string {
	return "Read the entire contents of a text file at the given path and return it as a string."
}

func (ReadFile) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Path to the file to read, absolute or relative to the current working directory.",
			},
		},
		"required": []string{"path"},
	}
}

func (r ReadFile) Execute(_ context.Context, args map[string]any) (tools.Result, error) {
	path, err := tools.StringArg(args, "path")
	if err != nil {
		return tools.Result{}, err
	}

	resolved := path
	if r.policy.Mode == sandbox.ModeWSL {
		rp, err := sandbox.ResolveWithinWorkspace(r.policy.Workspace, path)
		if err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: %v", path, err)}, nil
		}
		resolved = rp
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: %v", path, err)}, nil
	}
	if info.IsDir() {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: is a directory", path)}, nil
	}
	if info.Size() > maxReadSize {
		return tools.Result{IsError: true, Content: fmt.Sprintf(
			"cannot read %s: file is %d bytes, which exceeds the %d byte limit", path, info.Size(), maxReadSize,
		)}, nil
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: %v", path, err)}, nil
	}

	return tools.Result{Content: string(data)}, nil
}
