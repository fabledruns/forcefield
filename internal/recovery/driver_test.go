package recovery

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// enterTempDir runs the test with the working directory in a fresh temp
// dir, since sessions persist under ./.forcefield/sessions.
func enterTempDir(t *testing.T) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restore Chdir: %v", err)
		}
	})
}

func newPersistedSession(t *testing.T) *session.Session {
	t.Helper()
	sess := session.New()
	sess.AddMessage("user", "do the thing")
	if err := sess.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return sess
}

func reloadSession(t *testing.T, id string) *session.Session {
	t.Helper()
	sess, err := session.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return sess
}

func toolStart(id, name string) runtime.Event {
	call := providers.ToolCall{ID: id, Name: name, Arguments: map[string]any{"command": "echo hi"}}
	return runtime.Event{Type: runtime.EventToolStart, ToolCall: &call}
}

func toolTerminal(typ runtime.EventType, id, name, content string, err error) runtime.Event {
	return runtime.Event{Type: typ, ToolResult: &runtime.ToolResult{
		ToolCallID: id, Name: name, Content: content, Success: err == nil, IsError: err != nil, Err: err,
	}}
}

func TestDriverCompletedRunOrdering(t *testing.T) {
	enterTempDir(t)
	sess := newPersistedSession(t)
	driver := NewDriver(sess)

	driver.HandleEvent(runtime.Event{Type: runtime.EventText, Text: "hello "})
	driver.HandleEvent(toolStart("c1", "shell"))
	// Intent committed before the result exists: batch + pending running.
	mid := reloadSession(t, sess.ID)
	if len(mid.Messages) != 2 {
		t.Fatalf("messages after start = %d, want user + assistant batch", len(mid.Messages))
	}
	batch := mid.Messages[1]
	if len(batch.ToolCalls) != 1 || batch.ToolCalls[0].ID != "c1" {
		t.Fatalf("batch tool calls = %+v, want [c1]", batch.ToolCalls)
	}
	if batch.Content != "hello" {
		t.Errorf("batch content = %q, want streamed text attached once", batch.Content)
	}
	if mid.Turn == nil || mid.Turn.Status != session.TurnInProgress {
		t.Fatalf("turn = %+v, want in_progress", mid.Turn)
	}
	if len(mid.Turn.Pending) != 1 || mid.Turn.Pending[0].Status != session.CallRunning {
		t.Fatalf("pending = %+v, want [c1 running]", mid.Turn.Pending)
	}

	driver.HandleEvent(toolTerminal(runtime.EventToolFinish, "c1", "shell", "hi", nil))
	driver.HandleEvent(runtime.Event{Type: runtime.EventText, Text: "all set"})
	driver.HandleEvent(runtime.Event{
		Type:     runtime.EventDone,
		Response: &providers.Response{Content: "all set"},
	})

	done := reloadSession(t, sess.ID)
	if len(done.Messages) != 4 {
		t.Fatalf("messages = %d, want user, batch, tool result, answer", len(done.Messages))
	}
	if res := done.Messages[2]; res.Role != "tool" || res.ToolCallID != "c1" || res.Content != "hi" {
		t.Errorf("result message = %+v, want tool c1 with output", res)
	}
	if ans := done.Messages[3]; ans.Role != "assistant" || ans.Content != "all set" {
		t.Errorf("answer = %+v, want the post-tool text exactly once", ans)
	}
	if done.Turn == nil || done.Turn.Status != session.TurnComplete {
		t.Errorf("turn = %+v, want complete", done.Turn)
	}
	pending := done.Turn.Pending
	if len(pending) != 1 || pending[0].Status != session.CallDone {
		t.Errorf("pending = %+v, want [c1 done]", pending)
	}
	if done.Compacted != 0 {
		t.Errorf("Compacted = %d, want 0", done.Compacted)
	}

	if code := driver.ExitCode(context.Background().Err()); code != ExitOK {
		t.Errorf("ExitCode = %d, want %d", code, ExitOK)
	}
	if response, ok := driver.FinalResponse(); !ok || response.Content != "all set" {
		t.Errorf("FinalResponse = %+v, %v; want the done content", response, ok)
	}
	if typ, _, ok := driver.Outcome(); !ok || typ != runtime.EventDone {
		t.Errorf("Outcome = %v, %v; want EventDone", typ, ok)
	}
}

func TestDriverCrashBeforeResultThenHeal(t *testing.T) {
	enterTempDir(t)
	sess := newPersistedSession(t)
	driver := NewDriver(sess)

	// The run dies after intent committed but before any result exists.
	driver.HandleEvent(runtime.Event{Type: runtime.EventText, Text: "checking"})
	driver.HandleEvent(toolStart("c1", "shell"))

	crashed := reloadSession(t, sess.ID)
	if len(crashed.Messages) != 2 {
		t.Fatalf("messages at crash = %d, want user + batch (intent persisted)", len(crashed.Messages))
	}
	if crashed.Turn == nil || crashed.Turn.Status != session.TurnInProgress {
		t.Fatalf("turn at crash = %+v, want in_progress", crashed.Turn)
	}

	// A new process adopts the session: heal marks the call interrupted
	// and synthesizes a result. It must not invent an outcome and must
	// not touch the compaction counter.
	if !Heal(crashed) {
		t.Fatal("Heal reported no change for a stranded turn")
	}
	healed := reloadSession(t, sess.ID)
	if healed.Turn.Status != session.TurnInterrupted {
		t.Errorf("turn = %q, want interrupted", healed.Turn.Status)
	}
	if healed.Turn.Pending[0].Status != session.CallInterrupted {
		t.Errorf("pending = %+v, want interrupted", healed.Turn.Pending)
	}
	last := healed.Messages[len(healed.Messages)-1]
	if last.Role != "tool" || last.ToolCallID != "c1" ||
		!strings.Contains(last.Content, "cancelled") {
		t.Errorf("synthesized result = %+v, want cancelled pairing for c1", last)
	}
	if healed.Compacted != 0 {
		t.Errorf("Compacted = %d, want unchanged 0", healed.Compacted)
	}

	// Continuing records nothing new for the settled call: the resumed
	// driver observes Done and exits OK with history intact.
	driver2 := NewDriver(healed)
	driver2.HandleEvent(runtime.Event{Type: runtime.EventDone, Response: &providers.Response{}})
	if code := driver2.ExitCode(context.Background().Err()); code != ExitOK {
		t.Errorf("ExitCode after heal+done = %d, want %d", code, ExitOK)
	}
	final := reloadSession(t, sess.ID)
	if len(final.Messages) != len(healed.Messages) {
		t.Errorf("messages grew %d -> %d on a result-less done",
			len(healed.Messages), len(final.Messages))
	}
}

func TestDriverHistoryIsAppendOnly(t *testing.T) {
	enterTempDir(t)
	sess := newPersistedSession(t)
	driver := NewDriver(sess)

	driver.HandleEvent(toolStart("c1", "read_file"))
	driver.HandleEvent(toolTerminal(runtime.EventToolFinish, "c1", "read_file", "original content", nil))
	snapshot := reloadSession(t, sess.ID).Messages

	// A later driver (new turn, new call, even a stray duplicate terminal
	// for the old call) must never rewrite recorded history.
	driver2 := NewDriver(sess)
	driver2.HandleEvent(toolStart("c2", "shell"))
	driver2.HandleEvent(toolTerminal(runtime.EventToolFinish, "c2", "shell", "new output", nil))
	driver2.HandleEvent(toolTerminal(runtime.EventToolFinish, "c1", "read_file", "forged content", nil))

	after := reloadSession(t, sess.ID).Messages
	if len(after) < len(snapshot) {
		t.Fatalf("history shrank %d -> %d", len(snapshot), len(after))
	}
	for i, msg := range snapshot {
		got := after[i]
		if got.Role != msg.Role || got.Content != msg.Content ||
			got.ToolCallID != msg.ToolCallID || got.Name != msg.Name ||
			len(got.ToolCalls) != len(msg.ToolCalls) {
			t.Fatalf("recorded message %d changed:\nwas  %+v\nnow  %+v", i, msg, got)
		}
		for j, tc := range msg.ToolCalls {
			if got.ToolCalls[j].ID != tc.ID || got.ToolCalls[j].Name != tc.Name {
				t.Fatalf("recorded message %d call %d changed:\nwas  %+v\nnow  %+v",
					i, j, tc, got.ToolCalls[j])
			}
		}
	}
}

func TestDriverDeniedOnlyBlockedNeedsHuman(t *testing.T) {
	enterTempDir(t)
	sess := newPersistedSession(t)
	driver := NewDriver(sess)

	deniedErr := errors.New("permission denied")
	driver.HandleEvent(toolStart("d1", "shell"))
	driver.HandleEvent(toolTerminal(runtime.EventToolDenied, "d1", "shell", "permission denied for tool \"shell\"", deniedErr))
	driver.HandleEvent(runtime.Event{Type: runtime.EventBlocked, Err: errors.New("stopped after 5 consecutive tool failures")})

	if stats := driver.Stats(); !stats.DeniedOnly() || stats.Denied != 1 {
		t.Errorf("stats = %+v, want denied-only", stats)
	}
	if code := driver.ExitCode(context.Background().Err()); code != ExitNeedsHuman {
		t.Errorf("ExitCode = %d, want %d (human must approve)", code, ExitNeedsHuman)
	}
}

func TestDriverTerminalMapping(t *testing.T) {
	enterTempDir(t)
	cancelErr := context.Canceled
	cases := []struct {
		name  string
		event runtime.Event
		code  int
		turn  session.TurnStatus
	}{
		{"cancelled", runtime.Event{Type: runtime.EventCancelled}, ExitNeedsHuman, session.TurnCancelled},
		{"error cancelled", runtime.Event{Type: runtime.EventError, Err: cancelErr}, ExitNeedsHuman, session.TurnCancelled},
		{"error interrupted", runtime.Event{Type: runtime.EventError, Err: transientNetError{}}, ExitRetryable, session.TurnInterrupted},
		{"blocked", runtime.Event{Type: runtime.EventBlocked, Err: errors.New("stopped")}, ExitTerminal, session.TurnComplete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := newPersistedSession(t)
			driver := NewDriver(sess)
			// Terminal events follow an in-flight turn in practice: open
			// one first so turn-close has something to settle, exactly
			// as the interactive path does on ToolStart.
			driver.HandleEvent(toolStart("c1", "shell"))
			driver.HandleEvent(tc.event)
			if code := driver.ExitCode(context.Background().Err()); code != tc.code {
				t.Errorf("ExitCode = %d, want %d", code, tc.code)
			}
			reloaded := reloadSession(t, sess.ID)
			if reloaded.Turn == nil || reloaded.Turn.Status != tc.turn {
				t.Errorf("turn = %+v, want %q", reloaded.Turn, tc.turn)
			}
		})
	}
}

func TestDriverBareErrorWithoutTurn(t *testing.T) {
	enterTempDir(t)
	// A provider failure before any tool call leaves no turn envelope —
	// same as the interactive path — but the exit mapping still holds.
	sess := newPersistedSession(t)
	driver := NewDriver(sess)
	driver.HandleEvent(runtime.Event{Type: runtime.EventError, Err: transientNetError{}})
	if code := driver.ExitCode(context.Background().Err()); code != ExitRetryable {
		t.Errorf("ExitCode = %d, want %d", code, ExitRetryable)
	}
	reloaded := reloadSession(t, sess.ID)
	if reloaded.Turn != nil {
		t.Errorf("turn = %+v, want nil (no turn was ever opened)", reloaded.Turn)
	}
}

func TestDriverNoTerminalFallback(t *testing.T) {
	enterTempDir(t)
	sess := newPersistedSession(t)
	driver := NewDriver(sess)
	driver.HandleEvent(runtime.Event{Type: runtime.EventText, Text: "partial"})

	// Text alone is persisted only at teardown; with no terminal event
	// the exit must not claim success or retryability without proof.
	if code := driver.ExitCode(context.Background().Err()); code != ExitTerminal {
		t.Errorf("ExitCode live ctx = %d, want %d", code, ExitTerminal)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if code := driver.ExitCode(cancelled.Err()); code != ExitNeedsHuman {
		t.Errorf("ExitCode cancelled ctx = %d, want %d", code, ExitNeedsHuman)
	}
}

func TestDriverIgnoresDisplayOnlyEvents(t *testing.T) {
	enterTempDir(t)
	sess := newPersistedSession(t)
	before := len(reloadSession(t, sess.ID).Messages)
	driver := NewDriver(sess)

	driver.HandleEvent(runtime.Event{Type: runtime.EventThinking, Thinking: "hmm"})
	driver.HandleEvent(runtime.Event{Type: runtime.EventToolProgress, ToolProgress: &runtime.ToolProgress{Data: "line"}})
	driver.HandleEvent(runtime.Event{Type: runtime.EventText, Text: ""})

	after := reloadSession(t, sess.ID).Messages
	if len(after) != before {
		t.Errorf("messages grew %d -> %d on display-only events", before, len(after))
	}
	if stats := driver.Stats(); stats != (Stats{}) {
		t.Errorf("stats = %+v, want zero", stats)
	}
	if _, _, ok := driver.Outcome(); ok {
		t.Error("Outcome reported without a terminal event")
	}
}

func TestDriverNilSafety(t *testing.T) {
	var driver *Driver
	// Must not panic; observation still reports terminal failure.
	driver.HandleEvent(runtime.Event{Type: runtime.EventText, Text: "x"})
	if stats := driver.Stats(); stats != (Stats{}) {
		t.Errorf("nil stats = %+v, want zero", stats)
	}
	if _, _, ok := driver.Outcome(); ok {
		t.Error("nil driver reported an outcome")
	}
	if _, ok := driver.FinalResponse(); ok {
		t.Error("nil driver reported a response")
	}
	if code := driver.ExitCode(context.Background().Err()); code != ExitTerminal {
		t.Errorf("nil ExitCode = %d, want %d", code, ExitTerminal)
	}

	// Record helpers are nil-session safe.
	RecordToolStart(nil, providers.ToolCall{ID: "c1"}, "text")
	RecordToolResult(nil, runtime.EventToolFinish, &runtime.ToolResult{ToolCallID: "c1"})
	NoteTerminal(nil, runtime.EventDone, nil)
	Heal(nil)
	AlignAgent(nil, nil)
	if PersistAssistantText(nil, "text") {
		t.Error("nil PersistAssistantText reported a save")
	}
	if CancelAndRepair(nil) {
		t.Error("nil CancelAndRepair reported a change")
	}
}

func TestHealRoundTrip(t *testing.T) {
	enterTempDir(t)

	// Clean session: nothing to heal, no save.
	sess := newPersistedSession(t)
	if Heal(sess) {
		t.Error("Heal reported a change for a clean session")
	}

	// Stranded turn: heal settles and persists. Mirror the real record
	// path — batch plus pending, as RecordToolStart writes them — since
	// Repair pairs assistant batches with results.
	sess.BeginTurn()
	sess.AddAssistantToolCalls("", []providers.ToolCall{{ID: "c1", Name: "shell"}})
	sess.AddPendingCall(providers.ToolCall{ID: "c1", Name: "shell"})
	_ = sess.Save()
	if !Heal(sess) {
		t.Fatal("Heal reported no change for a stranded turn")
	}
	reloaded := reloadSession(t, sess.ID)
	if reloaded.Turn.Status != session.TurnInterrupted {
		t.Errorf("turn = %q, want interrupted", reloaded.Turn.Status)
	}
	if len(reloaded.Messages) == 0 || reloaded.Messages[len(reloaded.Messages)-1].ToolCallID != "c1" {
		t.Errorf("last message = %+v, want synthesized result for c1", reloaded.Messages)
	}
}

func TestCallStatusForEvent(t *testing.T) {
	cases := map[runtime.EventType]session.CallStatus{
		runtime.EventToolFinish:    session.CallDone,
		runtime.EventToolDenied:    session.CallDenied,
		runtime.EventToolCancelled: session.CallCancelled,
		runtime.EventToolFailed:    session.CallFailed,
		runtime.EventDone:          session.CallFailed,
	}
	for event, want := range cases {
		if got := CallStatusForEvent(event); got != want {
			t.Errorf("CallStatusForEvent(%v) = %q, want %q", event, got, want)
		}
	}
}

func TestToolErrString(t *testing.T) {
	if got := ToolErrString(nil); got != "" {
		t.Errorf("nil result = %q, want empty", got)
	}
	reported := &runtime.ToolResult{IsError: true, Content: "no such file"}
	if got := ToolErrString(reported); got != "" {
		t.Errorf("tool-reported failure = %q, want empty (it ran and said no)", got)
	}
	infra := &runtime.ToolResult{IsError: true, Err: errors.New("boom")}
	if got := ToolErrString(infra); got != "boom" {
		t.Errorf("infra failure = %q, want the Go error", got)
	}
}

func TestPersistAssistantTextSkipsBlank(t *testing.T) {
	enterTempDir(t)
	sess := newPersistedSession(t)
	before := len(reloadSession(t, sess.ID).Messages)
	for _, blank := range []string{"", "   ", "\n\t "} {
		if PersistAssistantText(sess, blank) {
			t.Errorf("PersistAssistantText(%q) reported a save", blank)
		}
	}
	if after := len(reloadSession(t, sess.ID).Messages); after != before {
		t.Errorf("messages grew %d -> %d on blank text", before, after)
	}
	if !PersistAssistantText(sess, "  real answer  ") {
		t.Error("PersistAssistantText(real answer) reported no save")
	}
	reloaded := reloadSession(t, sess.ID)
	last := reloaded.Messages[len(reloaded.Messages)-1]
	if last.Role != "assistant" || last.Content != "  real answer  " {
		t.Errorf("last = %+v, want the text stored verbatim (scrub/trim is the caller's job)", last)
	}
}

// breakSaves corrupts the session ID so every subsequent Save fails
// validation before touching disk, and returns a repair func. Save
// failures are otherwise hard to inject deterministically across
// platforms (chmod semantics differ); an invalid ID fails identically
// everywhere, exercising the exact same rollback/no-partial-write paths.
func breakSaves(sess *session.Session) (restore func()) {
	goodID := sess.ID
	sess.ID = "bad/id"
	return func() { sess.ID = goodID }
}

func TestDriverFailedBatchSaveLeavesHealableFile(t *testing.T) {
	enterTempDir(t)
	sess := newPersistedSession(t)
	goodID := sess.ID
	driver := NewDriver(sess)

	restore := breakSaves(sess)
	// Intent commits in memory, but the file keeps the last valid state.
	driver.HandleEvent(runtime.Event{Type: runtime.EventText, Text: "working"})
	driver.HandleEvent(toolStart("c1", "shell"))
	if len(sess.Messages) != 2 {
		t.Fatalf("in-memory messages = %d, want user + batch", len(sess.Messages))
	}
	// The on-disk file is untouched: still exactly the baseline, still
	// coherent, still healable (nothing dangling was half-written).
	onDisk := reloadSession(t, goodID)
	if len(onDisk.Messages) != 1 {
		t.Fatalf("on-disk messages = %d, want only the baseline", len(onDisk.Messages))
	}
	if onDisk.Turn != nil {
		t.Errorf("on-disk turn = %+v, want nil (batch never persisted)", onDisk.Turn)
	}
	restore()
	// Recovery converges: the next save persists everything at once.
	driver.HandleEvent(toolTerminal(runtime.EventToolFinish, "c1", "shell", "done", nil))
	converged := reloadSession(t, goodID)
	if len(converged.Messages) != 3 {
		t.Fatalf("converged messages = %d, want user + batch + result", len(converged.Messages))
	}
}

func TestDriverFailedResultSaveConverges(t *testing.T) {
	enterTempDir(t)
	sess := newPersistedSession(t)
	goodID := sess.ID
	driver := NewDriver(sess)

	driver.HandleEvent(toolStart("c1", "shell"))
	restore := breakSaves(sess)
	driver.HandleEvent(toolTerminal(runtime.EventToolFinish, "c1", "shell", "done", nil))
	// In memory moved on; on disk the batch sits without its result —
	// precisely the crash window heal() exists for.
	if len(sess.Messages) != 3 {
		t.Fatalf("in-memory messages = %d, want user + batch + result", len(sess.Messages))
	}
	stale := reloadSession(t, goodID)
	if len(stale.Messages) != 2 {
		t.Fatalf("on-disk messages = %d, want user + batch", len(stale.Messages))
	}
	if stale.Turn == nil || stale.Turn.Status != session.TurnInProgress {
		t.Fatalf("on-disk turn = %+v, want in_progress", stale.Turn)
	}
	// A heal pass over the stale file pairs the orphan instead of
	// replaying a dangling call, proving the failure left healable state.
	if !Heal(stale) {
		t.Fatal("Heal reported no change for an orphaned batch")
	}
	restore()
	driver.HandleEvent(runtime.Event{Type: runtime.EventDone, Response: &providers.Response{}})
	final := reloadSession(t, goodID)
	var results int
	for _, m := range final.Messages {
		if m.Role == "tool" && m.ToolCallID == "c1" {
			results++
			if m.Content != "done" {
				t.Errorf("result content = %q, want the real outcome", m.Content)
			}
		}
	}
	if results != 1 {
		t.Errorf("results for c1 = %d, want exactly one (no duplicate)", results)
	}
}

func TestDriverFailedEndTurnSaveKeepsPriorTurn(t *testing.T) {
	enterTempDir(t)
	sess := newPersistedSession(t)
	goodID := sess.ID
	driver := NewDriver(sess)

	// Complete one turn cleanly first.
	driver.HandleEvent(toolStart("c1", "shell"))
	driver.HandleEvent(toolTerminal(runtime.EventToolFinish, "c1", "shell", "done", nil))
	driver.HandleEvent(runtime.Event{Type: runtime.EventDone, Response: &providers.Response{}})
	if st := reloadSession(t, goodID).Turn; st == nil || st.Status != session.TurnComplete {
		t.Fatalf("setup turn = %+v, want complete", st)
	}

	// The next turn opens in memory but its persistence fails: the file
	// must still describe the previous complete turn, not a half-open
	// new one.
	restore := breakSaves(sess)
	driver.HandleEvent(toolStart("c2", "shell"))
	_ = restore
	kept := reloadSession(t, goodID)
	if kept.Turn == nil || kept.Turn.Status != session.TurnComplete {
		t.Fatalf("on-disk turn = %+v, want the previous complete turn", kept.Turn)
	}
	for _, m := range kept.Messages {
		for _, tc := range m.ToolCalls {
			if tc.ID == "c2" {
				t.Fatalf("unpersisted batch for c2 leaked to disk: %+v", m)
			}
		}
	}
}

func TestDriverMidStreamErrorWithPartialBatch(t *testing.T) {
	enterTempDir(t)
	sess := newPersistedSession(t)
	driver := NewDriver(sess)

	// Model streamed text plus one tool call, then the stream died with
	// a transient failure: the turn must record interrupted (never
	// retried mid-stream), keep what streamed, and exit retryable.
	driver.HandleEvent(runtime.Event{Type: runtime.EventText, Text: "half an answer"})
	driver.HandleEvent(toolStart("c1", "shell"))
	driver.HandleEvent(runtime.Event{Type: runtime.EventError, Err: transientNetError{}})
	if code := driver.ExitCode(context.Background().Err()); code != ExitRetryable {
		t.Errorf("ExitCode = %d, want %d", code, ExitRetryable)
	}
	persisted := reloadSession(t, sess.ID)
	if persisted.Turn == nil || persisted.Turn.Status != session.TurnInterrupted {
		t.Errorf("turn = %+v, want interrupted", persisted.Turn)
	}
	if persisted.Messages[1].Content != "half an answer" {
		t.Errorf("batch content = %q, want streamed text kept", persisted.Messages[1].Content)
	}
	// The terminal path already converged the file (CancelAndRepair
	// runs inside terminal handling, exactly like the UI teardown), so
	// a later heal is a no-op and the orphan is already paired.
	if Heal(persisted) {
		t.Error("Heal reported a change, want converged (already paired)")
	}
	converged := reloadSession(t, sess.ID)
	var paired bool
	for _, m := range converged.Messages {
		if m.Role == "tool" && m.ToolCallID == "c1" {
			paired = true
		}
	}
	if !paired {
		t.Errorf("orphaned c1 has no synthesized result: %+v", converged.Messages)
	}
}
