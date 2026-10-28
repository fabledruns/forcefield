package search

// search_files walker "truncation made visible" regression tests (A1):
// row-cap and files-cap events must surface a truthful head summary on line 1
// with ACTUAL counts, tail notes must carry actual counts, and zero-match /
// large-file phrasing gains the read_file escape hatch. Authored against the
// PRE-FIX baseline: the baseline feeds the notes the constants (100 matches /
// 2000 files) instead of actuals and has no head summary at all.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// buildRowCapFixture writes files made only of short matching lines and
// returns the directory.
func buildRowCapFixture(t *testing.T, dir string, files map[string]int) {
	t.Helper()
	for name, lines := range files {
		var b strings.Builder
		for i := 1; i <= lines; i++ {
			fmt.Fprintf(&b, "MARKER_WALK %d %s\n", i, strings.Repeat("p", 12))
		}
		writeFile(t, filepath.Join(dir, name), b.String())
	}
}

// walkerContentLine1 returns the first line of the result content.
func walkerContentLine1(content string) string {
	line := content
	if i := strings.Index(content, "\n"); i >= 0 {
		line = content[:i]
	}
	return line
}

// TestWalker_A1RowCapHeadSummaryAndActualCounts pins that a full row cap adds
// a truthful line-1 summary (kept rows, actual files visited) and that the
// tail note carries actual counts. Baseline: line 1 is a match row and the
// tail note hardcodes "2000 files".
func TestWalker_A1RowCapHeadSummaryAndActualCounts(t *testing.T) {
	dir := t.TempDir()
	buildRowCapFixture(t, dir, map[string]int{"big.go": 150})

	tool := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: dir})
	tool.SetLimits(tools.Limits{MaxLines: 10})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_WALK", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("row cap is a success shape, got IsError: %s", res.Content)
	}
	line1 := walkerContentLine1(res.Content)
	wantLine1 := "[search truncated: showing first 10 matches; walk stopped after 1 files; more matches may exist; narrow pattern, path, or include glob]"
	if line1 != wantLine1 {
		t.Fatalf("row-cap line 1:\n got: %q\nwant: %q", line1, wantLine1)
	}
	wantNote := "[output truncated at 10 matches / 1 files visited; narrow pattern, path, or include glob]"
	if !strings.Contains(res.Content, wantNote) {
		t.Fatalf("row-cap tail note must carry ACTUAL counts (1 file visited), missing %q in:\n%s", wantNote, res.Content)
	}
	if !strings.Contains(res.Content, "big.go:1: MARKER_WALK 1") {
		t.Fatalf("rows remain under the summary, got:\n%s", res.Content)
	}
}

// TestWalker_A1RowCapTailNoteActualFiles pins the multi-file row-cap path:
// 50 + 40 matches from the first two files, the cap fires while scanning the
// third, so "files visited" is the actual 3 and the note says so.
func TestWalker_A1RowCapTailNoteActualFiles(t *testing.T) {
	dir := t.TempDir()
	buildRowCapFixture(t, dir, map[string]int{"a1.go": 50, "a2.go": 40, "a3.go": 20})

	tool := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_WALK", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	wantLine1 := "[search truncated: showing first 100 matches; walk stopped after 3 files; more matches may exist; narrow pattern, path, or include glob]"
	if got := walkerContentLine1(res.Content); got != wantLine1 {
		t.Fatalf("row-cap line 1:\n got: %q\nwant: %q", got, wantLine1)
	}
	if !strings.Contains(res.Content, "[output truncated at 100 matches / 3 files visited; narrow pattern, path, or include glob]") {
		t.Fatalf("tail note must report the actual 3 files visited, got:\n%s", res.Content)
	}
}

// buildFileCapFixture writes ~2006 non-matching files plus six matching files
// that sort first, so a walk over the directory exceeds maxFilesVisited with
// matches intact.
func buildFileCapFixture(t *testing.T) string {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "aa_match1.txt"), "needlecapfile 1\n")
	writeFile(t, filepath.Join(dir, "aa_match2.txt"), "needlecapfile 2\n")
	writeFile(t, filepath.Join(dir, "aa_match3.txt"), "needlecapfile 3\n")
	writeFile(t, filepath.Join(dir, "aa_match4.txt"), "needlecapfile 4\n")
	writeFile(t, filepath.Join(dir, "aa_match5.txt"), "needlecapfile 5\n")
	writeFile(t, filepath.Join(dir, "aa_match6.txt"), "needlecapfile 6\n")
	for i := 1; i <= 2002; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("f%04d", i)), "no match here\n")
	}
	return dir
}

// TestWalker_A1FileCapZeroMatchesStillSignals pins the silent-silence bug:
// when the file cap stops the walk and nothing matched, the result must say
// so explicitly (second summary line) instead of a bare "no matches".
// Baseline: the zero-match branch swallows the truncated flag.
func TestWalker_A1FileCapZeroMatchesStillSignals(t *testing.T) {
	dir := buildFileCapFixture(t)
	tool := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "needleZABSENT", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("file-cap zero-match is a success shape, got IsError: %s", res.Content)
	}
	if !strings.Contains(res.Content, `[search truncated: walk stopped after 2001 files (cap 2000); matches may exist beyond; narrow the path]`) {
		t.Fatalf("file-cap zero-match must carry an explicit truncation signal, got:\n%s", res.Content)
	}
	if res.Metadata != nil {
		t.Fatalf("file-cap zero-match metadata must stay nil, got %v", res.Metadata)
	}
}

// TestWalker_A1FileCapWithMatchesSummaryLineOne pins the match-bearing file
// cap: line 1 is a truthful summary ("showing 6 matches; walked 2001 files"),
// rows follow, and the metadata count is the actual 2001 (parity for the
// counter itself).
func TestWalker_A1FileCapWithMatchesSummaryLineOne(t *testing.T) {
	dir := buildFileCapFixture(t)
	tool := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "needlecapfile", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("file-cap with matches is a success shape, got IsError: %s", res.Content)
	}
	want := "[search truncated: showing 6 matches; walk stopped after 2001 files (cap 2000); more matches may exist; narrow the path]"
	if got := walkerContentLine1(res.Content); got != want {
		t.Fatalf("file-cap line 1:\n got: %q\nwant: %q", got, want)
	}
	if !strings.Contains(res.Content, "aa_match6.txt:1: needlecapfile 6") {
		t.Fatalf("match rows must survive, got:\n%s", res.Content)
	}
	if res.Metadata == nil || res.Metadata["truncated"] != true || res.Metadata["matches"] != 6 {
		t.Fatalf("truncation metadata must stay truth-shape, got %v", res.Metadata)
	}
	if got, ok := res.Metadata["files_visited"].(int); !ok || got != 2001 {
		t.Fatalf("metadata files_visited = %v, want actual 2001", res.Metadata["files_visited"])
	}
}

// TestWalker_A1LargeFileZeroMatchesAdvice pins the extended zero-row
// large-file parenthetical with the read_file escape hatch. Baseline: the
// note ends at "skipped)".
func TestWalker_A1LargeFileZeroMatchesAdvice(t *testing.T) {
	dir := t.TempDir()
	huge := make([]byte, maxFileBytes+100)
	for i := range huge {
		huge[i] = 'x'
	}
	writeFile(t, filepath.Join(dir, "huge.bin"), string(huge))
	writeFile(t, filepath.Join(dir, "notes.txt"), "nothing here\n")

	tool := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MISSING_PHRASE_XYZ", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "no matches for") {
		t.Fatalf("zero-match note expected, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "(1 large files skipped; if a match may be inside one, use read_file)") {
		t.Fatalf("zero-row large-file note must gain the read_file advice, got:\n%s", res.Content)
	}
}

// TestWalker_A1LargeFileTailNoteAdviceWithMatches pins the tail note variant:
// rows exist (so no zero-row branch) and the skipped-large note becomes the
// bracketed tail advice form. Baseline: "[1 files over 512 KiB skipped]".
func TestWalker_A1LargeFileTailNoteAdviceWithMatches(t *testing.T) {
	dir := t.TempDir()
	huge := make([]byte, maxFileBytes+100)
	for i := range huge {
		huge[i] = 'x'
	}
	copy(huge, "NEEDLE_HUGE") // skipped either way; content irrelevant
	writeFile(t, filepath.Join(dir, "huge.bin"), string(huge))
	writeFile(t, filepath.Join(dir, "small.go"), "NEEDLE_HUGE row\n")

	tool := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "NEEDLE_HUGE", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "small.go:1: NEEDLE_HUGE row") {
		t.Fatalf("rows must be present, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "[1 files over 512 KiB skipped; if a match may be inside one, use read_file]") {
		t.Fatalf("tail note with rows must carry the read_file advice, got:\n%s", res.Content)
	}
}

// TestWalker_A1ZeroMatchesBinaryNoteUnchanged is the parity pin on the
// redesign's unchanged binary note (binary phrasing stays as today).
func TestWalker_A1ZeroMatchesBinaryNoteUnchanged(t *testing.T) {
	dir := t.TempDir()
	bin := append([]byte("MARKER_BINARY\x00\x01"), make([]byte, 100)...)
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), bin, 0o644); err != nil {
		t.Fatalf("write binary fixture: %v", err)
	}
	tool := NewSearchFilesWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_BINARY", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "(1 binary files skipped)") {
		t.Fatalf("binary note must remain unchanged, got:\n%s", res.Content)
	}
}
