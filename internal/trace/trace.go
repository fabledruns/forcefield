// Package trace records structured, local-only execution traces for
// diagnosing agent runs. A trace is one JSON object per line
// (append-oriented JSONL) under .forcefield/traces/<runID>.jsonl,
// covering: run start, model turns, tool calls with permission
// outcomes, tool results, and the terminal run event.
//
// Traces are disabled by default and never leave the machine: there is
// no network, no sampling, and no telemetry. Every free-text field is
// capped and passed through the centralized redaction before writing,
// so secrets cannot land in trace files even when they flow through
// tool output or errors. A per-run byte cap bounds disk use; session
// history and crash recovery (internal/session) are untouched.
package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"forcefield/internal/redact"
)

// snippetCap bounds any single free-text field kept in a trace line.
const snippetCap = 1024

// maxFileBytes bounds one run's trace file; past it the tracer writes
// a single truncation line and drops the rest.
const maxFileBytes = 4 << 20

// defaultDir is used when no directory is configured. It is resolved
// against the process working directory at first use, next to sessions.
const defaultDir = ".forcefield/traces"

// Entry is one JSONL line. Only populated fields are serialized.
type Entry struct {
	Seq        int64          `json:"seq"`
	Ts         string         `json:"ts"`
	Run        string         `json:"run"`
	Type       string         `json:"type"`
	Turn       *int64         `json:"turn,omitempty"`
	CallID     string         `json:"call_id,omitempty"`
	Tool       string         `json:"tool,omitempty"`
	Status     string         `json:"status,omitempty"`
	Success    *bool          `json:"success,omitempty"`
	DurationMs int64          `json:"duration_ms,omitempty"`
	Attempt    int            `json:"attempt,omitempty"`
	ExitCode   *int           `json:"exit_code,omitempty"`
	Error      string         `json:"error,omitempty"`
	Detail     string         `json:"detail,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
}

// Tracer owns trace files for a process. It is safe for concurrent use:
// tool events arrive from scheduler worker goroutines.
type Tracer struct {
	mu      sync.Mutex
	enabled bool
	dir     string
	runs    map[string]*Run
}

// New returns a tracer. Empty dir resolves to defaultDir at first use.
func New(enabled bool, dir string) *Tracer {
	return &Tracer{enabled: enabled, dir: dir, runs: make(map[string]*Run)}
}

// Enabled reports whether tracing records anything.
func (t *Tracer) Enabled() bool {
	return t != nil && t.enabled
}

// RunMeta describes a run for its run_start line.
type RunMeta struct {
	Provider     string
	Model        string
	Agent        string
	MessageCount int
}

// StartRun opens the run's file and records run_start. A nil Run (and
// no file) results when tracing is disabled or the file cannot be
// created: tracing must never fail the run it observes.
func (t *Tracer) StartRun(runID string, meta RunMeta) *Run {
	if t == nil || !t.enabled || runID == "" {
		return nil
	}
	dir := t.dir
	if dir == "" {
		dir = defaultDir
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	_ = os.Chmod(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, runID+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	r := &Run{t: t, id: runID, f: f}
	t.mu.Lock()
	t.runs[runID] = r
	t.mu.Unlock()
	r.emit("run_start", 0, "", "", "", nil, 0, 0, nil, "", "", map[string]any{
		"provider": meta.Provider, "model": meta.Model,
		"agent": meta.Agent, "messages": meta.MessageCount,
	})
	return r
}

// Run is one run's append handle. All methods are nil-safe no-ops so
// call sites never branch on tracing.
type Run struct {
	t      *Tracer
	id     string
	mu     sync.Mutex
	f      *os.File
	seq    int64
	bytes  int64
	capped bool
	closed bool
}

// Close releases the run's file handle. Crash-abandoned files simply
// stay on disk: they are the point (post-mortem diagnostics).
func (r *Run) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeLocked()
}

func (r *Run) closeLocked() {
	if r.closed {
		return
	}
	r.closed = true
	if r.f != nil {
		_ = r.f.Close()
	}
	if r.t != nil {
		r.t.mu.Lock()
		delete(r.t.runs, r.id)
		r.t.mu.Unlock()
	}
}

func boolp(b bool) *bool    { return &b }
func intp(i int) *int       { return &i }
func int64p(i int64) *int64 { return &i }
func snippet(s string) string {
	s = redact.Scrub(s)
	runes := []rune(s)
	if len(runes) <= snippetCap {
		return s
	}
	return string(runes[:snippetCap]) + "…[truncated]"
}

func scrubArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	scrubbed := redact.ScrubMap(args)
	raw, err := json.Marshal(scrubbed)
	if err != nil {
		return ""
	}
	// Truncate on rune boundaries like snippet(): the JSON routinely
	// carries CJK/non-ASCII tool arguments, and a byte cut can split a
	// multi-byte sequence into invalid UTF-8. Same cap, counted in runes.
	s := string(raw)
	if runes := []rune(s); len(runes) > snippetCap {
		s = string(runes[:snippetCap]) + "…[truncated]"
	}
	return s
}

func (r *Run) emit(typ string, turn int64, callID, tool, status string, success *bool, durationMs int64, attempt int, exitCode *int, errText, detail string, meta map[string]any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.f == nil {
		return
	}
	if r.capped {
		return
	}
	r.seq++
	e := Entry{
		Seq: r.seq, Ts: time.Now().UTC().Format(time.RFC3339Nano), Run: r.id,
		Type: typ, CallID: callID, Tool: tool, Status: status, Success: success,
		DurationMs: durationMs, Attempt: attempt, ExitCode: exitCode,
		Error: redact.Scrub(errText), Detail: detail, Meta: meta,
	}
	if turn > 0 {
		e.Turn = int64p(turn)
	}
	raw, jerr := json.Marshal(e)
	if jerr != nil {
		return
	}
	raw = append(raw, '\n')
	if _, werr := r.f.Write(raw); werr != nil {
		r.closeLocked()
		return
	}
	r.bytes += int64(len(raw))
	if r.bytes >= maxFileBytes {
		r.capped = true
		end, _ := json.Marshal(Entry{Seq: r.seq + 1, Ts: time.Now().UTC().Format(time.RFC3339Nano), Run: r.id, Type: "trace_truncated", Detail: "run exceeded the trace file cap; further events dropped"})
		end = append(end, '\n')
		_, _ = r.f.Write(end)
	}
}

// TurnStart records a model turn beginning with request metadata.
func (r *Run) TurnStart(turn int64, messageCount int, estTokens int) {
	r.emit("turn_start", turn, "", "", "", nil, 0, 0, nil, "", "", map[string]any{
		"messages": messageCount, "est_tokens": estTokens,
	})
}

// TurnEnd records a completed model turn.
func (r *Run) TurnEnd(turn int64, stopReason string, promptTokens, completionTokens, totalTokens, toolCalls int, duration time.Duration) {
	r.emit("turn_end", turn, "", "", stopReason, nil, duration.Milliseconds(), 0, nil, "", "", map[string]any{
		"prompt_tokens": promptTokens, "completion_tokens": completionTokens,
		"total_tokens": totalTokens, "tool_calls": toolCalls,
	})
}

// TurnError records a failed model turn.
func (r *Run) TurnError(turn int64, err error, duration time.Duration) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	r.emit("turn_error", turn, "", "", "", boolp(false), duration.Milliseconds(), 0, nil, msg, "", nil)
}

// ToolCall records a tool execution start with scrubbed arguments.
func (r *Run) ToolCall(callID, tool string, args map[string]any) {
	r.emit("tool_start", 0, callID, tool, "", nil, 0, 0, nil, "", scrubArgs(args), nil)
}

// ToolResult records a terminal tool outcome. Content is a scrubbed,
// capped snippet — enough to diagnose, never a full dump.
func (r *Run) ToolResult(callID, tool, status string, success bool, duration time.Duration, attempt int, exitCode *int, content, errText string) {
	r.emit("tool_end", 0, callID, tool, status, boolp(success), duration.Milliseconds(), attempt, exitCode, errText, snippet(content), nil)
}

// RunDone records normal completion.
func (r *Run) RunDone(status string, finalLen int) {
	r.emit("run_done", 0, "", "", status, boolp(true), 0, 0, nil, "", "", map[string]any{"final_chars": finalLen})
}

// RunError records a run-ending failure (including cancellation).
func (r *Run) RunError(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	r.emit("run_error", 0, "", "", "", boolp(false), 0, 0, nil, msg, "", nil)
}

// RunBlocked records a runtime-enforced stop.
func (r *Run) RunBlocked(reason error) {
	msg := ""
	if reason != nil {
		msg = reason.Error()
	}
	r.emit("run_blocked", 0, "", "", "", boolp(false), 0, 0, nil, msg, "", nil)
}
