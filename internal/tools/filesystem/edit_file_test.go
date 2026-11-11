package filesystem

import (
	"context"
	"os"
	"path/filepath"
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

func TestEditFile_BoundaryChecker(t *testing.T) {
	dir := t.TempDir()
	tool := NewEditFileWithPolicy(sandbox.Policy{Workspace: dir})

	// Check boundary inside workspace
	resPath, err := tool.CheckBoundary(map[string]any{"path": "foo/bar.txt"})
	if err != nil {
		t.Fatalf("CheckBoundary inside workspace: %v", err)
	}
	if !strings.HasPrefix(resPath, dir) {
		t.Errorf("expected path inside %s, got %s", dir, resPath)
	}

	// Check boundary outside workspace
	_, errOutside := tool.CheckBoundary(map[string]any{"path": "../../../outside.txt"})
	if errOutside == nil {
		t.Fatal("expected error for path outside workspace")
	}
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
