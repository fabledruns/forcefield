package bench

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Workspace represents an isolated filesystem directory and Git repository
// created for running a single benchmark task.
type Workspace struct {
	Dir           string
	InitialCommit string
	Task          *Task
	RunID         string
	Retained      bool
	Cleaned       bool
}

// WorkspaceManager handles workspace provisioning, fixture installation,
// diff collection, and safe cleanup.
type WorkspaceManager struct {
	BaseDir    string
	KeepAll    bool
	KeepFailed bool
}

// NewWorkspaceManager constructs a WorkspaceManager.
func NewWorkspaceManager(baseDir string, keepAll, keepFailed bool) *WorkspaceManager {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "ff-benchmarks")
	}
	return &WorkspaceManager{
		BaseDir:    baseDir,
		KeepAll:    keepAll,
		KeepFailed: keepFailed,
	}
}

// Prepare creates and initializes an isolated workspace for task.
func (m *WorkspaceManager) Prepare(ctx context.Context, task *Task, runID string) (*Workspace, error) {
	if err := os.MkdirAll(m.BaseDir, 0700); err != nil {
		return nil, fmt.Errorf("create benchmark base dir: %w", err)
	}

	wsDir, err := os.MkdirTemp(m.BaseDir, fmt.Sprintf("%s-%s-*", task.ID, runID))
	if err != nil {
		return nil, fmt.Errorf("create task workspace directory: %w", err)
	}

	ws := &Workspace{
		Dir:   wsDir,
		Task:  task,
		RunID: runID,
	}

	// 1. Copy fixture files from task directory if present (e.g. task.Dir/fixture).
	if task.Dir != "" {
		fixtureDir := filepath.Join(task.Dir, "fixture")
		if info, err := os.Stat(fixtureDir); err == nil && info.IsDir() {
			if err := copyDir(fixtureDir, wsDir); err != nil {
				_ = os.RemoveAll(wsDir)
				return nil, fmt.Errorf("copy task fixture directory: %w", err)
			}
		}
	}

	// 2. Populate embedded fixtures defined in task manifest.
	for relPath, content := range task.Fixtures {
		cleanRel := filepath.Clean(filepath.FromSlash(relPath))
		if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) || filepath.VolumeName(cleanRel) != "" {
			_ = os.RemoveAll(wsDir)
			return nil, fmt.Errorf("fixture path %q escapes workspace boundary", relPath)
		}
		target := filepath.Join(wsDir, cleanRel)
		relCheck, err := filepath.Rel(wsDir, target)
		if err != nil || strings.HasPrefix(relCheck, "..") {
			_ = os.RemoveAll(wsDir)
			return nil, fmt.Errorf("fixture path %q resolves outside workspace", relPath)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			_ = os.RemoveAll(wsDir)
			return nil, fmt.Errorf("create fixture parent directory for %s: %w", relPath, err)
		}
		if err := os.WriteFile(target, []byte(content), 0644); err != nil {
			_ = os.RemoveAll(wsDir)
			return nil, fmt.Errorf("write fixture file %s: %w", relPath, err)
		}
	}

	// 3. Initialize Git repository and commit initial state.
	initCmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.name", "Forcefield Benchmark"},
		{"git", "config", "user.email", "bench@forcefield.local"},
		{"git", "config", "commit.gpgsign", "false"},
		{"git", "add", "-A"},
		{"git", "commit", "-m", "initial benchmark state", "--allow-empty"},
	}

	for _, argv := range initCmds {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = wsDir
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			_ = os.RemoveAll(wsDir)
			return nil, fmt.Errorf("git %s failed in %s: %v (output: %s)", argv[1], wsDir, err, out.String())
		}
	}

	// Capture initial commit SHA.
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = wsDir
	out, err := cmd.Output()
	if err != nil {
		_ = os.RemoveAll(wsDir)
		return nil, fmt.Errorf("resolve initial commit in %s: %w", wsDir, err)
	}
	ws.InitialCommit = strings.TrimSpace(string(out))

	return ws, nil
}

// RestoreCanonical re-applies any canonical test files to ensure the agent did
// not bypass validation by altering assertions.
func (m *WorkspaceManager) RestoreCanonical(ws *Workspace) error {
	if ws == nil || ws.Task == nil {
		return nil
	}
	task := ws.Task

	// Restore from task directory validation/canonical folder if present.
	if task.Dir != "" {
		for _, sub := range []string{"canonical", "validation"} {
			canDir := filepath.Join(task.Dir, sub)
			if info, err := os.Stat(canDir); err == nil && info.IsDir() {
				if err := copyDir(canDir, ws.Dir); err != nil {
					return fmt.Errorf("restore canonical directory %s: %w", sub, err)
				}
			}
		}
	}

	// Restore from embedded CanonicalFiles in task manifest.
	for relPath, content := range task.CanonicalFiles {
		cleanRel := filepath.Clean(filepath.FromSlash(relPath))
		if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) || filepath.VolumeName(cleanRel) != "" {
			return fmt.Errorf("canonical path %q escapes workspace boundary", relPath)
		}
		target := filepath.Join(ws.Dir, cleanRel)
		relCheck, err := filepath.Rel(ws.Dir, target)
		if err != nil || strings.HasPrefix(relCheck, "..") {
			return fmt.Errorf("canonical path %q resolves outside workspace", relPath)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return fmt.Errorf("create canonical parent dir for %s: %w", relPath, err)
		}
		if err := os.WriteFile(target, []byte(content), 0644); err != nil {
			return fmt.Errorf("restore canonical file %s: %w", relPath, err)
		}
	}
	return nil
}

// Diff collects list of modified files and git diff against initial commit.
func (m *WorkspaceManager) Diff(ctx context.Context, ws *Workspace) (files []string, diffText string, err error) {
	if ws == nil {
		return nil, "", fmt.Errorf("workspace is nil")
	}

	// Mark untracked files with intent-to-add so git diff includes them.
	cmdAddN := exec.CommandContext(ctx, "git", "add", "-N", ".")
	cmdAddN.Dir = ws.Dir
	_ = cmdAddN.Run()

	// List modified and untracked files
	cmdFiles := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmdFiles.Dir = ws.Dir
	outFiles, err := cmdFiles.Output()
	if err != nil {
		return nil, "", fmt.Errorf("git status in %s: %w", ws.Dir, err)
	}

	lines := strings.Split(string(outFiles), "\n")
	seen := make(map[string]bool)
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if len(l) < 3 {
			continue
		}
		// status format: "XY path" or "XY orig -> new"
		pathPart := strings.TrimSpace(l[2:])
		if arrowIdx := strings.Index(pathPart, "->"); arrowIdx != -1 {
			pathPart = strings.TrimSpace(pathPart[arrowIdx+2:])
		}
		norm := filepath.ToSlash(pathPart)
		if !seen[norm] {
			seen[norm] = true
			files = append(files, norm)
		}
	}

	// Collect full unified diff against initial commit
	var commitTarget = "HEAD"
	if ws.InitialCommit != "" {
		commitTarget = ws.InitialCommit
	}
	cmdDiff := exec.CommandContext(ctx, "git", "diff", commitTarget)
	cmdDiff.Dir = ws.Dir
	outDiff, err := cmdDiff.Output()
	if err == nil {
		diffText = string(outDiff)
	}

	return files, diffText, nil
}

// Cleanup removes or retains the workspace according to configuration and outcome.
func (m *WorkspaceManager) Cleanup(ws *Workspace, passed bool) error {
	if ws == nil || ws.Cleaned {
		return nil
	}
	if m.KeepAll || (!passed && m.KeepFailed) {
		ws.Retained = true
		return nil
	}

	absBase, err := filepath.Abs(m.BaseDir)
	if err != nil {
		return fmt.Errorf("resolve base dir %s: %w", m.BaseDir, err)
	}
	absWs, err := filepath.Abs(ws.Dir)
	if err != nil {
		return fmt.Errorf("resolve workspace dir %s: %w", ws.Dir, err)
	}
	rel, err := filepath.Rel(absBase, absWs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("refusing to delete directory outside base dir: %s (base: %s)", absWs, absBase)
	}

	ws.Cleaned = true
	// Give windows a brief moment to release file handles if needed
	time.Sleep(10 * time.Millisecond)
	return os.RemoveAll(ws.Dir)
}

// copyDir recursively copies files and directories from src to dst.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
