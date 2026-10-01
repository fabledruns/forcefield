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

// ReadFile reads the contents of a file at a given path.
type ReadFile struct {
	policy sandbox.Policy
	// limits overrides the read bound. Zero values resolve via
	// tools.DefaultLimitsFor("read_file").
	limits tools.Limits
}

// NewReadFile returns a ready-to-register ReadFile tool. Reads are
// always confined to the workspace root (the policy's Workspace, or the
// process working directory when unset): paths are canonicalized and
// resolved inside it before anything is opened.
func NewReadFile() *ReadFile { return &ReadFile{} }

// NewReadFileWithPolicy returns a ReadFile confined to policy.Workspace.
// It behaves like NewReadFile: confinement is unconditional, and the
// policy only selects which root to cage to.
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

func (r ReadFile) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	path, err := tools.StringArg(args, "path")
	if err != nil {
		return tools.Result{}, err
	}
	if err := sandbox.CheckCtx(ctx); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: %v", path, err)}, nil
	}

	// Workspace confinement is unconditional: canonicalize and resolve
	// inside the root before opening anything, so approval can never
	// authorize a read outside it.
	resolved, err := sandbox.ResolveWithinWorkspace(r.policy.Workspace, path)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: %v", path, err)}, nil
	}

	// Hardened open: no-follow where the platform allows (see
	// internal/sandbox/fsaccess_* for the honest Windows caveat), then
	// every check below runs against the open descriptor, never a
	// path re-stat, closing the final-path race.
	var f *os.File
	f, err = sandbox.OpenNoFollowRead(resolved)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: %v", path, err)}, nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: %v", path, err)}, nil
	}
	if err := sandbox.AssertRegular(info); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot read %s: %v", path, err)}, nil
	}
	// maxBytes caps how much of a file read_file will return, so a model
	// accidentally pointed at a huge file can't blow up memory or flood
	// the context window. Over-limit files are refused with a note (not
	// read partially), and the bound is configurable per tool.
	maxBytes := int64(r.resolveLimits().MaxBytes)
	if err := sandbox.AssertReadSize(info, maxBytes); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf(
			"cannot read %s: %v", path, err,
		), Metadata: map[string]any{"limit_bytes": maxBytes, "file_bytes": info.Size()}}, nil
	}

	data, err := sandbox.ReadCapped(ctx, f, maxBytes)
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

// CheckBoundary implements tools.BoundaryChecker: it dry-runs the
// workspace boundary decision for a read (canonicalize + resolve, no
// writes) and returns the canonical path the read would open.
func (r ReadFile) CheckBoundary(args map[string]any) (string, error) {
	path, err := tools.StringArg(args, "path")
	if err != nil {
		return "", err
	}
	return sandbox.ResolveWithinWorkspace(r.policy.Workspace, path)
}
