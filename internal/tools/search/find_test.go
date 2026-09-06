package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

func TestFind_SubstringMatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "x")
	writeFile(t, filepath.Join(dir, "main_test.go"), "x")
	writeFile(t, filepath.Join(dir, "README.md"), "x")

	tool := NewFindFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "main", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %s", res.Content)
	}
	if !strings.Contains(res.Content, "main.go") || !strings.Contains(res.Content, "main_test.go") {
		t.Errorf("substring misses, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "README") {
		t.Errorf("false positive, got:\n%s", res.Content)
	}
}

func TestFind_GlobMatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "x")
	writeFile(t, filepath.Join(dir, "b.txt"), "x")
	writeFile(t, filepath.Join(dir, "sub", "c.go"), "x")

	tool := NewFindFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "*.go", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Basename globbing finds nested matches too (walk is recursive).
	if !strings.Contains(res.Content, "a.go") || !strings.Contains(res.Content, "c.go") {
		t.Errorf("glob misses, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "b.txt") {
		t.Errorf("glob false positive, got:\n%s", res.Content)
	}
}

func TestFind_PathGlobMatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "sub", "c.go"), "x")
	writeFile(t, filepath.Join(dir, "other.go"), "x")

	tool := NewFindFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "sub/*.go", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "sub/c.go") {
		t.Errorf("path glob miss, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "other.go") {
		t.Errorf("path glob false positive, got:\n%s", res.Content)
	}
}

func TestFind_OutputSortedAndRelative(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "zebra.txt"), "x")
	writeFile(t, filepath.Join(dir, "apple.txt"), "x")
	writeFile(t, filepath.Join(dir, "sub", "mango.txt"), "x")

	tool := NewFindFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "*.txt", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(strings.SplitN(res.Content, "\n\n[", 2)[0]), "\n")
	want := []string{"apple.txt", "sub/mango.txt", "zebra.txt"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("lines = %v, want sorted %v", lines, want)
		}
	}
	if filepath.IsAbs(lines[0]) {
		t.Errorf("paths must be workspace-relative, got %q", lines[0])
	}
}

func TestFind_ResultCapTruncates(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 70; i++ {
		writeFile(t, filepath.Join(dir, strings.Repeat("f", 3)+string(rune('a'+i%26))+string(rune('a'+i/26))+".txt"), "x")
	}
	tool := NewFindFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "*.txt", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "truncated at 50 matches") {
		t.Fatalf("expected truncation marker, got:\n%.500s", res.Content)
	}
	if res.Metadata == nil || res.Metadata["truncated"] != true || res.Metadata["match_limit"] != 50 {
		t.Errorf("Metadata = %v, want truncation record", res.Metadata)
	}
}

func TestFind_EmptyAndErrorCases(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "x")
	tool := NewFindFiles()

	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "zzz-no-such", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "no files matching") {
		t.Errorf("want no-match message, got:\n%s", res.Content)
	}

	if _, err := tool.Execute(context.Background(), map[string]any{"pattern": "  ", "path": dir}); err == nil {
		t.Error("empty pattern must be a hard error")
	}
	if _, err := tool.Execute(context.Background(), map[string]any{"path": dir}); err == nil {
		t.Error("missing pattern must be a hard error")
	}
	res, err = tool.Execute(context.Background(), map[string]any{"pattern": "[unclosed", "path": dir})
	if err != nil || !res.IsError || !strings.Contains(res.Content, "invalid glob") {
		t.Errorf("malformed glob must be a soft invalid-glob error, got err=%v res=%+v", err, res)
	}

	res, err = tool.Execute(context.Background(), map[string]any{"pattern": "x", "path": filepath.Join(dir, "nope")})
	if err != nil || !res.IsError {
		t.Fatalf("missing dir must be a soft error, got %v %v", err, res)
	}
	res, err = tool.Execute(context.Background(), map[string]any{"pattern": "x", "path": filepath.Join(dir, "a.go")})
	if err != nil || !res.IsError {
		t.Fatalf("file-as-dir must be a soft error, got %v %v", err, res)
	}
}

func TestFind_InvalidGlobIsSoftError(t *testing.T) {
	dir := t.TempDir()
	tool := NewFindFiles()
	// "[*.go" contains glob metacharacters but is malformed.
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "[*.go", "path": dir})
	if err != nil {
		t.Fatalf("invalid glob must be soft, got hard: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "invalid glob") {
		t.Fatalf("want invalid-glob soft error, got:\n%s", res.Content)
	}
}

func TestFind_SkipsExcludedDirsSensitiveAndLocks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "node_modules", "dep", "index.js"), "x")
	writeFile(t, filepath.Join(dir, "dist", "out.js"), "x")
	writeFile(t, filepath.Join(dir, "Cargo.lock"), "x")
	writeFile(t, filepath.Join(dir, ".env"), "K=V\n")
	writeFile(t, filepath.Join(dir, ".hidden", "secret.txt"), "x")
	writeFile(t, filepath.Join(dir, "src", "main.go"), "x")

	tool := NewFindFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "*", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, leak := range []string{"node_modules", "dist/", "Cargo.lock", ".env"} {
		if strings.Contains(res.Content, leak) {
			t.Errorf("excluded %q leaked into output:\n%s", leak, res.Content)
		}
	}
	if !strings.Contains(res.Content, "src/main.go") {
		t.Errorf("normal file missing, got:\n%s", res.Content)
	}
	// Hidden files behave like list_files: visible, not filtered.
	if !strings.Contains(res.Content, ".hidden/secret.txt") {
		t.Errorf("hidden files must stay visible, got:\n%s", res.Content)
	}
}

func TestFind_ExplicitLockQueryWins(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Cargo.lock"), "x")
	writeFile(t, filepath.Join(dir, "main.go"), "x")

	tool := NewFindFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "*.lock", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "Cargo.lock") {
		t.Errorf("explicit lock query must find lockfiles, got:\n%s", res.Content)
	}
}

func TestFind_WSLStrictConfinesTraversal(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, filepath.Join(ws, "inner.txt"), "x")
	tool := NewFindFilesWithPolicy(sandbox.Policy{Mode: sandbox.ModeWSL, Workspace: ws})

	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "*", "path": `..\..`})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("traversal outside workspace must fail, got:\n%s", res.Content)
	}
	res, err = tool.Execute(context.Background(), map[string]any{"pattern": "inner", "path": "."})
	if err != nil || res.IsError {
		t.Fatalf("inside workspace must succeed, got %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "inner.txt") {
		t.Errorf("missing inner file, got:\n%s", res.Content)
	}
}

func TestFind_SymlinkEscapeSkipped(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "outside-secret.txt"), "x")
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(filepath.Join(outside, "outside-secret.txt"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeFile(t, filepath.Join(dir, "local.txt"), "x")

	tool := NewFindFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "*.txt", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(res.Content, "link.txt") {
		t.Fatalf("symlink escape must be skipped, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "local.txt") {
		t.Errorf("local file missing, got:\n%s", res.Content)
	}
}

func TestFind_OverrideBoundsResults(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		writeFile(t, filepath.Join(dir, string(rune('a'+i))+".txt"), "x")
	}
	tool := NewFindFiles()
	tool.SetLimits(tools.Limits{MaxLines: 4})
	if got := tool.ToolLimits().MaxLines; got != 4 {
		t.Fatalf("ToolLimits MaxLines = %d, want 4", got)
	}
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "*.txt", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "truncated at 4 matches") {
		t.Errorf("override marker missing, got:\n%s", res.Content)
	}
}

func TestFind_CancelledContextIsSoftError(t *testing.T) {
	dir := t.TempDir()
	tool := NewFindFiles()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := tool.Execute(ctx, map[string]any{"pattern": "*", "path": dir})
	if err != nil {
		t.Fatalf("cancel must be soft, got hard: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "cancelled") {
		t.Errorf("want cancelled soft error, got:\n%s", res.Content)
	}
}
