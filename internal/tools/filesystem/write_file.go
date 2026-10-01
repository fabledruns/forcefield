package filesystem

import (
	"context"
	"fmt"
	"os"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// WriteFile writes text content to a file, creating it (and any missing
// parent directories) if necessary and overwriting it if it exists.
type WriteFile struct {
	policy sandbox.Policy
}

// NewWriteFile returns a ready-to-register WriteFile tool. Writes are
// always confined to the workspace root (the policy's Workspace, or the
// process working directory when unset): paths are canonicalized and
// resolved inside it before any directory is created or file is opened.
func NewWriteFile() *WriteFile { return &WriteFile{} }

// NewWriteFileWithPolicy returns a WriteFile confined to
// policy.Workspace. It behaves like NewWriteFile: confinement is
// unconditional, and the policy only selects which root to cage to.
func NewWriteFileWithPolicy(p sandbox.Policy) *WriteFile { return &WriteFile{policy: p} }

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

func (w WriteFile) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	path, err := tools.StringArg(args, "path")
	if err != nil {
		return tools.Result{}, err
	}
	content, err := tools.StringArg(args, "content")
	if err != nil {
		return tools.Result{}, err
	}
	if err := sandbox.CheckCtx(ctx); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
	}
	// Bound input bytes before any allocation or write so one call
	// cannot fill disk. Mirrors the read cap; the model can chunk
	// large writes across calls.
	if len(content) > tools.DefaultWriteMaxBytes {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: content is %d bytes, limit is %d bytes; split the write into smaller chunks", path, len(content), tools.DefaultWriteMaxBytes)}, nil
	}

	// Workspace confinement is unconditional: canonicalize and resolve
	// inside the root before creating anything, so approval can never
	// authorize a write outside it. EnsureWithinWorkspace is
	// creation-aware (it walks existing ancestors for symlink escapes
	// when the target itself does not exist yet).
	resolved, err := sandbox.EnsureWithinWorkspace(w.policy.Workspace, path)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
	}

	// Shared parent preparation: create missing parents, then
	// re-validate so a concurrent swap surfaces as an error before
	// anything is opened (see internal/sandbox/fsaccess.go).
	if err := sandbox.EnsureParentDirs(w.policy.Workspace, resolved); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
	}
	if err := sandbox.CheckCtx(ctx); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
	}

	// If the target already exists and is a symlink, refuse with a
	// clear message. Unix additionally enforces O_NOFOLLOW at open;
	// Windows has no equivalent (honest limitation in fsaccess_windows.go).
	if info, err := os.Lstat(resolved); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: path is a symlink", path)}, nil
		}
	}

	// Preserve existing file mode when overwriting; otherwise default to 0600.
	targetPerm := os.FileMode(0o600)
	if info, err := os.Stat(resolved); err == nil {
		targetPerm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
	}

	// Open no-follow, then validate the open descriptor: regular file
	// (never FIFO/socket/device) and single link (never a hard link to
	// outside data on link-counting platforms). Descriptor checks close
	// the Lstat->write swap window the old path-stat checks left open.
	f, err := sandbox.OpenNoFollowWrite(resolved, targetPerm)
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
	}
	if err := sandbox.AssertRegular(info); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
	}
	if err := sandbox.AssertWriteLinkCount(info); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
	}
	if err := sandbox.WriteCapped(ctx, f, []byte(content)); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
	}
	// fchmod on the open descriptor: unlike path chmod it cannot be
	// redirected by a final-path swap between write and chmod.
	if err := f.Chmod(targetPerm); err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot write %s: %v", path, err)}, nil
	}

	return tools.Result{Content: fmt.Sprintf("wrote %d bytes to %s", len(content), path)}, nil
}

// CheckBoundary implements tools.BoundaryChecker: it dry-runs the
// workspace boundary decision for a write (canonicalize + resolve, no
// writes, nothing created) and returns the canonical path the write
// would target.
func (w WriteFile) CheckBoundary(args map[string]any) (string, error) {
	path, err := tools.StringArg(args, "path")
	if err != nil {
		return "", err
	}
	return sandbox.EnsureWithinWorkspace(w.policy.Workspace, path)
}
