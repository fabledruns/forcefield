package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"
)

// AgentRowState is the lifecycle state of one delegate child as observed
// by the agents panel. It derives only from runtime events: every value
// below describes something the panel actually saw, never an assumption
// about the child's work.
type AgentRowState string

const (
	AgentQueued    AgentRowState = "queued"
	AgentRunning   AgentRowState = "running"
	AgentCompleted AgentRowState = "completed"
	AgentFailed    AgentRowState = "failed"
	AgentCancelled AgentRowState = "cancelled"
	AgentDenied    AgentRowState = "denied"
	AgentRejected  AgentRowState = "rejected"
)

// childRow is one delegate child. Rows are created by the panel when a
// delegate tool call starts and are never removed, so subagent-N labels
// stay stable for the lifetime of the session.
type childRow struct {
	Label      string
	Agent      string
	Task       string
	CallID     string
	Turns      int
	TurnsExact bool
	// FinalTurns is the child's own final turn count from the delegate
	// result header, authoritative once the child reports. The panel
	// renders Turns (the observed floor, "N+"); the transcript summary
	// cites FinalTurns when the progress budget hid turns.
	FinalTurns int
	State      AgentRowState
	Status     string
	Detail     string
	StartedAt  time.Time
	EndedAt    time.Time
}

// agentsState is the agents panel's state model. It is pure presentation
// state: apply() only reorganizes observed events, and nothing here talks
// to the runtime, the session, or the model.
type agentsState struct {
	rows      []*childRow
	byCall    map[string]*childRow
	nextLabel int
	// collapsed holds call IDs whose terminal row was already reported
	// to the transcript (the panel itself stops rendering terminal
	// rows). drainCollapsed hands those summaries out exactly once.
	collapsed map[string]bool
}

// apply folds one runtime event into the state. It is tolerant of
// duplicate, late and out-of-order events: the first delegate start for a
// call ID wins, and the first terminal event for a call ID wins. Events
// the panel cannot attribute to a delegate call are ignored. The zero
// value of agentsState is ready to use.
func (s *agentsState) apply(e runtime.Event) {
	if s == nil {
		return
	}
	if s.rows == nil {
		s.rows = make([]*childRow, 0)
	}
	if s.byCall == nil {
		s.byCall = make(map[string]*childRow)
	}
	if s.collapsed == nil {
		s.collapsed = make(map[string]bool)
	}

	switch {
	case e.Type == runtime.EventToolStart && e.ToolCall != nil && e.ToolCall.Name == "delegate":
		s.startDelegate(e.ToolCall)
	case e.Type == runtime.EventToolProgress && e.ToolProgress != nil:
		s.applyProgress(e.ToolProgress)
	case e.ToolResult != nil && e.ToolResult.Name == "delegate":
		s.finishDelegate(e.Type, e.ToolResult)
	}
}

// startDelegate creates the row for a delegate call. A repeated start for
// the same call ID is ignored so labels and start times never move.
func (s *agentsState) startDelegate(call *providers.ToolCall) {
	if s.byCall[call.ID] != nil {
		return
	}
	s.nextLabel++
	row := &childRow{
		Label:      "subagent-" + strconv.Itoa(s.nextLabel),
		Agent:      childAgentName(call.Arguments),
		Task:       childTaskLine(call.Arguments),
		CallID:     call.ID,
		State:      AgentQueued,
		TurnsExact: true,
		StartedAt:  time.Now(),
	}
	s.rows = append(s.rows, row)
	s.byCall[call.ID] = row
}

// applyProgress counts child turns. The recorder's progress lines carry
// a cumulative turn number, so the biggest number seen so far wins; the
// per-child progress budget can hide turns, and the terminal event
// reconciles that (see finishDelegate).
func (s *agentsState) applyProgress(p *runtime.ToolProgress) {
	if p.Stream != "progress" || !strings.HasPrefix(p.Data, "[delegate:") {
		return // another tool's live output
	}
	row := s.byCall[p.ToolCallID]
	if row == nil || row.State.terminal() {
		return
	}
	if n, ok := progressTurn(p.Data); ok {
		if n > row.Turns {
			row.Turns = n
		}
		row.State = AgentRunning
	}
}

// finishDelegate applies the first terminal event for a delegate call.
// Rejections and before-start cancellations are recognized from the
// delegation tool's fixed result messages, so the panel's failed and
// rejected rows describe what actually happened.
func (s *agentsState) finishDelegate(t runtime.EventType, result *runtime.ToolResult) {
	row := s.byCall[result.ToolCallID]
	if row == nil || row.State.terminal() {
		return
	}
	row.EndedAt = time.Now()
	// Every path below are terminal: queue the transcript summary once.
	s.collapsed[result.ToolCallID] = true

	switch t {
	case runtime.EventToolCancelled:
		row.State = AgentCancelled
		return
	case runtime.EventToolDenied:
		row.State = AgentDenied
		row.Detail = resultDetail(result)
		return
	}

	if t == runtime.EventToolFinish && result.Success && !result.IsError {
		row.State = AgentCompleted
		row.Status = childStatusLine(result.Content)
		reconcileTurns(row, result.Content)
		return
	}
	if state, ok := rejectionState(result.Content); ok {
		row.State = state
	} else {
		row.State = AgentFailed
	}
	row.Detail = failureDetail(t, result)
}

// failureDetail is resultDetail with a terminal-kind fallback so a
// failed row never renders without a reason: a tool that errored with
// no message still gets an honest, event-derived line.
func failureDetail(t runtime.EventType, result *runtime.ToolResult) string {
	if d := resultDetail(result); d != "" {
		return d
	}
	switch t {
	case runtime.EventToolFinish:
		return "delegate reported an error"
	case runtime.EventToolFailed:
		return "delegate failed"
	case runtime.EventToolDenied:
		return "delegation denied"
	case runtime.EventToolCancelled:
		return "delegation cancelled"
	default:
		return "delegate failed"
	}
}

// reconcileTurns compares the child's own final turn count (rendered in
// the delegate result header) with what the progress budget let the
// panel observe. More turns than observed means the count is a floor:
// the row renders "N+ turns" instead of claiming an exact total.
func reconcileTurns(row *childRow, content string) {
	n, ok := headerTurns(content)
	if !ok {
		return
	}
	if n > row.Turns {
		row.TurnsExact = false
	}
	if n > row.FinalTurns {
		row.FinalTurns = n
	}
}

// active reports whether any child is still queued or running, which is
// what tells the footer to render the panel at all.
func (s *agentsState) active() bool {
	if s == nil {
		return false
	}
	for _, row := range s.rows {
		if !row.State.terminal() {
			return true
		}
	}
	return false
}

// cancelOpen transitions any still-open rows to AgentCancelled when the
// owning stream is torn down before their terminal events arrive.
func (s *agentsState) cancelOpen() {
	if s == nil {
		return
	}
	now := time.Now()
	for _, row := range s.rows {
		if !row.State.terminal() {
			row.State = AgentCancelled
			row.Detail = "delegation cancelled"
			row.EndedAt = now
		}
	}
}

// drainCollapsed hands out the bounded transcript summaries for children
// that reached a terminal state since the last drain, clearing them so
// each child is reported exactly once. Summaries are sorted for stable
// transcript order. Callers should append them as system entries.
func (s *agentsState) drainCollapsed() []string {
	if len(s.collapsed) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.collapsed))
	for id := range s.collapsed {
		row := s.byCall[id]
		delete(s.collapsed, id)
		if row == nil {
			continue
		}
		status := row.Status
		if status == "" {
			status = truncateRunes(row.Detail, 64)
		}
		if status == "" {
			status = "none"
		}
		turns := row.Turns
		if row.FinalTurns > row.Turns {
			turns = row.FinalTurns
		}
		out = append(out, fmt.Sprintf("%s %s: status=%s, turns=%d", row.Label, row.State, status, turns))
	}
	sort.Strings(out)
	return out
}

// terminal reports whether the state is an end state.
func (state AgentRowState) terminal() bool {
	switch state {
	case AgentCompleted, AgentFailed, AgentCancelled, AgentDenied, AgentRejected:
		return true
	default:
		return false
	}
}

// childAgentName reads the delegate call's target agent.
func childAgentName(args map[string]any) string {
	agent, _ := args["agent"].(string)
	return strings.TrimSpace(agent)
}

// childTaskLine is the first line of the task argument, cut to 80 runes.
func childTaskLine(args map[string]any) string {
	task, _ := args["task"].(string)
	task = strings.TrimSpace(task)
	if i := strings.IndexByte(task, '\n'); i >= 0 {
		task = strings.TrimSpace(task[:i])
	}
	return truncateRunes(task, 80)
}

// progressTurn extracts the cumulative turn number from a delegate
// progress line ("[delegate: agent \"coding\" turn 3]"). The number
// follows the LAST " turn " so an agent name containing that word
// cannot confuse the parse.
func progressTurn(data string) (int, bool) {
	i := strings.LastIndex(data, " turn ")
	if i < 0 {
		return 0, false
	}
	rest := strings.TrimSuffix(strings.TrimSpace(data[i+len(" turn "):]), "]")
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return n, true
}

// headerTurns reads the child's final turn count from the delegate
// result header ("[delegate] agent=... turns=7 tool_calls=...").
func headerTurns(content string) (int, bool) {
	i := strings.Index(content, " turns=")
	if i < 0 {
		return 0, false
	}
	rest := content[i+len(" turns="):]
	if j := strings.IndexByte(rest, ' '); j >= 0 {
		rest = rest[:j]
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return n, true
}

// childStatusLine returns the remainder of the "[delegate] child
// status=" line of the delegate result, cut to 64 runes. The header
// lines come first in the result, so the scan is cheap.
func childStatusLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "[delegate] child status=") {
			return truncateRunes(strings.TrimPrefix(line, "[delegate] child status="), 64)
		}
	}
	return ""
}

// resultDetail is the bounded error/reason text for a failed row: the
// tool error when present, otherwise the first line of its result,
// cut to 160 runes.
func resultDetail(result *runtime.ToolResult) string {
	if result.Err != nil {
		return truncateRunes(result.Err.Error(), 160)
	}
	detail := result.Content
	if i := strings.IndexByte(detail, '\n'); i >= 0 {
		detail = detail[:i]
	}
	return truncateRunes(detail, 160)
}

// rejectionState recognizes the delegation tool's fixed rejection and
// before-start-cancellation messages, which are the only observable
// difference between a rejected call and a failed one. Unknown failure
// text maps to AgentFailed (the ok=false case above).
func rejectionState(content string) (AgentRowState, bool) {
	switch {
	case strings.HasPrefix(content, "task is "):
		return AgentRejected, true // task_chars budget
	case strings.HasPrefix(content, "unknown agent "):
		return AgentRejected, true
	case strings.HasPrefix(content, "delegation depth limit reached"):
		return AgentRejected, true // depth budget
	case strings.HasPrefix(content, "delegation limit reached ("):
		return AgentRejected, true // spawn budget
	case strings.HasPrefix(content, "delegation cancelled before it started"):
		return AgentCancelled, true
	default:
		return AgentFailed, false
	}
}

// truncateRunes cuts s to at most n runes, multi-byte safe.
func truncateRunes(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
