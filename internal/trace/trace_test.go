package trace

import (
	"bufio"
	"encoding/json"
	"errors"
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
