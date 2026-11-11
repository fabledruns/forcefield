package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"forcefield/internal/sandbox"
)

func TestEditFile_ExactReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.txt")
	initial := "Hello World\nThis is Forcefield.\nGoodbye World\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "This is Forcefield.",
				"replace": "This is Forcefield v1.7.0.",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := "Hello World\nThis is Forcefield v1.7.0.\nGoodbye World\n"
	if string(got) != expected {
		t.Errorf("got %q, want %q", string(got), expected)
	}
}

func TestEditFile_MultipleSequentialEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sequence.txt")
	initial := "step1: pending\nstep2: pending\nstep3: pending\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "step1: pending",
				"replace": "step1: completed",
			},
			map[string]any{
				// Second edit targets something modified or adjacent
				"search":  "step2: pending",
				"replace": "step2: completed",
			},
			map[string]any{
				// Third edit targets the output of the second or later
				"search":  "step3: pending",
				"replace": "step3: skipped",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := "step1: completed\nstep2: completed\nstep3: skipped\n"
	if string(got) != expected {
		t.Errorf("got %q, want %q", string(got), expected)
	}
}

func TestEditFile_MissingSearchBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing.txt")
	initial := "line 1\nline 2\nline 3\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "nonexistent line",
				"replace": "replacement",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for missing search block")
	}
	if !strings.Contains(res.Content, "search block not found") {
		t.Errorf("expected 'search block not found' in error, got: %s", res.Content)
	}

	// File must be untouched
	got, _ := os.ReadFile(path)
	if string(got) != initial {
		t.Errorf("file was altered on failed edit: got %q, want %q", string(got), initial)
	}
}

func TestEditFile_DuplicateSearchBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "duplicate.txt")
	initial := "foo\nbar\nfoo\nbar\nfoo\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "foo",
				"replace": "baz",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for ambiguous match")
	}
	if !strings.Contains(res.Content, "matched 3 times") {
		t.Errorf("expected 'matched 3 times' in error, got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "at lines 1, 3, 5") {
		t.Errorf("expected line numbers in error, got: %s", res.Content)
	}

	// File must be untouched
	got, _ := os.ReadFile(path)
	if string(got) != initial {
		t.Errorf("file was altered on failed edit: got %q, want %q", string(got), initial)
	}
}

func TestEditFile_EmptySearchBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty_search.txt")
	initial := "content\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "",
				"replace": "replacement",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected tool error for empty search block")
	}
	if !strings.Contains(res.Content, "cannot be empty") {
		t.Errorf("expected 'cannot be empty' in error, got: %s", res.Content)
	}
}

func TestEditFile_EmptyReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "delete.txt")
	initial := "line 1\nREMOVE_ME\nline 3\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "REMOVE_ME\n",
				"replace": "",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}

	got, _ := os.ReadFile(path)
	expected := "line 1\nline 3\n"
	if string(got) != expected {
		t.Errorf("got %q, want %q", string(got), expected)
	}
}

func TestEditFile_CRLFPreservation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crlf.txt")
	initial := "first\r\nsecond\r\nthird\r\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	// Model sends LF-terminated strings; edit_file should adapt and preserve CRLF in output
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "second\n",
				"replace": "second_modified\n",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}

	got, _ := os.ReadFile(path)
	expected := "first\r\nsecond_modified\r\nthird\r\n"
	if string(got) != expected {
		t.Errorf("got %q, want %q", string(got), expected)
	}
	if !strings.Contains(string(got), "\r\n") {
		t.Error("CRLF line endings were lost")
	}
}

func TestEditFile_UnicodeContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unicode.txt")
	initial := "Hello 世界! 🚀\nForcefield: 日本語と絵文字\nEnd\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "日本語と絵文字",
				"replace": "Go言語とAIエージェント 🔥",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}

	got, _ := os.ReadFile(path)
	expected := "Hello 世界! 🚀\nForcefield: Go言語とAIエージェント 🔥\nEnd\n"
	if string(got) != expected {
		t.Errorf("got %q, want %q", string(got), expected)
	}
}

func TestEditFile_LargeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	var b strings.Builder
	for i := 1; i <= 2000; i++ {
		b.WriteString("line ")
		b.WriteString(string(rune('A' + (i % 26))))
		b.WriteString(" numbering ")
		b.WriteString(strings.Repeat("x", 40))
		b.WriteString("\n")
		if i == 1500 {
			b.WriteString("TARGET_KEY_UNIQUE_12345 = false\n")
		}
	}
	initial := b.String()
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "TARGET_KEY_UNIQUE_12345 = false",
				"replace": "TARGET_KEY_UNIQUE_12345 = true",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}

	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "TARGET_KEY_UNIQUE_12345 = true") {
		t.Error("large file target was not modified")
	}
	if strings.Contains(string(got), "TARGET_KEY_UNIQUE_12345 = false") {
		t.Error("large file old target still present")
	}
}

func TestEditFile_PermissionPreservation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script.sh")
	initial := "#!/bin/sh\necho hello\n"
	perm := os.FileMode(0o755)
	if err := os.WriteFile(path, []byte(initial), perm); err != nil {
		t.Fatal(err)
	}
	// Verify perm before edit
	infoBefore, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "echo hello",
				"replace": "echo world",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}

	infoAfter, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if infoAfter.Mode().Perm() != infoBefore.Mode().Perm() {
		t.Errorf("permissions changed: before %v, after %v", infoBefore.Mode().Perm(), infoAfter.Mode().Perm())
	}
}

func TestEditFile_WorkspaceBoundary(t *testing.T) {
	dir := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("sensitive\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": outsideFile,
		"edits": []any{
			map[string]any{
				"search":  "sensitive",
				"replace": "leaked",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected boundary error for outside file")
	}
	if !strings.Contains(res.Content, "escapes") && !strings.Contains(res.Content, "outside") {
		t.Errorf("expected escape error, got: %s", res.Content)
	}

	// Outside file must remain untouched
	got, _ := os.ReadFile(outsideFile)
	if string(got) != "sensitive\n" {
		t.Errorf("outside file was modified: %s", string(got))
	}
}

func TestEditFile_AtomicFailureWhenLaterEditFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic.txt")
	initial := "part1: original\npart2: original\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	// First edit would succeed, but second edit fails
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "part1: original",
				"replace": "part1: modified",
			},
			map[string]any{
				"search":  "part2: NONEXISTENT",
				"replace": "part2: modified",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected failure on second edit")
	}

	// File on disk must NOT have part1 modified!
	got, _ := os.ReadFile(path)
	if string(got) != initial {
		t.Errorf("atomic failure violated: file was partially modified! got: %q", string(got))
	}
}

func TestEditFile_GoSyntaxValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.go")
	initial := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})

	// 1. Invalid Go syntax edit: broken bracket
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "func main() {",
				"replace": "func main( {",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected syntax validation error for broken Go syntax")
	}
	if !strings.Contains(res.Content, "syntax validation failed") {
		t.Errorf("expected 'syntax validation failed' in error, got: %s", res.Content)
	}

	// File must remain unchanged
	got, _ := os.ReadFile(path)
	if string(got) != initial {
		t.Errorf("broken syntax was committed to disk: %q", string(got))
	}

	// 2. Valid Go syntax edit: valid function modification
	resValid, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  `println("hello")`,
				"replace": `println("world")`,
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resValid.IsError {
		t.Fatalf("unexpected tool error: %s", resValid.Content)
	}

	gotValid, _ := os.ReadFile(path)
	expectedValid := "package main\n\nfunc main() {\n\tprintln(\"world\")\n}\n"
	if string(gotValid) != expectedValid {
		t.Errorf("got %q, want %q", string(gotValid), expectedValid)
	}
}

func TestEditFile_NonexistentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.txt")

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "foo",
				"replace": "bar",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error for nonexistent file")
	}
	if !strings.Contains(res.Content, "file does not exist") {
		t.Errorf("expected 'file does not exist' in error, got: %s", res.Content)
	}
}

func canonicalizeWorkspacePath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	abs = strings.TrimPrefix(filepath.Clean(abs), `\\?\`)
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs
}

func isWithinWorkspace(workspace, target string) bool {
	canonWs := canonicalizeWorkspacePath(workspace)
	canonTarget := canonicalizeWorkspacePath(target)
	if canonWs == canonTarget {
		return true
	}
	rel, err := filepath.Rel(canonWs, canonTarget)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func assertWithinWorkspace(t *testing.T, workspace, target string) {
	t.Helper()
	if !isWithinWorkspace(workspace, target) {
		t.Fatalf("target %q (canonical: %q) is not within workspace %q (canonical: %q)",
			target, canonicalizeWorkspacePath(target), workspace, canonicalizeWorkspacePath(workspace))
	}
}

func TestEditFile_BoundaryChecker(t *testing.T) {
	dir := t.TempDir()
	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})

	// Check boundary inside workspace
	resPath, err := tool.CheckBoundary(map[string]any{"path": "foo/bar.txt"})
	if err != nil {
		t.Fatalf("CheckBoundary inside workspace: %v", err)
	}
	assertWithinWorkspace(t, dir, resPath)

	// Check boundary outside workspace
	_, errOutside := tool.CheckBoundary(map[string]any{"path": "../../../outside.txt"})
	if errOutside == nil {
		t.Fatal("expected error for path outside workspace")
	}
}

func TestEditFile_BoundaryChecker_Regression(t *testing.T) {
	t.Run("canonical vs non-canonical temporary paths", func(t *testing.T) {
		dir := t.TempDir()
		// Test uncleaned relative path
		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		resPath, err := tool.CheckBoundary(map[string]any{"path": "foo/./sub/../bar.txt"})
		if err != nil {
			t.Fatalf("CheckBoundary failed for uncleaned subpath: %v", err)
		}
		assertWithinWorkspace(t, dir, resPath)

		// Test uncleaned workspace directory
		uncleanedWs := filepath.Join(dir, "extra", "..")
		toolUncleaned := NewEditFileWithPolicy(sandbox.Policy{Workspace: uncleanedWs})
		resPath2, err := toolUncleaned.CheckBoundary(map[string]any{"path": "file.txt"})
		if err != nil {
			t.Fatalf("CheckBoundary failed for uncleaned workspace: %v", err)
		}
		assertWithinWorkspace(t, dir, resPath2)
	})

	t.Run("macOS /var vs /private/var symlink alias equivalence", func(t *testing.T) {
		dir := t.TempDir()
		aliasDir := filepath.Join(t.TempDir(), "ws-alias")
		if err := os.Symlink(dir, aliasDir); err != nil {
			t.Skipf("symlinks unavailable on this host/privilege: %v", err)
		}
		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: aliasDir})
		resPath, err := tool.CheckBoundary(map[string]any{"path": "nested/target.txt"})
		if err != nil {
			t.Fatalf("CheckBoundary failed through workspace symlink: %v", err)
		}
		assertWithinWorkspace(t, dir, resPath)
		assertWithinWorkspace(t, aliasDir, resPath)
	})

	t.Run("windows short-name vs long-name", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("Windows 8.3 short names are specific to Windows hosts")
		}
		dir := t.TempDir()
		shortDir, err := windowsShortPath(dir)
		if err != nil {
			t.Skipf("short path unavailable: %v", err)
		}
		dirInfo, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat long workspace path: %v", err)
		}
		shortDirInfo, err := os.Stat(shortDir)
		if err != nil {
			t.Fatalf("stat short workspace path: %v", err)
		}
		if !os.SameFile(dirInfo, shortDirInfo) {
			t.Fatalf("expected long and short workspace paths to reference the same directory: %q vs %q", dir, shortDir)
		}
		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: shortDir})
		resPath, err := tool.CheckBoundary(map[string]any{"path": "short/test.txt"})
		if err != nil {
			t.Fatalf("CheckBoundary failed for 8.3 short-name workspace: %v", err)
		}
		assertWithinWorkspace(t, shortDir, resPath)
	})

	t.Run("windows case differences", func(t *testing.T) {
		dir := t.TempDir()
		var casedDir string
		if runtime.GOOS == "windows" {
			casedDir = strings.ToUpper(dir)
		} else {
			casedDir = dir
		}
		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: casedDir})
		resPath, err := tool.CheckBoundary(map[string]any{"path": "cased/file.txt"})
		if err != nil {
			t.Fatalf("CheckBoundary failed for cased workspace: %v", err)
		}
		assertWithinWorkspace(t, dir, resPath)
	})

	t.Run("genuine paths outside workspace", func(t *testing.T) {
		dir := t.TempDir()
		outside := t.TempDir()
		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		outsideFile := filepath.Join(outside, "secret.txt")
		_, err := tool.CheckBoundary(map[string]any{"path": outsideFile})
		if err == nil {
			t.Fatalf("CheckBoundary unexpectedly allowed genuine outside path: %s", outsideFile)
		}
	})

	t.Run("prefix collision rejection", func(t *testing.T) {
		dir := t.TempDir()
		siblingDir := dir + "_sibling"
		if err := os.MkdirAll(siblingDir, 0o755); err != nil {
			t.Fatal(err)
		}
		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		siblingFile := filepath.Join(siblingDir, "evil.txt")
		_, err := tool.CheckBoundary(map[string]any{"path": siblingFile})
		if err == nil {
			t.Fatalf("CheckBoundary unexpectedly allowed prefix-collision sibling path: %s", siblingFile)
		}
	})

	t.Run("parent directory traversal", func(t *testing.T) {
		dir := t.TempDir()
		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		traversals := []string{
			"../../../outside.txt",
			"sub/../../outside.txt",
			"a/b/../../../outside.txt",
		}
		for _, target := range traversals {
			_, err := tool.CheckBoundary(map[string]any{"path": target})
			if err == nil {
				t.Fatalf("CheckBoundary unexpectedly allowed traversal %q", target)
			}
		}
	})

	t.Run("symlink escapes", func(t *testing.T) {
		dir := t.TempDir()
		outside := t.TempDir()
		linkOut := filepath.Join(dir, "link_out")
		if err := os.Symlink(outside, linkOut); err != nil {
			t.Skipf("symlinks unavailable on this host/privilege: %v", err)
		}
		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		_, err := tool.CheckBoundary(map[string]any{"path": filepath.Join("link_out", "secret.txt")})
		if err == nil {
			t.Fatalf("CheckBoundary unexpectedly allowed symlink escape through %s", linkOut)
		}
	})
}

func TestEditFile_ConcurrentModification(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrency.txt")
	initial := "original line\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	// Trigger a concurrent write right before the commit check
	tool.preCommitHook = func(p string) {
		time.Sleep(10 * time.Millisecond)
		_ = os.WriteFile(p, []byte("concurrent change that alters size and time\n"), 0o644)
	}

	res, err := tool.Execute(context.Background(), map[string]any{
		"path": path,
		"edits": []any{
			map[string]any{
				"search":  "original line",
				"replace": "my line",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error for concurrent modification")
	}
	if !strings.Contains(res.Content, "modified concurrently") {
		t.Errorf("expected 'modified concurrently' in error, got: %s", res.Content)
	}
}

func TestEditFile_SymlinkRejection(t *testing.T) {
	dir := t.TempDir()
	realFile := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(realFile, []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(dir, "link.txt")
	if err := os.Symlink(realFile, symlinkPath); err != nil {
		t.Skipf("symlinks not supported on this platform/privilege: %v", err)
	}

	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{
		"path": symlinkPath,
		"edits": []any{
			map[string]any{
				"search":  "content",
				"replace": "modified",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error for symlink path")
	}
	if !strings.Contains(res.Content, "is a symlink") {
		t.Errorf("expected 'is a symlink' in error, got: %s", res.Content)
	}
}

func TestEditFile_IndentationMatching_Suite(t *testing.T) {
	// 1. Exact matching still taking precedence
	t.Run("ExactPrecedence", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "precedence.go")
		initial := "package main\n\nfunc A() {\n    val := 1\n}\nfunc B() {\n\tval := 1\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    val := 1",
					"replace": "    val := 100",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package main\n\nfunc A() {\n    val := 100\n}\nfunc B() {\n\tval := 1\n}\n"
		if string(got) != expected {
			t.Errorf("exact match should take precedence;\ngot:\n%s\nwant:\n%s", string(got), expected)
		}
	})

	// 2. Tab-indented file matched by space-indented search input
	t.Run("TabIndentedFile_SpaceSearch", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "payload.go")
		initial := "package payload\n\nfunc Unmarshal() {\n\tdoc.Author = doc.Meta.Author\n\treturn &doc, nil\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    doc.Author = doc.Meta.Author\n    return &doc, nil",
					"replace": "    if doc.Meta != nil {\n        doc.Author = doc.Meta.Author\n    }\n    return &doc, nil",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package payload\n\nfunc Unmarshal() {\n\tif doc.Meta != nil {\n\t\tdoc.Author = doc.Meta.Author\n\t}\n\treturn &doc, nil\n}\n"
		if string(got) != expected {
			t.Errorf("tab-indented file replacement mismatch;\ngot:\n%s\nwant:\n%s", string(got), expected)
		}
	})

	// 3. Space-indented file matched by tab-indented search input
	t.Run("SpaceIndentedFile_TabSearch", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "app.py")
		initial := "def calc():\n    a = 1\n    b = 2\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "\ta = 1\n\tb = 2",
					"replace": "\ta = 10\n\tb = 20",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "def calc():\n    a = 10\n    b = 20\n"
		if string(got) != expected {
			t.Errorf("space-indented file replacement mismatch;\ngot:\n%s\nwant:\n%s", string(got), expected)
		}
	})

	// 4. Mixed indentation
	t.Run("MixedIndentation", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "mixed.txt")
		initial := "header\n\t    flag = true\nfooter\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "        flag = true",
					"replace": "        flag = false",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		if !strings.Contains(string(got), "flag = false") {
			t.Errorf("expected mixed indentation replacement, got:\n%s", string(got))
		}
	})

	// 5. Whitespace inside string literals remains significant
	t.Run("WhitespaceInStrings_Significant", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "strings.go")
		initial := "package main\n\nfunc msg() {\n\ts := \"hello   world\"\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    s := \"hello world\"", // single space instead of three
					"replace": "    s := \"goodbye\"",
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatal("expected search failure when whitespace inside string literal differs")
		}
	})

	// 6. Whitespace inside expressions remains significant
	t.Run("WhitespaceInExpressions_Significant", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "expr.go")
		initial := "package main\n\nfunc sum() {\n\tres := a + b\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    res := a+b", // collapsed spaces inside expression
					"replace": "    res := a + b + c",
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatal("expected search failure when whitespace inside expression differs")
		}
	})

	// 7. Multiple normalized matches are rejected
	t.Run("MultipleNormalizedMatches_Rejected", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "dup.go")
		initial := "package main\n\nfunc F1() error {\n\treturn nil\n}\n\nfunc F2() error {\n\treturn nil\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    return nil",
					"replace": "    return errors.New(\"fail\")",
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatal("expected error for multiple normalized matches")
		}
		if !strings.Contains(res.Content, "matched 2 times") {
			t.Errorf("expected 'matched 2 times' in error message, got: %s", res.Content)
		}
	})

	// 8. No normalized match is rejected
	t.Run("NoNormalizedMatch_Rejected", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "none.go")
		initial := "package main\n\nfunc F() {\n\tval := 1\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    nonExistentCall()",
					"replace": "    replacement()",
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatal("expected error when search block does not exist")
		}
		if !strings.Contains(res.Content, "search block not found") {
			t.Errorf("expected 'search block not found', got: %s", res.Content)
		}
	})

	// 9. Original indentation is preserved after replacement
	t.Run("OriginalIndentation_Preserved", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "tabs.go")
		initial := "package main\n\nfunc Scope() {\n\tinitialCall()\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    initialCall()",
					"replace": "    if true {\n        deepCall()\n    }",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		// Must preserve tab indentation of the original file
		expected := "package main\n\nfunc Scope() {\n\tif true {\n\t\tdeepCall()\n\t}\n}\n"
		if string(got) != expected {
			t.Errorf("original tab indentation was not preserved;\ngot:\n%s\nwant:\n%s", string(got), expected)
		}
	})

	// 10. CRLF and LF handling remains correct
	t.Run("CRLF_LF_Handling", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "crlf.go")
		initial := "package main\r\n\r\nfunc F() {\r\n\tx := 1\r\n}\r\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    x := 1", // LF and space
					"replace": "    x := 10",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package main\r\n\r\nfunc F() {\r\n\tx := 10\r\n}\r\n"
		if string(got) != expected {
			t.Errorf("CRLF line endings were not preserved;\ngot: %q, want: %q", string(got), expected)
		}
	})

	// 11. A failed edit does not partially modify the file
	t.Run("AtomicFailure_NoPartialModification", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "atomic.go")
		initial := "package main\n\nfunc F() {\n\tstep1 := true\n\tstep2 := true\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    step1 := true",
					"replace": "    step1 := false",
				},
				map[string]any{
					"search":  "    nonExistentTarget := true",
					"replace": "    step2 := false",
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatal("expected error on second failing edit")
		}

		got, _ := os.ReadFile(path)
		if string(got) != initial {
			t.Errorf("file was partially modified despite failure;\ngot:\n%s\nwant:\n%s", string(got), initial)
		}
	})

	// 12. Existing path traversal and symlink protections still work
	t.Run("PathTraversalAndSymlinks_Protected", func(t *testing.T) {
		dir := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.txt")
		_ = os.WriteFile(outside, []byte("secret"), 0o644)

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": outside,
			"edits": []any{
				map[string]any{
					"search":  "secret",
					"replace": "hacked",
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatal("expected workspace escape error")
		}
		if !strings.Contains(res.Content, "escapes the authorized workspace") {
			t.Errorf("expected escape error message, got: %s", res.Content)
		}
	})
}

func TestEditFile_ReviewFindings_Suite(t *testing.T) {
	// 1. HIGH-01 Reproduction: Search block ending in LF matches in middle of file
	t.Run("High01_Reproduction_SearchEndingInLF", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "middle.go")
		initial := "package main\n\nfunc foo() {\n\ta := 1\n\tb := 2\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    a := 1\n",
					"replace": "    a := 10\n",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package main\n\nfunc foo() {\n\ta := 10\n\tb := 2\n}\n"
		if string(got) != expected {
			t.Errorf("mismatch;\ngot:\n%q\nwant:\n%q", string(got), expected)
		}
	})

	// 2. HIGH-02 Reproduction: Replace block ending in LF does not add duplicate blank line
	t.Run("High02_Reproduction_ReplaceEndingInLF_NoDuplicateNewline", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "nodupe.go")
		initial := "package main\n\nfunc foo() {\n\ta := 1\n\tb := 2\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    a := 1\n",
					"replace": "    a := 20\n",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package main\n\nfunc foo() {\n\ta := 20\n\tb := 2\n}\n"
		if string(got) != expected {
			t.Errorf("duplicate newline introduced;\ngot:\n%q\nwant:\n%q", string(got), expected)
		}
	})

	// 3. LOW-01: Indentation style detection with mixed comment and tabs
	t.Run("Low01_IndentationStyleDetection_MixedCommentAndTabs", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "mixed_comment.go")
		initial := "package main\n\nfunc foo() {\n  // leading space comment\n\ta := 1\n\tb := 2\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "  // leading space comment\n    a := 1\n    b := 2\n",
					"replace": "  // leading space comment\n    a := 10\n    b := 20\n",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package main\n\nfunc foo() {\n  // leading space comment\n\ta := 10\n\tb := 20\n}\n"
		if string(got) != expected {
			t.Errorf("dominant indentation style mismatch;\ngot:\n%q\nwant:\n%q", string(got), expected)
		}
	})

	// 4. Search block ending in CRLF
	t.Run("SearchEndingInCRLF", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "crlf_search.go")
		initial := "package main\r\n\r\nfunc foo() {\r\n\ta := 1\r\n\tb := 2\r\n}\r\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    a := 1\r\n",
					"replace": "    a := 10\r\n",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package main\r\n\r\nfunc foo() {\r\n\ta := 10\r\n\tb := 2\r\n}\r\n"
		if string(got) != expected {
			t.Errorf("CRLF mismatch;\ngot:\n%q\nwant:\n%q", string(got), expected)
		}
	})

	// 5. Search block without terminal newline
	t.Run("SearchWithoutTerminalNewline", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "noterm.go")
		initial := "package main\n\nfunc foo() {\n\ta := 1\n\tb := 2\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    a := 1",
					"replace": "    a := 10",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package main\n\nfunc foo() {\n\ta := 10\n\tb := 2\n}\n"
		if string(got) != expected {
			t.Errorf("mismatch;\ngot:\n%q\nwant:\n%q", string(got), expected)
		}
	})

	// 6. Replacement block ending in LF
	t.Run("ReplacementEndingInLF", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "multiline_replace.go")
		initial := "package main\n\nfunc foo() {\n\ta := 1\n\tb := 2\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    a := 1\n",
					"replace": "    a := 10\n    extra := true\n",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package main\n\nfunc foo() {\n\ta := 10\n\textra := true\n\tb := 2\n}\n"
		if string(got) != expected {
			t.Errorf("mismatch;\ngot:\n%q\nwant:\n%q", string(got), expected)
		}
	})

	// 7. Replacement block ending in CRLF
	t.Run("ReplacementEndingInCRLF", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "multiline_crlf.go")
		initial := "package main\r\n\r\nfunc foo() {\r\n\ta := 1\r\n\tb := 2\r\n}\r\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    a := 1\r\n",
					"replace": "    a := 10\r\n    extra := true\r\n",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package main\r\n\r\nfunc foo() {\r\n\ta := 10\r\n\textra := true\r\n\tb := 2\r\n}\r\n"
		if string(got) != expected {
			t.Errorf("mismatch;\ngot:\n%q\nwant:\n%q", string(got), expected)
		}
	})

	// 8. Replacement block without terminal newline
	t.Run("ReplacementWithoutTerminalNewline", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "multiline_noterm.go")
		initial := "package main\n\nfunc foo() {\n\ta := 1\n\tb := 2\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    a := 1\n    b := 2",
					"replace": "    a := 10\n    b := 20",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package main\n\nfunc foo() {\n\ta := 10\n\tb := 20\n}\n"
		if string(got) != expected {
			t.Errorf("mismatch;\ngot:\n%q\nwant:\n%q", string(got), expected)
		}
	})

	// 9. Intentional blank line inside search block
	t.Run("IntentionalBlankLineInsideSearchBlock", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "blank_in_search.go")
		initial := "package main\n\nfunc foo() {\n\ta := 1\n\n\tb := 2\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    a := 1\n\n    b := 2\n",
					"replace": "    a := 10\n\n    b := 20\n",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package main\n\nfunc foo() {\n\ta := 10\n\n\tb := 20\n}\n"
		if string(got) != expected {
			t.Errorf("intentional blank line mismatch;\ngot:\n%q\nwant:\n%q", string(got), expected)
		}
	})

	// 10. Intentional blank line inside replacement block
	t.Run("IntentionalBlankLineInsideReplacementBlock", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "blank_in_replace.go")
		initial := "package main\n\nfunc foo() {\n\ta := 1\n\tb := 2\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    a := 1\n    b := 2\n",
					"replace": "    a := 10\n\n    b := 20\n",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package main\n\nfunc foo() {\n\ta := 10\n\n\tb := 20\n}\n"
		if string(got) != expected {
			t.Errorf("intentional blank line in replace mismatch;\ngot:\n%q\nwant:\n%q", string(got), expected)
		}
	})

	// 11. Replacement of final file line when original file has final newline
	t.Run("ReplacementOfFinalFileLine_FileWithFinalNewline", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "eof_nl.txt")
		initial := "first line\nlast line\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "last line\n",
					"replace": "updated last line\n",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "first line\nupdated last line\n"
		if string(got) != expected {
			t.Errorf("final line with newline mismatch;\ngot:\n%q\nwant:\n%q", string(got), expected)
		}
	})

	// 12. Replacement of final file line when original file has NO final newline
	t.Run("ReplacementOfFinalFileLine_FileWithoutFinalNewline", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "eof_nonl.txt")
		initial := "first line\nlast line"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "last line",
					"replace": "updated last line",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "first line\nupdated last line"
		if string(got) != expected {
			t.Errorf("final line without newline mismatch;\ngot:\n%q\nwant:\n%q", string(got), expected)
		}
	})

	// 13. Middle-of-file replacement does NOT delete adjacent blank line
	t.Run("MiddleOfFileReplacement_DoesNotDeleteAdjacentBlankLine", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "preserve_blank.go")
		initial := "package main\n\nfunc foo() {\n\ta := 1\n\n\tb := 2\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "    a := 1\n",
					"replace": "    a := 10\n",
				},
			},
		})
		if err != nil || res.IsError {
			t.Fatalf("unexpected error: %v, content: %s", err, res.Content)
		}

		got, _ := os.ReadFile(path)
		expected := "package main\n\nfunc foo() {\n\ta := 10\n\n\tb := 2\n}\n"
		if string(got) != expected {
			t.Errorf("adjacent blank line was consumed/corrupted;\ngot:\n%q\nwant:\n%q", string(got), expected)
		}
	})

	// 14. Failed edits preserve original file byte-for-byte
	t.Run("FailedEdits_PreserveOriginalByteForByte", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "immutable.go")
		initial := "package main\n\nfunc foo() {\n\ta := 1\n}\n"
		if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
			t.Fatal(err)
		}

		tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})
		res, err := tool.Execute(context.Background(), map[string]any{
			"path": path,
			"edits": []any{
				map[string]any{
					"search":  "nonexistent target string\n",
					"replace": "broken",
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError {
			t.Fatal("expected failure on nonexistent target")
		}

		got, _ := os.ReadFile(path)
		if string(got) != initial {
			t.Errorf("file modified despite failure;\ngot:\n%q\nwant:\n%q", string(got), initial)
		}
	})
}
