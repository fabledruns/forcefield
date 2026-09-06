package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/tools"
)

func writeFiles(t *testing.T, dir string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%02d.txt", i)), []byte("needle haystack\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSearchFiles_MatchOverrideBoundsOutput pins that a configured match
// bound replaces the 100-match default, with the marker naming the live
// bound and structured metadata attached.
func TestSearchFiles_MatchOverrideBoundsOutput(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, 10)
	s := NewSearchFiles()
	s.SetLimits(tools.Limits{MaxLines: 4})

	result, err := s.Execute(context.Background(), map[string]any{"pattern": "needle", "path": dir})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result.Content, "[output truncated at 4 matches") {
		t.Errorf("content lacks override marker:\n%s", result.Content)
	}
	if result.Metadata == nil || result.Metadata["truncated"] != true || result.Metadata["match_limit"] != 4 {
		t.Errorf("Metadata = %v, want truncation record with limit 4", result.Metadata)
	}
}

// TestSearchFiles_DefaultMatchBoundUnchanged pins the 100-match default
// path still reports its established marker.
func TestSearchFiles_DefaultMatchBoundUnchanged(t *testing.T) {
	if got := NewSearchFiles().ToolLimits().MaxLines; got != tools.DefaultSearchMaxLines {
		t.Errorf("default MaxLines = %d, want %d", got, tools.DefaultSearchMaxLines)
	}
	dir := t.TempDir()
	writeFiles(t, dir, 3)
	s := NewSearchFiles()
	result, err := s.Execute(context.Background(), map[string]any{"pattern": "needle", "path": dir})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(result.Content, "truncated") {
		t.Errorf("small search has a marker: %q", result.Content)
	}
	if result.Metadata != nil {
		t.Errorf("Metadata = %v, want nil", result.Metadata)
	}
}
