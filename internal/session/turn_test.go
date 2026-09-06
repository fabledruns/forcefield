package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"forcefield/internal/providers"
)

func toolCall(id, name string) providers.ToolCall {
	return providers.ToolCall{ID: id, Name: name, Arguments: map[string]any{"x": 1}}
}

// TestTurnLifecycle pins the full envelope: begin, concurrent pending
// calls, per-call terminal states, turn close, and JSON persistence of
// every field.
func TestTurnLifecycle(t *testing.T) {
	s := New()
	id := s.BeginTurn()
	if id == "" {
		t.Fatal("BeginTurn() returned an empty turn ID")
	}
	if s.Turn == nil || s.Turn.Status != TurnInProgress {
		t.Fatalf("Turn = %+v, want in_progress", s.Turn)
	}

	s.AddPendingCall(toolCall("c1", "shell"))
	s.AddPendingCall(toolCall("c2", "read_file"))
	s.AddPendingCall(toolCall("c1", "shell")) // duplicate: first wins
	if len(s.Turn.Pending) != 2 {
		t.Fatalf("pending = %d, want 2 (duplicate ignored)", len(s.Turn.Pending))
	}

	s.ResolvePendingCall("c1", CallDone, "")
	s.ResolvePendingCall("c2", CallFailed, "exit 1")
	s.ResolvePendingCall("c1", CallFailed, "late") // terminal is final
	if s.Turn.Pending[0].Status != CallDone {
		t.Errorf("c1 status = %q, want done (first resolution wins)", s.Turn.Pending[0].Status)
	}
	if s.Turn.Pending[1].Status != CallFailed || s.Turn.Pending[1].Error != "exit 1" {
		t.Errorf("c2 = %+v, want failed with error", s.Turn.Pending[1])
	}
	if !s.Turn.ModelCompleted {
		t.Error("ModelCompleted = false, want true once calls were produced")
	}

	if !s.EndTurn(TurnComplete, true) {
		t.Fatal("EndTurn(complete) reported no change")
	}
	if s.Turn.Status != TurnComplete {
		t.Errorf("turn status = %q, want complete", s.Turn.Status)
	}
	// Terminal turns are immutable: no late overwrite.
	if s.EndTurn(TurnInterrupted, false) {
		t.Error("EndTurn on a complete turn reported a change")
	}
	if s.Turn.Status != TurnComplete {
		t.Errorf("complete turn overwritten to %q", s.Turn.Status)
	}
	if s.CancelTurn() {
		t.Error("CancelTurn on a complete turn reported a change")
	}

	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Session
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Turn == nil || decoded.Turn.ID != id || decoded.Turn.Status != TurnComplete {
		t.Fatalf("round-trip Turn = %+v", decoded.Turn)
	}
	if len(decoded.Turn.Pending) != 2 || decoded.Turn.Pending[1].Error != "exit 1" {
		t.Fatalf("round-trip Pending = %+v", decoded.Turn.Pending)
	}
}

// crashFixture replays the TUI persist order for one batch (assistant
// batch + pending under one Save, then one result + resolve per call)
// and stops after stopAt events to simulate a crash at that exact
// window. It returns the session as the dead process left it in memory;
// callers Save it to mimic what reached disk.
func crashFixture(t *testing.T, stopAt string) *Session {
	t.Helper()
	s := New()
	s.AddMessage("user", "do things")
	s.AddAssistantToolCalls("", []providers.ToolCall{toolCall("c1", "shell"), toolCall("c2", "read_file")})
	s.AddPendingCall(toolCall("c1", "shell"))
	s.AddPendingCall(toolCall("c2", "read_file"))
	if stopAt == "before-execution" {
		return s
	}
	s.ResolvePendingCall("c1", CallDone, "")
	s.AddToolResult("c1", "shell", "done")
	if stopAt == "during-execution" {
		return s
	}
	s.ResolvePendingCall("c2", CallDone, "")
	s.AddToolResult("c2", "read_file", "contents")
	return s
}

func saveAndReload(t *testing.T, s *Session) *Session {
	t.Helper()
	chdirTemp(t)
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return loaded
}

// TestCrashBeforeExecution: batch + pending reached disk, no call ran.
// Recovery must mark both interrupted and pair every call for replay.
func TestCrashBeforeExecution(t *testing.T) {
	loaded := saveAndReload(t, crashFixture(t, "before-execution"))

	if loaded.Turn == nil || loaded.Turn.Status != TurnInProgress {
		t.Fatalf("reloaded Turn = %+v, want in_progress", loaded.Turn)
	}
	if !loaded.RecoverInterruptedTurn() {
		t.Fatal("RecoverInterruptedTurn() = false, want true")
	}
	if loaded.Turn.Status != TurnInterrupted {
		t.Errorf("turn = %q, want interrupted", loaded.Turn.Status)
	}
	for _, p := range loaded.Turn.Pending {
		if p.Status != CallInterrupted {
			t.Errorf("call %s = %q, want interrupted", p.ID, p.Status)
		}
	}
	if n := loaded.RepairInterruptedTurn(); n != 2 {
		t.Errorf("Repair = %d, want 2 synthesized results", n)
	}
	assertReplayPaired(t, loaded)
}

// TestCrashDuringExecution: first call finished and persisted, second
// still running. Recovery must keep the finished result and interrupt
// only the unsettled call.
func TestCrashDuringExecution(t *testing.T) {
	loaded := saveAndReload(t, crashFixture(t, "during-execution"))

	if !loaded.RecoverInterruptedTurn() {
		t.Fatal("RecoverInterruptedTurn() = false, want true")
	}
	got := map[string]CallStatus{}
	for _, p := range loaded.Turn.Pending {
		got[p.ID] = p.Status
	}
	if got["c1"] != CallDone {
		t.Errorf("c1 = %q, want done (finished before the crash)", got["c1"])
	}
	if got["c2"] != CallInterrupted {
		t.Errorf("c2 = %q, want interrupted", got["c2"])
	}
	if n := loaded.RepairInterruptedTurn(); n != 1 {
		t.Errorf("Repair = %d, want 1 (only c2 orphaned)", n)
	}
	assertReplayPaired(t, loaded)
}

// TestCrashAfterCompletionBeforePersistence: both calls finished in
// memory but the process died before their results were written. The
// disk shows only the running batch, so recovery deterministically marks
// the calls interrupted rather than guessing — and crucially never
// re-executes them.
func TestCrashAfterCompletionBeforePersistence(t *testing.T) {
	chdirTemp(t)
	s := crashFixture(t, "before-execution")
	if err := s.Save(); err != nil {
		t.Fatalf("Save batch state: %v", err)
	}
	// In-memory completions that never reach disk.
	s.ResolvePendingCall("c1", CallDone, "")
	s.AddToolResult("c1", "shell", "done")
	s.ResolvePendingCall("c2", CallDone, "")
	s.AddToolResult("c2", "read_file", "contents")

	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The file cannot know the calls finished: safe recovery says
	// interrupted, and no path re-queues them for execution.
	if !loaded.RecoverInterruptedTurn() {
		t.Fatal("RecoverInterruptedTurn() = false, want true")
	}
	for _, p := range loaded.Turn.Pending {
		if p.Status != CallInterrupted {
			t.Errorf("call %s = %q, want interrupted (never re-run)", p.ID, p.Status)
		}
	}
	loaded.RepairInterruptedTurn()
	assertReplayPaired(t, loaded)
}

// TestMultiplePendingCallsRecovery covers a wide concurrent batch with
// mixed terminal states at crash time.
func TestMultiplePendingCallsRecovery(t *testing.T) {
	s := New()
	s.AddMessage("user", "go")
	var calls []providers.ToolCall
	for _, id := range []string{"a", "b", "c", "d"} {
		calls = append(calls, toolCall(id, "shell"))
	}
	s.AddAssistantToolCalls("", calls)
	for _, c := range calls {
		s.AddPendingCall(c)
	}
	s.ResolvePendingCall("a", CallDone, "")
	s.ResolvePendingCall("b", CallDenied, "nope")
	s.AddToolResult("a", "shell", "ok")
	s.AddToolResult("b", "shell", "denied")
	loaded := saveAndReload(t, s)

	if !loaded.RecoverInterruptedTurn() {
		t.Fatal("want recovery")
	}
	want := map[string]CallStatus{"a": CallDone, "b": CallDenied, "c": CallInterrupted, "d": CallInterrupted}
	for _, p := range loaded.Turn.Pending {
		if want[p.ID] != p.Status {
			t.Errorf("call %s = %q, want %q", p.ID, p.Status, want[p.ID])
		}
	}
	// Second recovery is a no-op: statuses are terminal.
	if loaded.RecoverInterruptedTurn() {
		t.Error("second RecoverInterruptedTurn() = true, want false (idempotent)")
	}
	if n := loaded.RepairInterruptedTurn(); n != 2 {
		t.Errorf("Repair = %d, want 2 (c, d)", n)
	}
	assertReplayPaired(t, loaded)
}

// TestOldSessionWithoutTurnField pins backwards compatibility: files
// written before TurnState existed load with a nil Turn, recover as a
// no-op, and replay exactly as before.
func TestOldSessionWithoutTurnField(t *testing.T) {
	dir := chdirTemp(t)
	s := New()
	s.AddMessage("user", "legacy goal")
	s.AddAssistantToolCalls("", []providers.ToolCall{toolCall("c1", "shell")})
	s.AddToolResult("c1", "shell", "legacy output")
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Strip the turn field the way a pre-feature file lacks it.
	path := filepath.Join(".forcefield", "sessions", s.ID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	delete(body, "turn")
	stripped, _ := json.MarshalIndent(body, "", "  ")
	if err := os.WriteFile(path, stripped, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = dir

	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Turn != nil {
		t.Errorf("Turn = %+v, want nil for a pre-feature file", loaded.Turn)
	}
	if loaded.RecoverInterruptedTurn() {
		t.Error("Recover on a turn-less session reported a change")
	}
	if n := loaded.RepairInterruptedTurn(); n != 0 {
		t.Errorf("Repair = %d, want 0 (already paired)", n)
	}
	msgs := loaded.ProviderMessages()
	if len(msgs) != 3 {
		t.Errorf("replay len = %d, want 3 (user, assistant, tool)", len(msgs))
	}
}

// TestSaveFailurePreservesTurnStateAndFile pins Save's failure contract
// for the new envelope, mirroring messages: a failed Save keeps the
// in-memory turn state (it is intended new state, retried on the next
// Save) while the file on disk stays the last good version.
func TestSaveFailurePreservesTurnStateAndFile(t *testing.T) {
	chdirTemp(t)
	s := New()
	s.AddMessage("user", "original")
	if err := s.Save(); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	path := filepath.Join(".forcefield", "sessions", s.ID+".json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}

	s.BeginTurn()
	s.AddPendingCall(toolCall("c9", "shell"))
	if err := s.Save(); err == nil {
		t.Fatal("expected Save to fail when destination is a directory")
	}
	// In-memory new state is preserved for the retry, like messages.
	if s.Turn == nil || len(s.Turn.Pending) != 1 || s.Turn.Pending[0].Status != CallRunning {
		t.Errorf("Turn = %+v, want the running batch preserved in memory", s.Turn)
	}
	// The file still holds the last good version (no turn yet), verified
	// after removing the blocking directory below.
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatalf("save after cleanup: %v", err)
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Turn == nil || loaded.Turn.Status != TurnInProgress || len(loaded.Turn.Pending) != 1 {
		t.Errorf("reloaded Turn = %+v, want the in-progress batch", loaded.Turn)
	}
}

// TestTurnStateBounded pins that repeated turns replace (never append)
// the envelope, so long runs cannot grow the file through TurnState.
func TestTurnStateBounded(t *testing.T) {
	s := New()
	for i := 0; i < 50; i++ {
		s.BeginTurn()
		s.AddPendingCall(toolCall("c", "shell"))
		s.ResolvePendingCall("c", CallDone, "")
		s.EndTurn(TurnComplete, true)
	}
	if len(s.Turn.Pending) != 1 {
		t.Errorf("pending len = %d, want 1 (latest turn only)", len(s.Turn.Pending))
	}
	raw, _ := json.Marshal(s.Turn)
	if len(raw) > 4*1024 {
		t.Errorf("turn envelope = %d bytes, want bounded (~small)", len(raw))
	}
}

func assertReplayPaired(t *testing.T, s *Session) {
	t.Helper()
	pending := map[string]bool{}
	for _, m := range s.ProviderMessages() {
		if m.Role == providers.AssistantRole {
			for _, tc := range m.ToolCalls {
				pending[tc.ID] = true
			}
		}
		if m.Role == providers.ToolRole && m.ToolCallID != "" {
			delete(pending, m.ToolCallID)
		}
	}
	if len(pending) != 0 {
		t.Errorf("dangling calls in replay: %v", pending)
	}
}

func TestEndTurnRejectsUnknownStatus(t *testing.T) {
	s := New()
	s.BeginTurn()
	if s.EndTurn(TurnStatus("bogus"), false) {
		t.Error("EndTurn(bogus) reported a change")
	}
	if s.Turn.Status != TurnInProgress {
		t.Errorf("turn = %q, want still in_progress", s.Turn.Status)
	}
}

func TestCancelTurnMakesPendingTerminal(t *testing.T) {
	s := New()
	s.BeginTurn()
	s.AddPendingCall(toolCall("c1", "shell"))
	s.AddPendingCall(toolCall("c2", "shell"))
	s.ResolvePendingCall("c1", CallDone, "")
	if !s.CancelTurn() {
		t.Fatal("CancelTurn() = false, want true")
	}
	if s.Turn.Status != TurnCancelled {
		t.Errorf("turn = %q, want cancelled", s.Turn.Status)
	}
	want := map[string]CallStatus{"c1": CallDone, "c2": CallCancelled}
	for _, p := range s.Turn.Pending {
		if want[p.ID] != p.Status {
			t.Errorf("call %s = %q, want %q", p.ID, p.Status, want[p.ID])
		}
	}
	if s.CancelTurn() {
		t.Error("second CancelTurn() = true, want false (idempotent)")
	}
}

func TestResolveIgnoresUnknownAndNonTerminal(t *testing.T) {
	s := New()
	s.BeginTurn()
	s.AddPendingCall(toolCall("c1", "shell"))
	s.ResolvePendingCall("nope", CallDone, "")
	s.ResolvePendingCall("c1", CallRunning, "") // non-terminal: ignored
	if s.Turn.Pending[0].Status != CallRunning {
		t.Errorf("status = %q, want still running", s.Turn.Pending[0].Status)
	}
}
