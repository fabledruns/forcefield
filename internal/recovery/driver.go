package recovery

import (
	"context"
	"errors"
	"strings"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// Heal recovers a session that may have been left mid-turn by a crash
// or kill: interrupted turns and calls are marked terminal (never
// re-executed), missing tool results are synthesized, and the session
// is persisted when anything changed. Nil-safe. Call it wherever a
// session is adopted or about to be replayed.
func Heal(sess *session.Session) bool {
	if sess == nil {
		return false
	}
	changed := sess.RecoverInterruptedTurn()
	if n := sess.RepairInterruptedTurn(); n > 0 {
		changed = true
	}
	if changed {
		_ = sess.Save()
	}
	return changed
}

// RecordToolStart persists one decided tool call before it runs: the
// assistant tool_calls batch (intent) and the running pending-call
// record (execution state) commit in the same Save, so a crash from
// here on is recoverable, never ambiguous. content is accompanying
// assistant text for the batch (already trimmed by the caller); empty
// is fine. A nil session is a no-op.
func RecordToolStart(sess *session.Session, call providers.ToolCall, content string) {
	if sess == nil {
		return
	}
	sess.AppendToolCallToLastAssistant(call, content)
	sess.AddPendingCall(call)
	_ = sess.Save()
}

// CallStatusForEvent maps a terminal tool event to the persisted call
// status. Denied is its own state (never conflated with failure or
// cancellation) so recovery and replay describe what actually happened.
func CallStatusForEvent(t runtime.EventType) session.CallStatus {
	switch t {
	case runtime.EventToolFinish:
		return session.CallDone
	case runtime.EventToolDenied:
		return session.CallDenied
	case runtime.EventToolCancelled:
		return session.CallCancelled
	default:
		return session.CallFailed
	}
}

// ToolErrString extracts the Go-level error for the pending-call
// record. Tool-reported failures (IsError with nil Err) record no
// error: they ran fine and said no.
func ToolErrString(result *runtime.ToolResult) string {
	if result == nil || result.Err == nil {
		return ""
	}
	return result.Err.Error()
}

// RecordToolResult persists one tool outcome: the pending call goes
// terminal under the same Save as its result message, so status and
// outcome commit together and replay always pairs calls with results.
// A nil session or nil result is a no-op.
func RecordToolResult(sess *session.Session, eventType runtime.EventType, result *runtime.ToolResult) {
	if sess == nil || result == nil {
		return
	}
	sess.ResolvePendingCall(result.ToolCallID, CallStatusForEvent(eventType), ToolErrString(result))
	sess.AddToolResult(result.ToolCallID, result.Name, result.Content)
	_ = sess.Save()
}

// NoteTerminal records how a turn ended and persists it, so the file
// says what happened even if the process dies before the next save:
//   - Done / Blocked: the tool batch finished cleanly; Blocked means
//     the runtime stopped itself before another model turn.
//   - Cancelled, or an error caused by context cancellation: cancelled.
//   - Any other error (provider failure, timeout, crash-adjacent):
//     interrupted.
//
// A nil session is a no-op.
func NoteTerminal(sess *session.Session, eventType runtime.EventType, err error) {
	if sess == nil {
		return
	}
	switch eventType {
	case runtime.EventDone:
		if sess.EndTurn(session.TurnComplete, true) {
			_ = sess.Save()
		}
	case runtime.EventBlocked:
		if sess.EndTurn(session.TurnComplete, true) {
			_ = sess.Save()
		}
	case runtime.EventCancelled:
		if sess.EndTurn(session.TurnCancelled, false) {
			_ = sess.Save()
		}
	case runtime.EventError:
		if errors.Is(err, context.Canceled) {
			if sess.EndTurn(session.TurnCancelled, false) {
				_ = sess.Save()
			}
		} else {
			if sess.EndTurn(session.TurnInterrupted, false) {
				_ = sess.Save()
			}
		}
	}
}

// PersistAssistantText saves leftover streamed assistant text that was
// never attached to a tool batch (a finished answer, or a fragment cut
// off by an error). Blank text saves nothing. Reports whether it saved.
func PersistAssistantText(sess *session.Session, text string) bool {
	if sess == nil {
		return false
	}
	if strings.TrimSpace(text) == "" {
		return false
	}
	sess.AddMessage("assistant", text)
	_ = sess.Save()
	return true
}

// CancelAndRepair settles teardown: the active turn (if any) is marked
// cancelled — a no-op when it already ended normally — and results are
// synthesized for calls whose terminal events will never arrive, so the
// session stays replayable. Reports whether it persisted anything.
func CancelAndRepair(sess *session.Session) bool {
	if sess == nil {
		return false
	}
	changed := sess.CancelTurn()
	if n := sess.RepairInterruptedTurn(); n > 0 {
		changed = true
	}
	if changed {
		_ = sess.Save()
	}
	return changed
}

// AlignAgent matches the runtime's active agent to the session's
// persisted agent, falling back to general for unknown names and
// persisting whichever agent ends up active. Mirrors the interactive
// startup path so headless and TUI runs replay with identical tools.
func AlignAgent(rt *runtime.Runtime, sess *session.Session) {
	if rt == nil || sess == nil {
		return
	}
	if strings.TrimSpace(sess.Agent) != "" {
		if err := rt.SetAgent(sess.Agent); err != nil {
			// Unknown agent (e.g. removed built-in): fall back to general.
			_ = rt.SetAgent("general")
			sess.Agent = rt.CurrentAgent()
			_ = sess.Save()
		} else {
			// Provider/model hints may have updated the config.
			sess.Agent = rt.CurrentAgent()
			_ = sess.Save()
		}
	} else {
		sess.Agent = rt.CurrentAgent()
		_ = sess.Save()
	}
}

// IsTerminal reports whether an event type ends a run. Exactly one
// terminal event is emitted per StreamChat invocation.
func IsTerminal(t runtime.EventType) bool {
	switch t {
	case runtime.EventDone, runtime.EventCancelled, runtime.EventBlocked, runtime.EventError:
		return true
	default:
		return false
	}
}

// Driver applies a runtime event stream to a session with the same
// ordering the interactive UI uses: text accumulates until a tool batch
// or the end of the stream, intent commits before execution, results
// commit with resolution, and turns close on terminal events. It never
// executes anything itself — the runtime owns execution; the driver
// only records what the event stream reports.
//
// A Driver is not safe for concurrent use; drive it from the single
// goroutine consuming the event channel.
type Driver struct {
	sess     *session.Session
	text     strings.Builder
	stats    Stats
	terminal runtime.EventType
	termErr  error
	hasTerm  bool
	final    *providers.Response
}

// NewDriver returns a Driver recording into sess. A nil session makes
// every record call a no-op; event observation (stats, outcome, exit
// code) still works.
func NewDriver(sess *session.Session) *Driver {
	return &Driver{sess: sess}
}

// HandleEvent folds one runtime event into the session.
func (d *Driver) HandleEvent(e runtime.Event) {
	if d == nil {
		return
	}
	switch e.Type {
	case runtime.EventText:
		if e.Text != "" {
			d.text.WriteString(e.Text)
		}
	case runtime.EventThinking, runtime.EventToolProgress:
		// Reasoning deltas and live progress are display-only: never
		// persisted, matching the interactive path.
	case runtime.EventToolStart:
		if e.ToolCall != nil {
			content := strings.TrimSpace(d.text.String())
			RecordToolStart(d.sess, *e.ToolCall, content)
			// The buffer now belongs to the persisted assistant turn;
			// start fresh for the next model turn's answer.
			if content != "" {
				d.text.Reset()
			}
		}
	case runtime.EventToolFinish, runtime.EventToolFailed, runtime.EventToolCancelled, runtime.EventToolDenied:
		if e.ToolResult != nil {
			RecordToolResult(d.sess, e.Type, e.ToolResult)
			d.stats.Tally(e.Type)
		}
	case runtime.EventDone, runtime.EventCancelled, runtime.EventBlocked, runtime.EventError:
		// Close the turn before teardown so the persisted record is
		// final even if the process dies before the next save, then
		// keep whatever streamed (a half-finished answer is
		// indistinguishable from the model having said nothing until
		// it is saved), then settle teardown exactly like the UI.
		NoteTerminal(d.sess, e.Type, e.Err)
		PersistAssistantText(d.sess, d.text.String())
		d.text.Reset()
		CancelAndRepair(d.sess)
		if !d.hasTerm {
			d.terminal = e.Type
			d.termErr = e.Err
			d.hasTerm = true
		}
		if e.Type == runtime.EventDone && e.Response != nil {
			cp := *e.Response
			d.final = &cp
		}
	}
}

// Stats returns the terminal tool outcomes observed so far.
func (d *Driver) Stats() Stats {
	if d == nil {
		return Stats{}
	}
	return d.stats
}

// Outcome returns the first terminal event observed, if any.
func (d *Driver) Outcome() (runtime.EventType, error, bool) {
	if d == nil || !d.hasTerm {
		return 0, nil, false
	}
	return d.terminal, d.termErr, true
}

// FinalResponse returns the EventDone response, if the run completed.
func (d *Driver) FinalResponse() (providers.Response, bool) {
	if d == nil || d.final == nil {
		return providers.Response{}, false
	}
	return *d.final, true
}

// ExitCode classifies the observed outcome under the Phase 0 contract.
// With no terminal event (a channel that closed early), a cancelled
// context is NeedsHuman; anything else is Terminal — never retryable
// without proof.
func (d *Driver) ExitCode(ctxErr error) int {
	if d == nil {
		return ExitTerminal
	}
	if typ, err, ok := d.Outcome(); ok {
		return Classify(typ, err, d.stats)
	}
	if errors.Is(ctxErr, context.Canceled) || errors.Is(ctxErr, context.DeadlineExceeded) {
		return ExitNeedsHuman
	}
	return ExitTerminal
}
