package filesystem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

// TestListFiles_TruncatesHugeDirectories pins the entry bound: bounded
// results, a model-visible marker naming shown/total, and structured
// metadata — instead of an unbounded dump.
func TestListFiles_TruncatesHugeDirectories(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 12; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("file-%02d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l := NewListFiles()
	l.SetLimits(tools.Limits{MaxLines: 5})

	result, err := l.Execute(context.Background(), map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("IsError = true: %s", result.Content)
	}
	lines := strings.Split(strings.TrimSpace(strings.SplitN(result.Content, "\n\n[", 2)[0]), "\n")
	if len(lines) != 5 {
		t.Errorf("shown lines = %d, want 5", len(lines))
	}
	if !strings.Contains(result.Content, "truncated at 5 of 12 entries") {
		t.Errorf("content lacks truncation marker:\n%s", result.Content)
	}
	if result.Metadata == nil || result.Metadata["truncated"] != true ||
		result.Metadata["shown_entries"] != 5 || result.Metadata["total_entries"] != 12 {
		t.Errorf("Metadata = %v, want truncation record", result.Metadata)
	}
}

// TestListFiles_SmallDirectoryUnchanged pins that normal listings carry
// no marker and no metadata.
func TestListFiles_SmallDirectoryUnchanged(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := NewListFiles()
	result, err := l.Execute(context.Background(), map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(result.Content, "truncated") {
		t.Errorf("small listing has a marker: %q", result.Content)
	}
	if result.Metadata != nil {
		t.Errorf("Metadata = %v, want nil", result.Metadata)
	}
	if strings.TrimSpace(result.Content) != "a.txt" {
		t.Errorf("content = %q, want a.txt", result.Content)
	}
}

// TestReadFile_OverrideRefusal pins that a configured byte bound replaces
// the 5 MiB default refusal threshold, keeping the refusal wording.
func TestReadFile_OverrideRefusal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, make([]byte, 3000), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewReadFile()
	r.SetLimits(tools.Limits{MaxBytes: 100})
	result, err := r.Execute(context.Background(), map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.IsError {
		t.Fatal("IsError = false, want refusal for over-limit file")
	}
	if !strings.Contains(result.Content, "exceeds the 100 byte limit") {
		t.Errorf("content = %q, want the 100-byte refusal", result.Content)
	}
	if result.Metadata["limit_bytes"] != int64(100) && result.Metadata["limit_bytes"] != 100 {
		t.Errorf("Metadata = %v, want limit_bytes 100", result.Metadata)
	}
}

// TestReadFile_DefaultStillReadsNormalFiles pins the unchanged default
// path (5 MiB refusal threshold, full content under it).
func TestReadFile_DefaultStillReadsNormalFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewReadFile()
	if got := r.ToolLimits().MaxBytes; got != tools.DefaultReadMaxBytes {
		t.Errorf("default MaxBytes = %d, want %d", got, tools.DefaultReadMaxBytes)
	}
	result, err := r.Execute(context.Background(), map[string]any{"path": path})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.IsError || result.Content != "hello" {
		t.Errorf("result = %+v, want hello", result)
	}
}
