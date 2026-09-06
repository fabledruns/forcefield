package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSearch_SkipsDefaultExcludedDirs pins the fixed exclusion set:
// dependency trees and build output are never descended into, at any
// depth, so a repository walk stays bounded and noise-free.
func TestSearch_SkipsDefaultExcludedDirs(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{".git", "node_modules", "dist", "build", "target", "vendor", ".next", "__pycache__"} {
		writeFile(t, filepath.Join(dir, d, "nested", "deep.txt"), "MARKER_EXCLUDED\n")
	}
	writeFile(t, filepath.Join(dir, "src", "code.go"), "nothing here\n")

	tool := NewSearchFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_EXCLUDED", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "no matches") {
		t.Fatalf("excluded dirs must be pruned, got:\n%s", res.Content)
	}
	for _, d := range []string{"node_modules", "dist", "build", "target", "vendor", ".next", "__pycache__"} {
		if strings.Contains(res.Content, d) {
			t.Errorf("excluded dir %q leaked into output:\n%s", d, res.Content)
		}
	}
}

// TestSearch_SkipsLockFiles pins that dependency lockfiles are not
// content-searched, while neighboring normal files still are.
func TestSearch_SkipsLockFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Cargo.lock"), "MARKER_LOCKED\n")
	writeFile(t, filepath.Join(dir, "poetry.lock"), "MARKER_LOCKED\n")
	writeFile(t, filepath.Join(dir, "main.go"), "MARKER_LOCKED\n")

	tool := NewSearchFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_LOCKED", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(res.Content, ".lock") {
		t.Fatalf("lockfiles must be skipped, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "main.go") {
		t.Fatalf("normal file must match, got:\n%s", res.Content)
	}
}

// TestSearch_SkipsBinaryFiles pins NUL-byte detection: binaries never
// match, and the skip is reported instead of silent.
func TestSearch_SkipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	bin := append([]byte("MARKER_BINARY\x00\x01\x02"), make([]byte, 1000)...)
	if err := os.WriteFile(filepath.Join(dir, "app.bin"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "notes.txt"), "nothing here\n")

	tool := NewSearchFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_BINARY", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "no matches") {
		t.Fatalf("binary must not match, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "binary") {
		t.Fatalf("binary skip must be reported, got:\n%s", res.Content)
	}
}

// TestSearch_BinaryAndLargeNotesCombine pins that both skip notes can
// appear together without disturbing matches.
func TestSearch_BinaryAndLargeNotesCombine(t *testing.T) {
	dir := t.TempDir()
	bin := append([]byte("x\x00y"), make([]byte, 100)...)
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, maxFileBytes+10)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(filepath.Join(dir, "b.dat"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "c.txt"), "MARKER_HERE\n")

	tool := NewSearchFiles()
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_HERE", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "c.txt") {
		t.Fatalf("normal match missing, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "binary files skipped") {
		t.Errorf("binary note missing, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "skipped") {
		t.Errorf("large-file note missing, got:\n%s", res.Content)
	}
}
