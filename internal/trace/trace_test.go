package trace

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open trace: %v", err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 4*1024*1024)
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("line is not valid JSON: %v (%q)", err, sc.Text())
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

func TestDisabledRecordsNothing(t *testing.T) {
	dir := t.TempDir()
	tr := New(false, dir)
	if tr.Enabled() {
		t.Fatal("disabled tracer reports enabled")
	}
	if r := tr.StartRun("x", RunMeta{}); r != nil {
		t.Fatal("disabled StartRun returned a run")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("disabled tracer created files: %v", entries)
	}
	var nilRun *Run
	nilRun.Close() // must not panic
	nilRun.TurnStart(1, 0, 0)
}

func TestRunLifecycleLines(t *testing.T) {
	dir := t.TempDir()
	tr := New(true, dir)
	r := tr.StartRun("run-1", RunMeta{Provider: "p", Model: "m", Agent: "general", MessageCount: 3})
	if r == nil {
		t.Fatal("StartRun returned nil")
	}
	r.TurnStart(1, 4, 100)
	r.ToolCall("c1", "shell", map[string]any{"command": "echo hi"})
	r.ToolResult("c1", "shell", "finish", true, 5*time.Millisecond, 1, intp(0), "hi", "")
	r.TurnEnd(1, "stop", 10, 5, 15, 1, time.Millisecond)
	r.RunDone("verified", 2)
	r.Close()
	r.Close() // idempotent

	lines := readLines(t, filepath.Join(dir, "run-1.jsonl"))
	wantTypes := []string{"run_start", "turn_start", "tool_start", "tool_end", "turn_end", "run_done"}
	if len(lines) != len(wantTypes) {
		t.Fatalf("lines = %d, want %d: %v", len(lines), len(wantTypes), lines)
	}
	for i, want := range wantTypes {
		if lines[i]["type"] != want {
			t.Errorf("line %d type = %v, want %q", i, lines[i]["type"], want)
		}
		if lines[i]["run"] != "run-1" {
			t.Errorf("line %d run = %v, want run-1", i, lines[i]["run"])
		}
		if seq, ok := lines[i]["seq"].(float64); !ok || int(seq) != i+1 {
			t.Errorf("line %d seq = %v, want %d", i, lines[i]["seq"], i+1)
		}
		if _, ok := lines[i]["ts"].(string); !ok {
			t.Errorf("line %d lacks ts", i)
		}
	}
	tool := lines[3]
	if tool["call_id"] != "c1" || tool["status"] != "finish" {
		t.Errorf("tool_end = %v", tool)
	}
}

func TestSecretsNeverPersist(t *testing.T) {
	dir := t.TempDir()
	tr := New(true, dir)
	r := tr.StartRun("run-s", RunMeta{})
	const secret = "sk-12345678901234567890abcdef"
	r.ToolCall("c1", "shell", map[string]any{"command": "curl -H 'X: " + secret + "'"})
	r.ToolResult("c1", "shell", "failed", false, 0, 1, nil, "leaked "+secret, "wrap: "+secret)
	r.TurnError(1, errors.New("boom "+secret), 0)
	r.RunError(errors.New("fatal " + secret))
	r.Close()

	raw, err := os.ReadFile(filepath.Join(dir, "run-s.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("trace file leaked secret:\n%s", raw)
	}
	if !strings.Contains(string(raw), "[redacted]") {
		t.Error("no redaction marker present")
	}
}

func TestSnippetCap(t *testing.T) {
	dir := t.TempDir()
	tr := New(true, dir)
	r := tr.StartRun("run-c", RunMeta{})
	big := strings.Repeat(" datum", 10000)
	r.ToolResult("c1", "shell", "finish", true, 0, 1, nil, big, "")
	r.Close()

	lines := readLines(t, filepath.Join(dir, "run-c.jsonl"))
	var tool map[string]any
	for _, l := range lines {
		if l["type"] == "tool_end" {
			tool = l
		}
	}
	if tool == nil {
		t.Fatal("no tool_end line")
	}
	detail, _ := tool["detail"].(string)
	if len(detail) > snippetCap+64 {
		t.Errorf("detail %d chars exceeds cap %d", len(detail), snippetCap)
	}
	if !strings.Contains(detail, "truncated") {
		t.Error("capped detail lacks truncation marker")
	}
}

func TestFileCapTruncatesRun(t *testing.T) {
	dir := t.TempDir()
	tr := New(true, dir)
	r := tr.StartRun("run-big", RunMeta{})
	for i := 0; i < 60000; i++ {
		r.TurnStart(int64(i), 10, 100)
	}
	r.Close()

	lines := readLines(t, filepath.Join(dir, "run-big.jsonl"))
	last := lines[len(lines)-1]
	if last["type"] != "trace_truncated" {
		t.Errorf("last line = %v, want trace_truncated", last["type"])
	}
	if len(lines) >= 60001 {
		t.Errorf("lines = %d, want the run capped", len(lines))
	}
}

func TestScrubArgsTruncationIsRuneSafe(t *testing.T) {
	// CJK text where a byte cut at snippetCap would land mid-rune: the
	// old s[:snippetCap] split multi-byte sequences into invalid UTF-8.
	cjk := strings.Repeat("日本語テスト漢字", 200) // 8 runes each, 1600 runes
	got := scrubArgs(map[string]any{"command": cjk, "path": "src/日本語"})
	if !strings.Contains(got, "truncated") {
		t.Fatalf("oversized CJK args lack truncation marker: %.60q…", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("scrubArgs produced invalid UTF-8 (split multi-byte sequence)")
	}
	if n := len([]rune(got)); n > snippetCap+len("…[truncated]") {
		t.Errorf("truncated args = %d runes, want at most %d", n, snippetCap+len("…[truncated]"))
	}
	// End-to-end through ToolCall: the JSONL line itself must stay valid.
	dir := t.TempDir()
	tr := New(true, dir)
	r := tr.StartRun("run-cjk", RunMeta{})
	r.ToolCall("c1", "shell", map[string]any{"command": cjk})
	r.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "run-cjk.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(raw) {
		t.Errorf("trace file holds invalid UTF-8 after CJK truncation")
	}
	lines := readLines(t, filepath.Join(dir, "run-cjk.jsonl"))
	var tool map[string]any
	for _, l := range lines {
		if l["type"] == "tool_start" {
			tool = l
		}
	}
	if tool == nil {
		t.Fatal("no tool_start line")
	}
	detail, _ := tool["detail"].(string)
	if !strings.Contains(detail, "truncated") || !utf8.ValidString(detail) {
		t.Errorf("tool_start detail not rune-safe: valid=%v marker=%v",
			utf8.ValidString(detail), strings.Contains(detail, "truncated"))
	}
}

func TestConcurrentEmitsStayConsistent(t *testing.T) {
	dir := t.TempDir()
	tr := New(true, dir)
	r := tr.StartRun("run-c", RunMeta{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				r.ToolCall("c", "shell", map[string]any{"i": i})
			}
		}(i)
	}
	wg.Wait()
	r.Close()

	lines := readLines(t, filepath.Join(dir, "run-c.jsonl"))
	// 1 run_start + 400 tool_start lines, seq strictly increasing.
	if len(lines) != 401 {
		t.Fatalf("lines = %d, want 401", len(lines))
	}
	for i := 1; i < len(lines); i++ {
		a := lines[i-1]["seq"].(float64)
		b := lines[i]["seq"].(float64)
		if b != a+1 {
			t.Fatalf("seq not monotonic at line %d", i)
		}
	}
}

func TestUnwritableDirDisablesRun(t *testing.T) {
	dir := t.TempDir()
	// A file where the directory should be: MkdirAll fails.
	blocker := filepath.Join(dir, "block")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := New(true, filepath.Join(blocker, "traces"))
	if r := tr.StartRun("x", RunMeta{}); r != nil {
		t.Error("unwritable dir should yield a nil run, not fail the caller")
	}
}

func TestNilTracer(t *testing.T) {
	var tr *Tracer
	if tr.Enabled() {
		t.Error("nil tracer reports enabled")
	}
	if r := tr.StartRun("x", RunMeta{}); r != nil {
		t.Error("nil tracer StartRun returned a run")
	}
}

// seedTraceFile writes one aged trace file directly, bypassing the
// tracer, so retention tests control exact modtimes.
func seedTraceFile(t *testing.T, dir, name string, age time.Duration) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("{\"type\":\"run_start\",\"run\":"+"\""+name+"\"}\n"), 0o600); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	stale := time.Now().Add(-age)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
}

func countTraceFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestRetentionPrunesOldestBeyondCap(t *testing.T) {
	dir := t.TempDir()
	// Fill past the cap with old files: run-00 oldest, run-69 newest.
	for i := 0; i < maxTraceFiles+6; i++ {
		seedTraceFile(t, dir, fmt.Sprintf("%02d", i)+".jsonl", 2*time.Hour)
	}
	tr := New(true, dir)
	r := tr.StartRun("fresh", RunMeta{})
	if r == nil {
		t.Fatal("StartRun returned nil")
	}
	defer r.Close()

	// Newest 63 old files plus the fresh one: total exactly the cap,
	// oldest 7 evicted.
	names := countTraceFiles(t, dir)
	if len(names) != maxTraceFiles {
		t.Fatalf("files = %d, want exactly the %d-file cap", len(names), maxTraceFiles)
	}
	present := make(map[string]bool, len(names))
	for _, n := range names {
		present[n] = true
	}
	for i := 0; i < 7; i++ {
		if present[fmt.Sprintf("%02d", i)+".jsonl"] {
			t.Errorf("%d.jsonl should have been pruned as oldest", i)
		}
	}
	if !present["fresh.jsonl"] {
		t.Error("the just-created run must never be pruned")
	}
	// Survivors stay complete and parseable.
	for _, n := range names {
		readLines(t, filepath.Join(dir, n))
	}
}

func TestRetentionKeepsFreshFiles(t *testing.T) {
	dir := t.TempDir()
	// Over the cap, but all recently written: a sibling run may still be
	// appending, so nothing may go even though the count exceeds the cap.
	for i := 0; i < maxTraceFiles+4; i++ {
		seedTraceFile(t, dir, fmt.Sprintf("%02d", i)+".jsonl", time.Minute)
	}
	tr := New(true, dir)
	r := tr.StartRun("fresh", RunMeta{})
	if r == nil {
		t.Fatal("StartRun returned nil")
	}
	defer r.Close()

	names := countTraceFiles(t, dir)
	if len(names) != maxTraceFiles+5 {
		t.Errorf("files = %d, want all %d fresh files kept", len(names), maxTraceFiles+5)
	}
}

func TestRetentionNeverDeletesOpenRun(t *testing.T) {
	dir := t.TempDir()
	tr := New(true, dir)
	// An open run whose file LOOKS ancient (clock skew, restored backup)
	// must still survive its own tracer's retention.
	r := tr.StartRun("ancient", RunMeta{})
	if r == nil {
		t.Fatal("StartRun returned nil")
	}
	path := filepath.Join(dir, "ancient.jsonl")
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, stale, stale); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxTraceFiles+3; i++ {
		seedTraceFile(t, dir, fmt.Sprintf("%02d", i)+".jsonl", 2*time.Hour)
	}
	second := tr.StartRun("second", RunMeta{})
	if second == nil {
		t.Fatal("second StartRun returned nil")
	}
	defer r.Close()
	defer second.Close()

	if _, err := os.Stat(path); err != nil {
		t.Errorf("open run file was pruned: %v", err)
	}
	// The open run stays writable after retention ran around it.
	r.TurnStart(1, 2, 3)
	lines := readLines(t, path)
	if len(lines) != 2 || lines[1]["type"] != "turn_start" {
		t.Errorf("open run not appendable after prune: %+v", lines)
	}
}

func TestRetentionIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	// Only .jsonl files are retention candidates; everything else —
	// notes, lockfiles, subdirectories — is left alone.
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxTraceFiles+2; i++ {
		seedTraceFile(t, dir, fmt.Sprintf("%02d", i)+".jsonl", 2*time.Hour)
	}
	tr := New(true, dir)
	r := tr.StartRun("fresh", RunMeta{})
	if r == nil {
		t.Fatal("StartRun returned nil")
	}
	defer r.Close()

	if _, err := os.Stat(filepath.Join(dir, "notes.md")); err != nil {
		t.Errorf("non-trace file removed: %v", err)
	}
	if info, err := os.Stat(filepath.Join(dir, "sub.jsonl")); err != nil || !info.IsDir() {
		t.Errorf("directory removed or mangled: %v", err)
	}
	if len(countTraceFiles(t, dir)) != maxTraceFiles {
		t.Errorf("trace files not pruned to the cap")
	}
}

func TestRetentionRepeatedStartsStayBounded(t *testing.T) {
	dir := t.TempDir()
	tr := New(true, dir)
	// Long-running operation: many sequential runs, each closed.
	for i := 0; i < maxTraceFiles+20; i++ {
		r := tr.StartRun(fmt.Sprintf("%02d", i), RunMeta{})
		if r == nil {
			t.Fatalf("StartRun %d returned nil", i)
		}
		r.TurnStart(1, 1, 1)
		r.Close()
		// Age files out of the freshness window as the run goes so the
		// steady state (not just fresh-file overshoot) is exercised.
		stale := time.Now().Add(-2 * time.Hour)
		_ = os.Chtimes(filepath.Join(dir, fmt.Sprintf("%02d", i)+".jsonl"), stale, stale)
	}
	if names := countTraceFiles(t, dir); len(names) != maxTraceFiles {
		t.Errorf("files = %d, want steady state at the %d-file cap", len(names), maxTraceFiles)
	}
}

func TestRetentionConcurrentStarts(t *testing.T) {
	dir := t.TempDir()
	tr := New(true, dir)
	// Concurrent runs from scheduler-adjacent goroutines: every StartRun
	// must succeed and no file may be half-pruned (prune only unlinks).
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := tr.StartRun("c"+fmt.Sprintf("%02d", i), RunMeta{})
			if r == nil {
				t.Errorf("concurrent StartRun %d returned nil", i)
				return
			}
			r.TurnStart(1, 1, 1)
			r.Close()
		}(i)
	}
	wg.Wait()
	for _, n := range countTraceFiles(t, dir) {
		readLines(t, filepath.Join(dir, n)) // every survivor parses
	}
}

func TestRetentionCrashAbandonedFileSurvives(t *testing.T) {
	dir := t.TempDir()
	tr := New(true, dir)
	// Simulate a killed process: run abandoned without Close.
	abandoned := tr.StartRun("crashed", RunMeta{})
	if abandoned == nil {
		t.Fatal("StartRun returned nil")
	}
	abandoned.TurnStart(7, 3, 100)
	// No Close during the test: a fresh tracer (new process) must still
	// read it, and its own retention must not reap another process's
	// recent file. (Closed in cleanup only so TempDir removal succeeds
	// on Windows, where open files cannot be deleted.)
	t.Cleanup(abandoned.Close)
	tr2 := New(true, dir)
	next := tr2.StartRun("next", RunMeta{})
	if next == nil {
		t.Fatal("second StartRun returned nil")
	}
	defer next.Close()

	lines := readLines(t, filepath.Join(dir, "crashed.jsonl"))
	if len(lines) != 2 || lines[1]["type"] != "turn_start" {
		t.Errorf("abandoned run unreadable: %+v", lines)
	}
}
