package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"forcefield/internal/orchevents"
	"forcefield/internal/providers"
	"forcefield/internal/redact"
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
	// Output is a bounded buffer of the child's actual streaming text.
	// Mutex-protected: concurrent text emission must not interleave or corrupt.
	mu       sync.Mutex
	output   strings.Builder // live child response text
	maxOutputLines int
}
	// Output is a bounded buffer of the child's actual streaming text.
	// Mutex-protected: concurrent text emission must not interleave or corrupt.
	mu       sync.Mutex
	output   strings.Builder // live child response text
	// MaxOutputLines bounds the row's visible scrollback so a long run
	// cannot flood the quadrant. Transient only: completed rows retain it.
	maxOutputLines int
}
	State      AgentRowState
	Status     string
	Detail     string
	StartedAt  time.Time
	EndedAt    time.Time

	// Delegation telemetry, enriched by applyOrchEvent from the
	// structured orchevents stream. Every field below is only filled
	// from something the events actually said, never reconstructed.
	// Depth stays 0 until a structured event anchors this row: 1 for
	// first-level children of the main agent, 2+ for nested runs.
	Depth int
	// Chain is the child's full delegation path as the events carried
	// it ("root > child"), first anchor wins and later spellings never
	// overwrite it.
	Chain string
	// ParentLabel names the parent visually: "main agent" for
	// first-level children, otherwise the parent row's subagent-N
	// label. Empty when the parent row was still unknown.
	ParentLabel string
	// TaskChars is the requested task length carried by delegation
	// events. The structured log deliberately stores a count, never
	// text; task content is never fabricated from it.
	TaskChars int
	// ActiveTool is the tool currently observed running inside the
	// child per structured tool events. The runtime emits no tool
	// events through the orchestrator sink today, so this stays empty
	// unless a future emitter does.
	ActiveTool string
	// ToolCalls/DeniedCount/FailedCount are observed tool counters.
	// A tool event of the child's own run never touches another
	// child's row, and terminal totals adopt by maximum so neither
	// feed can undercount the other.
	ToolCalls   int
	DeniedCount int
	FailedCount int
	// SlotIndex pins the row to quadrant slot 1..3 (0 = no slot, shown
	// in overflow). Only assignQuadrantSlots may set or clear it, and
	// a non-terminal owner never jumps between slots.
	SlotIndex int
	// seq orders rows deterministically for slot tie-breaks; rows are
	// only appended, so creation order is stable.
	seq int
	// terminalAt stamps the first terminal transition for slot
	// retention age (see terminalSlotRetention).
	terminalAt time.Time
}

// agentsState is the agents panel's state model. It is pure presentation
// state: apply() only reorganizes observed events, and nothing here talks
// to the runtime, the session, or the model.
type agentsState struct {
	rows      []*childRow
	byCall    map[string]*childRow
	nextLabel int
	seq       int
	// collapsed holds call IDs whose terminal row was already reported
	// to the transcript (the panel itself stops rendering terminal
	// rows). drainCollapsed hands those summaries out exactly once.
	collapsed map[string]bool

	// --- structured orchestration events (see applyOrchEvent) ---
	// Correlation indices. Chain-bearing keys are lower-cased because
	// spawn events carry registry casing while delegation events
	// lowercase the target; the pair (call id, chain) is the standing
	// fallback because call ids alone restart per scheduler.
	byChildRun  map[string]*childRow // child run UUID -> row
	byOrchCall  map[string]*childRow // callID + orchKeySep + lower(chain) -> row
	byOrchChain map[string]*childRow // lower(full child chain) -> row

	// Quadrant slot bookkeeping. slots[0..2] hold the owners of Q1..Q3
	// (the main agent is Q0 and is drawn by the panel, never tracked
	// here); archivedBySlot[1..3] counts agents archived out of reuse
	// of that slot so a reclaimed quadrant can report its history.
	slots          [3]*childRow
	archivedBySlot [4]int

	// now injects the wall clock so tests can drive retention ages and
	// StartedAt ordering deterministically. nil means time.Now.
	now func() time.Time
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
	s.initFeed()

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
		Label:        "subagent-" + strconv.Itoa(s.nextLabel),
		Agent:        childAgentName(call.Arguments),
		Task:         childTaskLine(call.Arguments),
		CallID:       call.ID,
		State:        AgentQueued,
		TurnsExact:   true,
		StartedAt:    s.clock(),
		seq:          s.nextRow(),
		maxOutputLines: maxChildOutputLines,
	}
	s.rows = append(s.rows, row)
	s.byCall[call.ID] = row
}

// clock is the panel's wall clock, injected (s.now) in tests so slot
// retention and row ordering stay deterministic.
func (s *agentsState) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// nextRow hands out the next stable row ordinal.
func (s *agentsState) nextRow() int {
	s.seq++
	return s.seq
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

// applyChildText appends a live streaming text chunk to this child's
// bounded output buffer. The row is found by call id (mirrored from the
// structured ChildText event). Text from one child never touches another
// child's buffer because each row owns its mutex-protected buffer, and
// each row is only ever written through its own call id.
// The buffer is bounded: once full, the oldest line is dropped to make
// room, so runaway output cannot flood memory or block the runtime.
func (s *agentsState) applyChildText(callID string, text string) {
	if text == "" {
		return
	}
	row := s.byCall[callID]
	if row == nil {
		return
	}
	row.mu.Lock()
	defer row.mu.Unlock()
	if row.output.Len() > 0 {
		row.output.WriteString("\n")
	}
	row.output.WriteString(text)
	// Bounded retention: drop oldest lines past the cap.
	if row.maxOutputLines > 0 {
		lines := strings.Split(row.output.String(), "\n")
		if len(lines) > row.maxOutputLines {
			row.output.Reset()
			for _, l := range lines[len(lines)-row.maxOutputLines:] {
				if row.output.Len() > 0 {
					row.output.WriteString("\n")
				}
				row.output.WriteString(l)
			}
		}
	}
}

// outputText returns the complete buffered text of this child's output,
// or "" when none was observed. Called by the renderer under no lock
// (output is only appended during run, terminal rows are stable).
func (row *childRow) outputText() string {
	row.mu.Lock()
	defer row.mu.Unlock()
	return row.output.String()
}

// outputLines returns the child's buffered output split into lines,
// bounded by keepLast when non-zero. Used by the renderer to draw only
// what fits in the quadrant.
func (row *childRow) outputLines(keepLast int) []string {
	row.mu.Lock()
	defer row.mu.Unlock()
	all := strings.Split(row.output.String(), "\n")
	if keepLast > 0 && len(all) > keepLast {
		all = all[len(all)-keepLast:]
	}
	return all
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
	row.EndedAt = s.clock()
	row.stampTerminal(row.EndedAt)
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
	now := s.clock()
	for _, row := range s.rows {
		if !row.State.terminal() {
			row.State = AgentCancelled
			row.Detail = "delegation cancelled"
			row.EndedAt = now
			row.stampTerminal(now)
			row.ActiveTool = ""
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

// --- structured orchestration events ------------------------------------------------
//
// The runtime's orchestrator mirrors delegation evidence into the
// structured orchevents stream (internal/orchevents, docs/Events.md).
// Through Runtime.SetOrchestrationEventSink it emits exactly:
//
//	DelegationRequested / DelegationAccepted / DelegationRejected
//	AgentSpawned / ChildProgress / ChildCompleted / BudgetExhausted
//
// Delegation* events carry the DELEGATING scope's identity: Depth is
// the child's depth minus one, CallID is the delegate call's id (the
// same id the runtime feed stamped on the EventToolStart that created
// the panel row), ChildRunID is still empty. Child events carry the
// CHILD's own depth and run UUID, and inherit the delegate call id.
// The schema also defines per-tool events (tool_started & co). No
// runtime path emits them through the sink (internal/runtime has zero
// Tool* emissions), but applyOrchEvent keeps defensive handlers so a
// future emitter lands without re-plumbing. Emissions that identify
// prose (Error, Reason, Status) are scrubbed here because the TUI sink
// sits upstream of the event file's own sanitize pass.

// mainAgentLabel is the visual identity of the main-agent quadrant
// (Q0). First-level children show it as their parent lineage.
const mainAgentLabel = "main agent"

// terminalSlotRetention is PLAN §6.1's terminal retention window: slot
// reuse prefers an owner that has been terminal at least this long.
// Because reuse always picks the LONGEST-terminal owner, and any aged
// owner is by construction the oldest, this window classifies the same
// owner the plain oldest rule would pick; it documents intent and lets
// tests assert the boundary.
const terminalSlotRetention = 1500 * time.Millisecond

// orchKeySep joins correlation key parts. Control characters cannot
// appear in agent names or call ids, so the pair stays unambiguous.
const orchKeySep = "\x1f"

// orchChainSep is the chain separator the runtime emits.
const orchChainSep = " > "

// eventChildDepth maps an event's scope depth to the child row's own
// depth: Delegation* carry the delegating scope's depth, so the child
// is one deeper; child events carry the child's own depth directly.
func eventChildDepth(ev orchevents.Event) int {
	switch ev.Type {
	case orchevents.DelegationRequested, orchevents.DelegationAccepted, orchevents.DelegationRejected:
		return ev.Depth + 1
	default:
		return ev.Depth
	}
}

// orchParentChainKey builds the byOrchChain lookup key for the row that
// delegated along this chain: the path without its own last segment,
// lower-cased. A one-segment chain identifies the main agent and has
// no parent row key.
func orchParentChainKey(chain string) string {
	c := strings.ToLower(strings.TrimSpace(chain))
	if i := strings.LastIndex(c, orchChainSep); i > 0 {
		return c[:i]
	}
	return ""
}

// orchCallKey builds the (call id, chain) correlation key used by all
// event types the delegation path emits.
func orchCallKey(callID, chain string) string {
	return callID + orchKeySep + strings.ToLower(chain)
}

// applyOrchEvent folds one structured orchestration event into the
// state. It enriches rows the runtime feed already created and creates
// rows for nested delegations, which the runtime feed can never see.
// It never mutates anything outside this package's own state.
//
// Ordering tolerances: the sink preserves the runtime's emission
// order, but this stream interleaves with the runtime event feed.
// Everything here re-checks terminal state and adopts counters by
// maximum, so neither feed can reverse or undercount the other, and
// duplicate events collapse into the row they anchored.
func (s *agentsState) applyOrchEvent(ev orchevents.Event) {
	if s == nil {
		return
	}
	s.orchInit()
	now := s.clock()
	switch ev.Type {
	case orchevents.DelegationRequested, orchevents.DelegationAccepted:
		s.orchDelegation(ev, false, now)
	case orchevents.DelegationRejected:
		s.orchDelegation(ev, true, now)
	case orchevents.AgentSpawned:
		s.orchAgentSpawned(ev, now)
	case orchevents.ChildProgress:
		s.orchChildActive(ev, now)
	case orchevents.ChildText:
		s.orchChildText(ev, now)
	case orchevents.BudgetExhausted:
		s.orchBudget(ev, now)
	case orchevents.ChildCompleted:
		s.orchChildCompleted(ev, now)
	case orchevents.ToolStarted:
		s.orchTool(ev, true, now)
	case orchevents.ToolCompleted, orchevents.ToolFailed,
		orchevents.ToolDenied, orchevents.ToolCancelled:
		s.orchTool(ev, false, now)
	default:
		// run_* and events_truncated flow only to the headless event
		// writer (cmd/run.go emits them to its own Writer); unknown
		// types are tolerated per the schema's reader contract.
	}
}

// orchInit lazily prepares the maps applyOrchEvent writes to, so the
// zero value keeps working without the runtime feed having run first.
func (s *agentsState) orchInit() {
	s.initFeed()
	if s.byChildRun == nil {
		s.byChildRun = make(map[string]*childRow)
	}
	if s.byOrchCall == nil {
		s.byOrchCall = make(map[string]*childRow)
	}
	if s.byOrchChain == nil {
		s.byOrchChain = make(map[string]*childRow)
	}
}

// initFeed is the lazy map setup shared by the runtime feed (apply)
// and the orch path: both create rows and collapse entries.
func (s *agentsState) initFeed() {
	if s.rows == nil {
		s.rows = make([]*childRow, 0)
	}
	if s.byCall == nil {
		s.byCall = make(map[string]*childRow)
	}
	if s.collapsed == nil {
		s.collapsed = make(map[string]bool)
	}
}

// orchAnchor resolves the row a structured event belongs to, creating
// a nested row when canCreate and the event pins its own parent.
//
// Lookup order (docs/Events.md "Correlation"):
//  1. ChildRunID — the fresh UUID of the child run, unique;
//  2. the (call id, chain) pair — the delegate call that carries the
//     child's identity, unique within a run because every delegation
//     is a distinct tool call;
//  3. for first-level events only, the delegate call id the runtime
//     feed row was created from, when the chain agrees (empty chains
//     too - the row may simply not be anchored yet). Call ids alone
//     are NOT unique across schedulers (provider counters restart
//     per run), so the pair check plus the chain match is what keeps
//     nested schedulers' call ids from being confused with the
//     main stream's.
//
// Nothing anchors without a row to anchor: unanchored first-level
// events are skipped (the runtime feed row always follows), and
// unanchored nested events are skipped until a parent row resolves
// (a later event of the same child anchors then).
func (s *agentsState) orchAnchor(ev orchevents.Event, now time.Time, canCreate bool) *childRow {
	if ev.ChildRunID != "" {
		if row := s.byChildRun[ev.ChildRunID]; row != nil {
			return row
		}
	}
	if ev.CallID != "" && ev.Chain != "" {
		if row := s.byOrchCall[orchCallKey(ev.CallID, ev.Chain)]; row != nil {
			return row
		}
	}
	if ev.CallID != "" && eventChildDepth(ev) <= 1 {
		if row := s.byCall[ev.CallID]; row != nil && rowChainMatches(row, ev.Chain) {
			return row
		}
	}
	if !canCreate || ev.Chain == "" || eventChildDepth(ev) < 2 {
		return nil
	}
	parent := s.byOrchChain[orchParentChainKey(ev.Chain)]
	if parent == nil {
		return nil
	}
	return s.newOrchRow(parent, ev, now)
}

// rowChainMatches reports whether a row's known chain agrees with a
// chain an event carries. An empty row chain matches anything: the row
// simply has not been anchored by a structured event yet, and the
// runtime feed's own dedup already made one call id mean one child.
func rowChainMatches(row *childRow, chain string) bool {
	if row.Chain == "" || chain == "" {
		return true
	}
	return strings.EqualFold(row.Chain, chain)
}

// anchorRow attaches an event's identity to its row. First-seen values
// win so late duplicates and reordered events never rewrite lineage.
// The event also registers its own index entries, which is how later
// events of the same child find the row through any single one of its
// identifiers.
func (s *agentsState) anchorRow(row *childRow, ev orchevents.Event) {
	if d := eventChildDepth(ev); d > 0 && row.Depth == 0 {
		row.Depth = d
	}
	if row.Chain == "" && ev.Chain != "" {
		row.Chain = ev.Chain
	}
	if row.ParentLabel == "" {
		row.ParentLabel = s.parentLabelFor(ev)
	}
	if ev.ChildRunID != "" {
		s.byChildRun[ev.ChildRunID] = row
	}
	if ev.CallID != "" && ev.Chain != "" {
		s.byOrchCall[orchCallKey(ev.CallID, ev.Chain)] = row
	}
	if ev.Chain != "" {
		s.byOrchChain[strings.ToLower(ev.Chain)] = row
	}
	if ev.TaskChars > row.TaskChars {
		row.TaskChars = ev.TaskChars
	}
}

// parentLabelFor derives the visible parent for a row being anchored.
// First-level children hang off the main agent; deeper children's
// parent is the row registered for the chain without the child's own
// segment. Blank when the chain does not identify one.
func (s *agentsState) parentLabelFor(ev orchevents.Event) string {
	d := eventChildDepth(ev)
	if d <= 0 {
		return ""
	}
	if d == 1 {
		return mainAgentLabel
	}
	if ev.Chain == "" {
		return ""
	}
	if parent := s.byOrchChain[orchParentChainKey(ev.Chain)]; parent != nil {
		return parent.Label
	}
	return ""
}

// newOrchRow creates a nested child row. Nested delegations run inside
// a parent's tool execution, so the runtime feed (fed from the
// top-level stream) can never create them: the structured stream is
// their only source. Callers have already resolved the parent.
func (s *agentsState) newOrchRow(parent *childRow, ev orchevents.Event, now time.Time) *childRow {
	s.nextLabel++
	row := &childRow{
		Label:      "subagent-" + strconv.Itoa(s.nextLabel),
		Agent:      strings.TrimSpace(ev.Agent),
		CallID:     ev.CallID,
		State:      AgentQueued,
		TurnsExact: true,
		StartedAt:  now,
		seq:        s.nextRow(),
	}
	s.rows = append(s.rows, row)
	s.anchorRow(row, ev)
	return row
}

// orchDelegation folds DelegationRequested / Accepted / Rejected. The
// accepted/rejected events add nothing but the reject reason after the
// requested one, and runs on the row the delegation anchors.
func (s *agentsState) orchDelegation(ev orchevents.Event, rejected bool, now time.Time) {
	row := s.orchAnchor(ev, now, !rejected)
	if row == nil {
		return
	}
	s.anchorRow(row, ev)
	if rejected {
		// Terminally rejected delegations are exactly what AgentRejected
		// exists for. A rejection never creates a row, never reverses a
		// terminal state, and never re-queues a collapsed summary.
		if !row.State.terminal() {
			row.State = AgentRejected
			row.EndedAt = now
			row.stampTerminal(now)
			row.ActiveTool = ""
			if d := scrubbedDetail(ev.Reason); d != "" {
				row.Detail = d
			}
			s.flushCollapse(row)
		}
		return
	}
	// requested/accepted: the task length is the only new fact; the
	// task text stays whatever richer feeds captured.
	if ev.TaskChars > row.TaskChars {
		row.TaskChars = ev.TaskChars
	}
}

// orchAgentSpawned binds a child run's UUID to its row. It never
// creates rows and never flips lifecycle state: the child has not yet
// proven a turn, so queued→running waits for the first progress event.
func (s *agentsState) orchAgentSpawned(ev orchevents.Event, now time.Time) {
	row := s.orchAnchor(ev, now, false)
	if row == nil {
		return
	}
	s.anchorRow(row, ev)
}

// orchChildActive folds ChildProgress. Turns are a cumulative exact
// count and only ever climb. First-level events whose runtime row has
// not arrived yet are skipped (the row always follows in the source's
// order); nested progress may still create its row once the parent is
// known.
func (s *agentsState) orchChildActive(ev orchevents.Event, now time.Time) {
	row := s.orchAnchor(ev, now, true)
	if row == nil {
		return
	}
	s.anchorRow(row, ev)
	if row.State.terminal() {
		return // late progress must not reverse a terminal state
	}
	if ev.Turns > row.Turns {
		row.Turns = ev.Turns
		row.TurnsExact = true // recorder counts are exact
	}
	if row.State != AgentRunning {
		row.State = AgentRunning
	}
}

// orchBudget folds BudgetExhausted. Exhaustion is never terminal by
// itself: the affected child keeps running until it records an honest
// cancellation, and the budget fields name the aggregate limit that
// fired. The event anchors like progress but never creates a row: a
// budget record only exists for a child whose delegation already
// appeared.
func (s *agentsState) orchBudget(ev orchevents.Event, now time.Time) {
	row := s.orchAnchor(ev, now, false)
	if row == nil {
		return
	}
	s.anchorRow(row, ev)
}

// orchChildCompleted applies the first terminal outcome from the
// structured stream. Rows the runtime feed already terminalized keep
// their lifecycle state (first-terminal-wins across both feeds) but
// still adopt the exact counter snapshot, which can be higher when the
// progress budget hid turns from the live feed.
func (s *agentsState) orchChildCompleted(ev orchevents.Event, now time.Time) {
	row := s.orchAnchor(ev, now, true)
	if row == nil {
		return
	}
	s.anchorRow(row, ev)

	// Counter snapshots are exact sums; adopt by maximum even on rows
	// the terminal race already closed, and never let a later feed
	// undercount the other.
	if ev.Turns > row.Turns {
		row.Turns = ev.Turns
		row.TurnsExact = true
	}
	if ev.ToolCalls > row.ToolCalls {
		row.ToolCalls = ev.ToolCalls
	}
	if ev.Denied > row.DeniedCount {
		row.DeniedCount = ev.Denied
	}
	if ev.Failed > row.FailedCount {
		row.FailedCount = ev.Failed
	}

	if row.State.terminal() {
		return
	}
	row.ActiveTool = ""
	switch ev.Outcome {
	case orchevents.OutcomeDone:
		row.State = AgentCompleted
		if s := scrubbedDetail(ev.Status); s != "" {
			row.Status = s
		}
	case orchevents.OutcomeCancelled:
		row.State = AgentCancelled
		row.Detail = scrubbedDetail(ev.Error)
	case orchevents.OutcomeBlocked:
		row.State = AgentFailed
		if d := scrubbedDetail(ev.Error); d != "" {
			row.Detail = d
		}
	case "", orchevents.OutcomeError:
		row.State = AgentFailed
		if d := scrubbedDetail(ev.Error); d != "" {
			row.Detail = d
		} else {
			row.Detail = "child run failed"
		}
	default:
		// Unknown outcome: stay honest rather than optimistic.
		row.State = AgentFailed
		row.Detail = "child run ended with unknown outcome " + string(ev.Outcome)
	}
	row.EndedAt = now
	row.stampTerminal(now)
	s.flushCollapse(row)
}

// orchChildText routes a ChildText event to the correct child's output
// buffer. The event carries the delegate call ID and chain; we resolve
// the row via the standard correlation (ChildRunID first, then call-id
// + chain pair) and append the text to that child's bounded buffer.
// Text from one child never touches another child's buffer because each
// row owns its mutex-protected buffer and rows are only resolved through
// the child's own identity keys.
func (s *agentsState) orchChildText(ev orchevents.Event, now time.Time) {
	row := s.orchAnchor(ev, now, false)
	if row == nil {
		return
	}
	s.anchorRow(row, ev)
	if row.State.terminal() {
		// Never feed output into a settled row; completed rows keep
		// their final text (the final report body) untouched.
		return
	}
	if ev.Text != "" {
		row.mu.Lock()
		if row.maxOutputLines > 0 {
			// Bounded retention: keep at most maxOutputLines lines.
			current := strings.Split(row.output.String(), "\n")
			kept := make([]string, 0, len(current)+1)
			if len(current) > row.maxOutputLines {
				kept = append(kept, current[len(current)-row.maxOutputLines:]...)
			} else {
				kept = append(kept, current...)
			}
			row.output.Reset()
			for _, l := range kept {
				if row.output.Len() > 0 {
					row.output.WriteString("\n")
				}
				row.output.WriteString(l)
			}
		}
		row.output.WriteString(ev.Text)
		row.mu.Unlock()

		// Mark the row running if it was not yet running.
		if !row.State.terminal() && row.State != AgentRunning {
			row.State = AgentRunning
		}
	}
}

// orchTool folds per-tool events. The runtime emits none through the
// sink today, so this stays deliberately simple: correlate by the
// usual order, count the call exactly once per started operation, and
// clear the active tool again on the first matching terminal event.
// A tool event can never touch another child's row (each event
// carries enough identity to anchor its own), never double-counts a
// tool already marked active, and never revives a terminal row's
// display.
func (s *agentsState) orchTool(ev orchevents.Event, started bool, now time.Time) {
	row := s.orchAnchor(ev, now, false)
	if row == nil {
		return
	}
	s.anchorRow(row, ev)
	if started {
		if row.State.terminal() || (ev.Tool != "" && row.ActiveTool == ev.Tool) {
			return // tool already known active: same operation, no double count
		}
		row.ActiveTool = ev.Tool
		if ev.Tool != "" {
			row.ToolCalls++
		}
		return
	}
	switch ev.Type {
	case orchevents.ToolFailed:
		row.FailedCount++
	case orchevents.ToolDenied:
		row.DeniedCount++
	}
	if row.ActiveTool == "" || ev.Tool == row.ActiveTool || ev.Tool == "" {
		row.ActiveTool = ""
	}
}

// scrubbedDetail cuts prose from a structured event into the row's
// bounded detail text. The TUI's sink sits upstream of the event
// file's own sanitize pass (orchevents.Writer.sanitize), so it scrubs
// defensively as well.
func scrubbedDetail(prose string) string {
	return truncateRunes(redact.Scrub(strings.TrimSpace(prose)), 160)
}

// stampTerminal records the first time the row reached a terminal
// state. Later terminal observations never move it: the slot
// manager's retention age measures the first completion.
func (row *childRow) stampTerminal(now time.Time) {
	if row.terminalAt.IsZero() {
		row.terminalAt = now
	}
}

// flushCollapse queues the row's one-time transcript summary through
// the same drain the runtime feed's finishDelegate uses. Only rows the
// runtime feed can resolve drain (drainCollapsed looks rows up in
// byCall by call id); nested rows are panel-only, so they do not queue
// a summary keyed by a call id no other feed would supply.
func (s *agentsState) flushCollapse(row *childRow) {
	if row.CallID == "" {
		return
	}
	if s.byCall[row.CallID] == row {
		s.collapsed[row.CallID] = true
	}
}

// --- quadrant slot management -------------------------------------------------------

// assignQuadrantSlots pins the rows the quadrant canvas shows in
// Q1..Q3 and reports them in slot order (nil = empty slot, rendered as
// idle standby). It is idempotent: repeated calls reshuffle nothing.
//
// Policy (PLAN §6.1):
//   - non-terminal owners (queued or running) keep their slot; a
//     running agent never jumps and a queued agent never displaces a
//     running one;
//   - vacant slots fill in claim order: running rows first, then
//     queued, oldest StartedAt first;
//   - when no slot is vacant, a claimant reuses the slot of a
//     TERMINAL owner — the one terminal longest, which is exactly the
//     "prefer ≥ terminalSlotRetention" winner whenever an aged owner
//     exists, and the plan's "oldest eligible terminal slot" fallback
//     otherwise;
//   - active rows are never evicted; rows that cannot claim stay in
//     the overflow summary (see overflowRows).
func (s *agentsState) assignQuadrantSlots() [3]*childRow {
	if s == nil {
		return [3]*childRow{}
	}
	now := s.clock()

	var claimants []*childRow
	for _, r := range s.rows {
		if !r.State.terminal() && r.SlotIndex == 0 {
			claimants = append(claimants, r)
		}
	}
	sort.SliceStable(claimants, func(i, j int) bool { return slotClaimOrder(claimants[i], claimants[j]) })

	for _, claim := range claimants {
		slot := -1
		for i, owner := range s.slots {
			if owner == nil {
				slot = i
				break
			}
		}
		if slot < 0 {
			slot = s.reusableSlot(now)
		}
		if slot < 0 {
			continue // stays visible in the overflow summary
		}
		s.claimSlot(slot, claim)
	}
	return s.slots
}

// slotClaimOrder sorts slot claimants: running work first (the slots
// exist to watch live work), then queued; oldest start first, with
// the row ordinal as the deterministic tiebreak.
func slotClaimOrder(a, b *childRow) bool {
	ra, rb := a.State == AgentRunning, b.State == AgentRunning
	if ra != rb {
		return ra
	}
	if !a.StartedAt.Equal(b.StartedAt) {
		return a.StartedAt.Before(b.StartedAt)
	}
	return a.seq < b.seq
}

// reusableSlot returns the slot worth reclaiming, or -1. Only terminal
// owners are candidates — an active owner is never evicted — and the
// one terminal longest wins, which unifies the plan's retention
// preference and its oldest-eligible fallback into a single testable
// rule.
func (s *agentsState) reusableSlot(now time.Time) int {
	best := -1
	for i, owner := range s.slots {
		if owner == nil || !owner.State.terminal() {
			continue
		}
		if best < 0 || owner.terminalAt.Before(s.slots[best].terminalAt) {
			best = i
		}
	}
	return best
}

// claimSlot moves slot ownership. Evicting a terminal owner archives
// it (SlotIndex cleared, history counted for the reused slot).
func (s *agentsState) claimSlot(slot int, claim *childRow) {
	if owner := s.slots[slot]; owner != nil && owner != claim {
		if !owner.State.terminal() {
			// Defensive: only reusableSlot offers owners for eviction,
			// which are terminal by construction. Refusing to boot an
			// active owner keeps an upstream bug from corrupting state.
			return
		}
		owner.SlotIndex = 0
		s.archivedBySlot[slot+1]++
	}
	s.slots[slot] = claim
	claim.SlotIndex = slot + 1
}

// archivedCount reports how many agents have been archived out of a
// reused quadrant slot (1..3), so a reclaimed slot can show its
// history. Slot 0 is the main agent and never contains children.
func (s *agentsState) archivedCount(slot int) int {
	if s == nil || slot < 1 || slot >= len(s.archivedBySlot) {
		return 0
	}
	return s.archivedBySlot[slot]
}

// overflowRows returns the non-terminal rows that hold no quadrant
// slot, in claim order (running before queued, oldest first). After
// assignQuadrantSlots fills the three assignable slots, this list is
// exactly what the canvas summarizes as "+N running".
func (s *agentsState) overflowRows() []*childRow {
	if s == nil {
		return nil
	}
	var out []*childRow
	for _, r := range s.rows {
		if !r.State.terminal() && r.SlotIndex == 0 {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return slotClaimOrder(out[i], out[j]) })
	return out
}

// overflowLine renders the overflow summary: "+R running: <labels>",
// where running rows are named with their live turn counter and queued
// rows with "(queued)". No agent with a slot is named, queued agents
// keep their own state word, and the summary can only grow as long as
// concurrency limits allow.
func (s *agentsState) overflowLine() string {
	rows := s.overflowRows()
	if len(rows) == 0 {
		return ""
	}
	running := 0
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.State == AgentRunning {
			running++
			mark := ""
			if !r.TurnsExact {
				mark = "+"
			}
			parts = append(parts, fmt.Sprintf("%s (turn %d%s)", r.Label, r.Turns, mark))
			continue
		}
		parts = append(parts, r.Label+" (queued)")
	}
	return fmt.Sprintf("+%d running: %s", running, strings.Join(parts, ", "))
}

// lineageLine renders the nested-delegation subtitle for depth-2+
// rows: "↳ via subagent-1 (depth 2)". Depth-1 children hang straight
// off the main agent and show nothing extra.
func (row *childRow) lineageLine() string {
	if row.Depth < 2 || row.ParentLabel == "" || row.ParentLabel == mainAgentLabel {
		return ""
	}
	return fmt.Sprintf("↳ via %s (depth %d)", row.ParentLabel, row.Depth)
}

// nestedActiveChild returns the first still-open child nested under
// parent's delegation subtree, if the event stream identified one, so
// the parent can show a derived wait state. It is strictly derived
// from observed lineage (never stored, never assumed): a "waiting"
// parent must itself be live, and without a genuinely active
// descendant nothing renders. (The runtime joins children before a
// parent's delegate call returns, so a terminal parent with an open
// descendant cannot occur realistically; the guard is for reordered
// delivery.)
func (s *agentsState) nestedActiveChild(parent *childRow) *childRow {
	if s == nil || parent == nil || parent.Depth < 1 || parent.State.terminal() {
		return nil
	}
	var found *childRow
	for _, r := range s.rows {
		if r == parent || r.ParentLabel == "" || r.State.terminal() {
			continue
		}
		if r.Depth > parent.Depth && s.descendsFrom(r, parent) {
			if found == nil || r.seq < found.seq {
				found = r
			}
		}
	}
	return found
}

// descendsFrom walks r's observed parent chain to see whether it
// contains parent. Parents are always created before their children,
// so the label walk is acyclic and terminates.
func (s *agentsState) descendsFrom(r, parent *childRow) bool {
	cur := r
	for cur != nil {
		if cur.ParentLabel == "" {
			return false
		}
		if cur.ParentLabel == parent.Label {
			return true
		}
		cur = s.rowByLabel(cur.ParentLabel)
	}
	return false
}

// rowByLabel finds the row owning a subagent label, or nil ("main
// agent" is a visual pseudonym, not a row, and ends the walk).
func (s *agentsState) rowByLabel(label string) *childRow {
	for _, r := range s.rows {
		if r.Label == label {
			return r
		}
	}
	return nil
}
