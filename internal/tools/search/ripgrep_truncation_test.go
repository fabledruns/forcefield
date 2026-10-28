package search

// search_code "truncation made visible" regression tests (A1): rg-path
// success shapes must lead with a truthful head summary (row cap with total
// found, writer cut with actual bytes produced), carry both-size tail notes,
// and gain the read_file escape hatch on zero-row results. Authored against
// the PRE-FIX baseline: the baseline has no head summary, its row-cap note
// omits the total-found count, and its byte-cap note omits "bytes produced".

import (
	"context"
	"strings"
	"testing"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// rgContentLine1 returns the first line of a result content string.
func rgContentLine1(content string) string {
	line := content
	if i := strings.Index(content, "\n"); i >= 0 {
		line = content[:i]
	}
	return line
}

// rgLongLineFile writes a file with count lines of size lineLen, all
// containing the needle at the head of the line.
func rgLongLineFile(t *testing.T, path, needle string, count, lineLen int) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < count; i++ {
		b.WriteString(needle)
		b.WriteString(strings.Repeat("a", lineLen-len(needle)))
		b.WriteString("\n")
	}
	writeSearchCodeFile(t, path, b.String())
}

// TestSearchCode_A1RowCapHeadSummaryShowsTotalFound pins the pure row-cap
// shape: content STARTS with "[search truncated: showing first %d of %d
// matches; ...]" where the second number is the total found before the cap,
// and the tail note is the "%d of %d" form. Baseline: no head summary, and
// the tail note omits the total.
func TestSearchCode_A1RowCapHeadSummaryShowsTotalFound(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	rgLongLineFile(t, dir+"/big.go", "MARKER_MANY", 150, 24)

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	tool.SetLimits(tools.Limits{MaxLines: 10})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_MANY", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("row cap is a success shape, got IsError: %s", res.Content)
	}
	wantLine1 := "[search truncated: showing first 10 of 150 matches; narrow pattern, path, or include glob]"
	if got := rgContentLine1(res.Content); got != wantLine1 {
		t.Fatalf("row-cap head summary:\n got: %q\nwant: %q", got, wantLine1)
	}
	if !strings.Contains(res.Content, "[output truncated: showing 10 of 150 matches; narrow pattern, path, or include glob]") {
		t.Fatalf("row-cap tail note must carry both kept and total counts, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "big.go:1:1: MARKER_MANY") {
		t.Fatalf("rows must follow the summary, got:\n%s", res.Content)
	}
}

// TestSearchCode_A1RowCapMetadataFrozen pins that truncation metadata keeps
// exactly the frozen key set (truncated/matches/match_limit/rg_used, no more,
// no fewer) for the pure row-cap shape. Parity pin at baseline; the redesign
// may not grow the schema.
func TestSearchCode_A1RowCapMetadataFrozen(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	rgLongLineFile(t, dir+"/big.go", "MARKER_META", 150, 24)

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	tool.SetLimits(tools.Limits{MaxLines: 10})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_META", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	meta := res.Metadata
	if meta == nil {
		t.Fatalf("row cap must carry truncation metadata")
	}
	if len(meta) != 4 {
		t.Fatalf("metadata schema is frozen: len = %d (%v), want exactly 4 keys", len(meta), meta)
	}
	if meta["truncated"] != true || meta["matches"] != 10 || meta["match_limit"] != 10 || meta["rg_used"] != true {
		t.Fatalf("metadata values drifted: %v", meta)
	}
}

// TestSearchCode_A1WriterCapHeadSummaryAndTailNote pins the writer-cut shape:
// line 1 reports the configured cap and the ACTUAL bytes produced plus rows
// shown so far, and the tail note adopts the "%d bytes, %d bytes produced"
// form. Fixture: five 3000-byte match lines with the JSON reader cap set so
// exactly the first event parses before the cut. Baseline: no head summary
// and the tail note omits "bytes produced".
func TestSearchCode_A1WriterCapHeadSummaryAndTailNote(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	rgLongLineFile(t, dir+"/long.go", "NEEDLEWRITER", 5, 3000)

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	tool.SetLimits(tools.Limits{MaxBytes: 4000})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "NEEDLEWRITER", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("writer cap is a success shape, got IsError: %s", res.Content)
	}
	line1 := rgContentLine1(res.Content)
	if !strings.HasPrefix(line1, "[search truncated: rg output capped at 4000 bytes, ") {
		t.Fatalf("writer-cut head summary missing, got line 1: %q", line1)
	}
	if !strings.Contains(line1, " bytes produced; 1 matches shown so far; more may exist; narrow pattern, path, or include glob]") {
		t.Fatalf("writer-cut head summary must report actual bytes produced and rows shown, got: %q", line1)
	}
	if !strings.Contains(res.Content, "[...rg output truncated at 4000 bytes, ") {
		t.Fatalf("writer-cut tail note must adopt the both-sizes form, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "long.go:1:1: NEEDLEWRITER") {
		t.Fatalf("the parsed row must survive, got:\n%s", res.Content)
	}
	meta := res.Metadata
	if meta == nil || meta["truncated"] != true {
		t.Fatalf("writer cut must carry truncation metadata, got %v", meta)
	}
	if got, ok := meta["kept_bytes"].(int); !ok || got != 4000 {
		t.Fatalf("kept_bytes = %v, want the 4000-byte reader cap", meta["kept_bytes"])
	}
	if got, ok := meta["limit_bytes"].(int); !ok || got != 4000 {
		t.Fatalf("limit_bytes = %v, want 4000", meta["limit_bytes"])
	}
	if got, ok := meta["original_bytes"].(int); !ok || got <= 4000 {
		t.Fatalf("original_bytes = %v, want the actual rg output size over the cap", meta["original_bytes"])
	}
}

// TestSearchCode_A1ZeroRowExitOneReadFileAdvice pins the zero-row no-cut
// shape: ripgrep's exit-1 no-match note gains the read_file escape hatch
// clause. Baseline: the note stops at the echo of the pattern.
func TestSearchCode_A1ZeroRowExitOneReadFileAdvice(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	writeSearchCodeFile(t, dir+"/ok.go", "package ok\n")

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MISSING_ZQ9_PHRASE", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("no-match must stay a success shape, got IsError: %s", res.Content)
	}
	if !strings.Contains(res.Content, `no matches for "MISSING_ZQ9_PHRASE" under`) {
		t.Fatalf("zero-row note lost its no-match wording, got:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "(files over 512 KiB are not searched; if a match may be inside one, use read_file)") {
		t.Fatalf("zero-row note must gain the read_file advice, got:\n%s", res.Content)
	}
}

// TestSearchCode_A1ZeroRowWriterCapZeroRowsShown pins the zero-rows-with-cut
// shape (events never produced rows): the no-match note plus an explicit
// byte-cap summary naming "0 matches shown so far". Baseline: only the vague
// "(output exceeded the byte limit)" suffix exists.
func TestSearchCode_A1ZeroRowWriterCapZeroRowsShown(t *testing.T) {
	requireRg(t)
	dir := t.TempDir()
	rgLongLineFile(t, dir+"/zcap.txt", "NEEDLEZC eight", 1, 280)

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	tool.SetLimits(tools.Limits{MaxBytes: 100})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "NEEDLEZC", "path": dir})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("zero-rows-with-cut is a success shape, got IsError: %s", res.Content)
	}
	if !strings.Contains(res.Content, "(files over 512 KiB are not searched; if a match may be inside one, use read_file)") {
		t.Fatalf("zero-row note with cut must gain the read_file advice, got:\n%s", res.Content)
	}
	// Spec order for the zero-rows-with-cut shape: the byte-truth
	// summary is the FIRST line, the no-matches sentence follows it.
	lines := strings.Split(res.Content, "\n")
	if len(lines) < 2 {
		t.Fatalf("zero-rows-with-cut must add an explicit summary line plus the no-matches sentence, got:\n%s", res.Content)
	}
	summary := lines[0]
	if !strings.HasPrefix(summary, "[search truncated: rg output capped at 100 bytes, ") ||
		!strings.Contains(summary, "0 matches shown so far") {
		t.Fatalf("first line = %q, want the byte-cap summary leading with 0 matches shown so far", summary)
	}
}

// TestSearchCode_A1FallbackProvenanceStaysFirstLine pins that the rg-absent
// fallback keeps "[note: rg not found on PATH; used built-in search]" as line
// 1 and the walker's row-cap summary becomes line 2 (summaries after the
// provenance note). Baseline: line 1 is the note (parity) and line 2 is a
// match row (RED).
func TestSearchCode_A1FallbackProvenanceStaysFirstLine(t *testing.T) {
	stubMissingRg(t)
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		b.WriteString("MARKER_FBK\n")
	}
	writeSearchCodeFile(t, dir+"/fallback.go", b.String())

	tool := NewSearchCodeWithPolicy(sandbox.Policy{Workspace: dir})
	tool.SetLimits(tools.Limits{MaxLines: 5})
	res, err := tool.Execute(context.Background(), map[string]any{"pattern": "MARKER_FBK", "path": dir})
	if err != nil {
		t.Fatalf("fallback must not hard-fail, got: %v", err)
	}
	if res.IsError {
		t.Fatalf("fallback row-cap is a success shape, got error: %s", res.Content)
	}
	lines := strings.Split(res.Content, "\n")
	if lines[0] != "[note: rg not found on PATH; used built-in search]" {
		t.Fatalf("provenance note must remain line 1, got %q", lines[0])
	}
	wantLine2 := "[search truncated: showing first 5 matches; walk stopped after 1 files; more matches may exist; narrow pattern, path, or include glob]"
	if len(lines) < 2 || lines[1] != wantLine2 {
		t.Fatalf("row-cap summary must be line 2 under the provenance note:\n got: %q\nwant: %q", lines[1], wantLine2)
	}
	if res.Metadata == nil || res.Metadata["rg_used"] != false || res.Metadata["fallback"] != true {
		t.Fatalf("fallback metadata must stay, got %v", res.Metadata)
	}
}
