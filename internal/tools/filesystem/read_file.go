// Package filesystem provides built-in tools for reading and writing
// local files.
package filesystem

import (
	"context"
	"fmt"
	"io"
	"os"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// ReadFile reads the contents of a file at a given path.
type ReadFile struct {
	policy sandbox.Policy
	// limits overrides the read bound. Zero values resolve via
	// tools.DefaultLimitsFor("read_file").
	limits tools.Limits
}

// NewReadFile returns a ready-to-register ReadFile tool.
func NewReadFile() *ReadFile { return &ReadFile{} }

// NewReadFileWithPolicy returns a ReadFile confined to policy.Workspace when
// policy.Mode is wsl; otherwise it behaves like NewReadFile (native).
func NewReadFileWithPolicy(p sandbox.Policy) *ReadFile { return &ReadFile{policy: p} }

// SetLimits overrides the read bound. Only positive fields take effect;
// the rest resolve to the tool defaults.
func (r *ReadFile) SetLimits(l tools.Limits) {
	r.limits = l
}

// ToolLimits reports the resolved bounds.
func (r *ReadFile) ToolLimits() tools.Limits {
	return r.resolveLimits()
}

func (r *ReadFile) resolveLimits() tools.Limits {
	if r == nil {
		return tools.DefaultLimitsFor("read_file")
	}
	return r.limits.WithDefaults(tools.DefaultLimitsFor("read_file"))
}

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
	if r.policy.Confines() {
		rp, err := sandbox.ResolveWithinWorkspace(r.policy.Workspace, path)
		if err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: %v", path, err)}, nil
		}
		resolved = rp
	}

	// TOCTOU mitigation in confined mode (wsl or strict): open with O_NOFOLLOW where available
	// so a symlink is not followed, then fstat the open descriptor. This
	// narrows the Stat→Read window. In native mode (unrestricted) we use
	// plain Open to preserve historical symlink-following behavior.
	var f *os.File
	if r.policy.Confines() {
		f, err = openNoFollow(resolved)
		if err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: %v", path, err)}, nil
		}
		defer f.Close()
	} else {
		f, err = os.Open(resolved)
		if err != nil {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: %v", path, err)}, nil
		}
		defer f.Close()
	}

	info, err := f.Stat()
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: %v", path, err)}, nil
	}
	if info.IsDir() {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: is a directory", path)}, nil
	}
	// maxBytes caps how much of a file read_file will return, so a model
	// accidentally pointed at a huge file can't blow up memory or flood
	// the context window. Over-limit files are refused with a note (not
	// read partially), and the bound is configurable per tool.
	maxBytes := int64(r.resolveLimits().MaxBytes)
	if info.Size() > maxBytes {
		return tools.Result{IsError: true, Content: fmt.Sprintf(
			"cannot read %s: file is %d bytes, which exceeds the %d byte limit", path, info.Size(), maxBytes,
		), Metadata: map[string]any{"limit_bytes": maxBytes, "file_bytes": info.Size()}}, nil
	}

	data, err := readLimited(f, maxBytes)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: %v", path, err)}, nil
	}
	if int64(len(data)) > maxBytes {
		return tools.Result{IsError: true, Content: fmt.Sprintf(
			"cannot read %s: file exceeds %d byte limit during read", path, maxBytes,
		)}, nil
	}

	return tools.Result{Content: string(data)}, nil
}

func readLimited(f *os.File, limit int64) ([]byte, error) {
	// Read up to limit+1 to detect overflow
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	return data, nil
}
