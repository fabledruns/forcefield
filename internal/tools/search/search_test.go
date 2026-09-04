package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSearch_LiteralMatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package main\n// TODO fix this\nfunc main() {}\n")
	writeFile(t, filepath.Join(dir, "b.go"), "package main\nfunc ok() {}\n")

	tool := NewSearchFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "TODO", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go:2:") {
		t.Fatalf("missing match, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "b.go") {
		t.Fatalf("false positive, got:\n%s", res.Content)
	}
}

func TestSearch_SkipsSensitiveFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "MARKER_SECRET=hunter2\n")
	writeFile(t, filepath.Join(dir, "app.go"), "MARKER_SECRET=hunter2\n")

	tool := NewSearchFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_SECRET", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(res.Content, ".env") {
		t.Fatalf("sensitive .env must be skipped, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "app.go") {
		t.Fatalf("normal file must match, got:\n%s", res.Content)
	}
}

func TestSearch_SkipsGitDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".git", "objects", "x"), "MARKER_GITDATA\n")
	writeFile(t, filepath.Join(dir, "code.go"), "nothing here\n")

	tool := NewSearchFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_GITDATA", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "no matches") {
		t.Fatalf(".git must be skipped, got:\n%s", res.Content)
	}
}

func TestSearch_SymlinkEscapeSkipped(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "MARKER_OUTSIDE\n")
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	tool := NewSearchFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_OUTSIDE", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "no matches") {
		t.Fatalf("symlink escape must be skipped, got:\n%s", res.Content)
	}
}

func TestSearch_WSLConfinesRoot(t *testing.T) {
	ws := t.TempDir()
	tool := NewSearchFilesWithPolicy(sandbox.Policy{Mode: sandbox.ModeWSL, Workspace: ws})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "x", "path": `..\..`})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("traversal outside workspace must fail, got:\n%s", res.Content)
	}
}

func TestSearch_InvalidRegexIsSoftError(t *testing.T) {
	dir := t.TempDir()
	tool := NewSearchFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "([", "path": dir, "regex": "true"})
	if err != nil {
		t.Fatalf("invalid regex must be soft error, got hard: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "invalid regex") {
		t.Fatalf("want invalid-regex soft error, got:\n%s", res.Content)
	}
}

func TestSearch_RegexAndIncludeAndCase(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "func Foo() {}\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "func Foo() {}\n")

	tool := NewSearchFiles()
	res, err := tool.Execute(context.Background(), map[string]any{
		"pattern": `func\s+\w+\(\)`, "path": dir, "regex": "true", "include": "*.go",
	})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "a.go") || strings.Contains(res.Content, "b.txt") {
		t.Fatalf("include glob not honored, got:\n%s", res.Content)
	}

	res, err = tool.Execute(context.Background(), map[string]any{
		"pattern": "FUNC FOO", "path": dir, "case_insensitive": "true",
	})
	if err != nil || res.IsError {
		t.Fatalf("Execute: %v %v", err, res.Content)
	}
	if !strings.Contains(res.Content, "a.go") {
		t.Fatalf("case-insensitive miss, got:\n%s", res.Content)
	}
}

func TestSearch_MatchCapTruncates(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < 150; i++ {
		b.WriteString("MARKER_MANY line\n")
	}
	writeFile(t, filepath.Join(dir, "big.go"), b.String())

	tool := NewSearchFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_MANY", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "truncated") {
		t.Fatalf("expected truncation marker, got:\n%.500s", res.Content)
	}
}

func TestSearch_SkipsOversizeFiles(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, maxFileBytes+100)
	for i := range big {
		big[i] = 'x'
	}
	copy(big, "MARKER_BIG")
	writeFile(t, filepath.Join(dir, "huge.bin"), string(big))
	writeFile(t, filepath.Join(dir, "small.go"), "nothing\n")

	tool := NewSearchFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_BIG", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(res.Content, "huge.bin") {
		t.Fatalf("oversize file must be skipped, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "skipped") {
		t.Fatalf("expected skip note, got:\n%s", res.Content)
	}
}

func TestSearch_EmptyPatternIsHardError(t *testing.T) {
	tool := NewSearchFiles()
	if _, err := tool.Execute(context.Background(), map[string]any{"pattern": "  "}); err == nil {
		t.Fatalf("empty pattern must be a hard error")
	}
}

func TestSearch_MissingPatternArgIsHardError(t *testing.T) {
	tool := NewSearchFiles()
	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatalf("missing pattern must be a hard error")
	}
}
