package runtime

// End-to-end proof that the model-visible tool result respects the redesigned
// session window (B1) for oversized search output: a scripted provider
// observes the messages sent to the model after one search tool call. Two
// scenarios from one fixture family:
//
//   (a) "precious tail": >6000 bytes of rows, <= 100 matches (no row cap), the
//       alphabetically-LAST file's match row lands at the very end of the tool
//       content. After the redesigned truncation the tail window must keep
//       that row reachable by the model. Baseline: head-only cut erases it.
//   (b) "row-cap signal": >100 matches plus an alphabetically-LAST precious
//       row. The tool itself never emits that row (row cap), but it must emit
//       an explicit omission signal ("[search truncated:" head summary and/or
//       "[output truncated:" tail note), and B1's retention must carry the
//       signal through to the provider message. Baseline: the end-attached
//       note is cut away, so the provider sees neither.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forcefield/internal/agent"
	"forcefield/internal/providers"
	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
	"forcefield/internal/tools/search"
)

const (
	truncRowLen      = 680            // bytes of padding after the 'needle ' prefix
	truncPreciousRow = "precioustail" // the unique marker inside zz_precious.txt
)

// writeTruncFixtureFile writes a fixture file, creating parent directories
// (the search fixtures live in subdirectories of the workspace root).
func writeTruncFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("fixture mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("fixture write %s: %v", path, err)
	}
}

// buildTruncationFixture builds the shared fixture family once per test:
// tail/ with nine one-match files of ~700-byte rows followed (alphabetically)
// by the short precious file, and rowcap/ with 150 matches (3 files x 50 rows)
// plus the same last precious file.
func buildTruncationFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// (a) precious tail: enough ~700-byte rows to push the content past the
	// window with no row cap (10 matches < 100).
	tail := filepath.Join(root, "tail")
	for i := 1; i <= 9; i++ {
		name, line := fmt.Sprintf("p%02d.txt", i),
			fmt.Sprintf("needle tailrow=p%02d %s\n", i, strings.Repeat("t", truncRowLen))
		writeTruncFixtureFile(t, filepath.Join(tail, name), line)
	}
	writeTruncFixtureFile(t, filepath.Join(tail, "zz_precious.txt"),
		"needle "+truncPreciousRow+" zz_mark "+strings.Repeat("x", 230)+"\n")

	// (b) row-cap signal: 150 matches of ~700-byte rows; the row cap keeps
	// only the first 100 sorted rows, so the precious row is never emitted.
	rowcap := filepath.Join(root, "rowcap")
	for i := 1; i <= 3; i++ {
		var b strings.Builder
		for j := 1; j <= 50; j++ {
			fmt.Fprintf(&b, "needle b%drow=%02d %s\n", i, j, strings.Repeat("r", truncRowLen))
		}
		writeTruncFixtureFile(t, filepath.Join(rowcap, fmt.Sprintf("b%d.txt", i)), b.String())
	}
	writeTruncFixtureFile(t, filepath.Join(rowcap, "zz_precious.txt"),
		"needle "+truncPreciousRow+" zz_mark "+strings.Repeat("x", 230)+"\n")

	return root
}

func newTruncRuntime(t *testing.T, root string, p *scriptedProvider) *Runtime {
	t.Helper()
	mgr := tools.NewManager(tools.NewRegistry())
	pol := sandbox.Policy{Workspace: root}
	if err := mgr.Register(search.NewSearchFilesWithPolicy(pol)); err != nil {
		t.Fatalf("Register search_files: %v", err)
	}
	if err := mgr.Register(search.NewSearchCodeWithPolicy(pol)); err != nil {
		t.Fatalf("Register search_code: %v", err)
	}
	return &Runtime{
		provider:  p,
		agent:     agent.New("test", "system", ""),
		manager:   mgr,
		scheduler: newScheduler(mgr, nil, nil, DefaultSchedulerConfig),
	}
}

// truncProviderVisible runs one scripted run (search turn + closing turn) and
// returns the provider-visible tool-result message content that the model
// received on the search turn.
func truncProviderVisible(t *testing.T, toolName, subdir string) string {
	t.Helper()
	args := map[string]any{"pattern": "needle", "path": subdir}
	p := &scriptedProvider{turns: [][]providers.StreamEvent{
		{
			{Text: "searching."},
			{ToolCalls: []providers.ToolCall{{ID: "call-1", Name: toolName, Arguments: args}}},
			{Done: true},
		},
		{
			{Text: "synthesized from search."},
			{Done: true},
		},
	}}
	root := buildTruncationFixture(t)
	rt := newTruncRuntime(t, root, p)
	if _, err := rt.Run([]providers.Message{{Role: providers.UserRole, Content: "find the needle"}}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if p.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", p.calls)
	}
	msgs := p.messages[1]
	last := msgs[len(msgs)-1]
	if last.Role != providers.ToolRole {
		t.Fatalf("last provider message role = %v, want tool", last.Role)
	}
	return last.Content
}

// truncPreciousChecks holds the per-scenario assertions shared by the four
// e2e tests: the window stays inside the budget past the fence, and the
// precious row either reaches the model (a) or stays honestly dropped (b).
func truncPreciousChecks(t *testing.T, label, got, headMarker string, wantPreciousRow bool) {
	t.Helper()
	if !strings.Contains(got, headMarker) {
		t.Fatalf("%s: head rows must stay reachable (parity), missing %q in:\n%s…", label, headMarker, got[:minLen(len(got), 400)])
	}
	// Fence adds <= ~50 bytes around a <= 6000-byte window; the baseline
	// is ~6175+ bytes because its marker rides AFTER the 6000-byte head.
	if wantPreciousRow {
		if !strings.Contains(got, "zz_precious.txt:") {
			t.Fatalf("%s: the alphabetically-last precious row was cut away from the model-visible result:\n…%s", label, got[maxI(len(got)-600, 0):])
		}
		if !strings.Contains(got, truncPreciousRow) {
			t.Fatalf("%s: precious row content mark missing from the model-visible result", label)
		}
	} else {
		if strings.Contains(got, "zz_precious.txt:") || strings.Contains(got, truncPreciousRow) {
			t.Fatalf("%s: row-capped result must not resurrect the dropped precious row", label)
		}
	}
	// Fence adds <= ~50 bytes around a <= 6000-byte window; the baseline
	// is ~6180 bytes because its marker rides AFTER the 6000-byte head.
	if len(got) > 6100 {
		t.Fatalf("%s: provider-visible tool result = %d bytes; the designed window keeps every marker inside the 6000-byte budget", label, len(got))
	}
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TestTruncationE2E_PreciousTailReachesModelViaSearchFiles proves (a) with the
// walker: the zz_precious row (content end, outside any row cap) survives the
// designed head+tail window into the provider message. RED at baseline.
func TestTruncationE2E_PreciousTailReachesModelViaSearchFiles(t *testing.T) {
	got := truncProviderVisible(t, "search_files", "tail")
	truncPreciousChecks(t, "search_files/tail", got, "p05.txt:1:", true)
}

// TestTruncationE2E_PreciousTailReachesModelViaSearchCode proves (a) with
// search_code: rg sorts the zz_precious row last exactly like the walker's
// lexical walk, so the tail window must keep it. RED at baseline.
func TestTruncationE2E_PreciousTailReachesModelViaSearchCode(t *testing.T) {
	got := truncProviderVisible(t, "search_code", "tail")
	truncPreciousChecks(t, "search_code/tail", got, "p05.txt:1:", true)
}

// TestTruncationE2E_RowCapSignalReachesModelViaSearchCode proves (b) with
// search_code: the provider-visible message must carry an explicit truthful
// omission signal that survives the designed window.
func TestTruncationE2E_RowCapSignalReachesModelViaSearchCode(t *testing.T) {
	got := truncProviderVisible(t, "search_code", "rowcap")
	// The explicit omission signal is the (b)-scenario's own regression:
	// check it before the helper's window guard so the RED reason is the
	// signal, not the length overflow.
	hasHead := strings.Contains(got, "[search truncated:")
	hasTail := strings.Contains(got, "[output truncated:")
	if !hasHead && !hasTail {
		t.Fatalf("fence cut removed both the head summary and the tail note: the model got rows with no truthful truncation signal:\n%s…", got[:minLen(len(got), 600)])
	}
	truncPreciousChecks(t, "search_code/rowcap", got, "b1.txt:1:", false)
}

// TestTruncationE2E_RowCapSignalReachesModelViaSearchFiles proves (b) with the
// walker: same fixture family, the row cap stops the walk early (files cap
// never fires), and the summary must reach the provider message.
func TestTruncationE2E_RowCapSignalReachesModelViaSearchFiles(t *testing.T) {
	got := truncProviderVisible(t, "search_files", "rowcap")
	if !strings.Contains(got, "[search truncated:") && !strings.Contains(got, "[output truncated:") {
		t.Fatalf("walker row-cap signal did not reach the model through the window:\n%s…", got[:minLen(len(got), 600)])
	}
	truncPreciousChecks(t, "search_files/rowcap", got, "b1.txt:1:", false)
}
