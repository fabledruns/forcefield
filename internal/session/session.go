// Package session manages local conversation history.
package session

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"forcefield/internal/providers"
	"forcefield/internal/redact"

	"github.com/google/uuid"
)

// Message is one session message. ToolCalls, ToolCallID, and Name are
// stored with omitempty so old session files containing only Role/Content/Time
// continue to load. ProviderMessages restores them when present.
type Message struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	Time    time.Time `json:"time"`

	ToolCalls []providers.ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID and Name are used for tool result messages (role=="tool").
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

// Session is a persisted conversation.
type Session struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Agent is the active specialised agent for this session (e.g. "coding").
	// Empty means old sessions that predate agents; callers should treat
	// "" as "general" for backwards compatibility.
	Agent    string    `json:"agent,omitempty"`
	Messages []Message `json:"messages"`
	// Compacted counts messages dropped by size-bound compaction across the
	// session lifetime. It makes the retention policy observable instead of
	// silent: UIs and doctor can report how much history was summarized
	// away, and tests can assert bounded growth without parsing content.
	Compacted int `json:"compacted,omitempty"`
	// Turn is the crash-recovery envelope for the latest tool-calling turn.
	// Nil for old files and tool-less sessions. See docs/Session.md.
	Turn *TurnState `json:"turn,omitempty"`
	// Supervisor tracks supervised-restart lifecycle (see SupervisorState
	// and docs/Recovery.md). Run metadata only; replay ignores it. No
	// locking; concurrent supervisors must not share a session.
	Supervisor *SupervisorState `json:"supervisor,omitempty"`
	// Plan is the persisted /plan and /build state (see plan.go). Nil
	// for old session files and sessions that never ran /plan; replay
	// and the transcript ignore it.
	Plan *PlanState `json:"plan,omitempty"`
	// LastSaveError records the most recent Save failure for
	// observability (in-memory only, never persisted): callers that
	// fire-and-forget saves can still surface repeated failures via
	// doctor/status without log spam on the success path.
	LastSaveError string `json:"-"`
	// LastSaveTime is when the last Save finished (success or
	// failure), in-memory only.
	LastSaveTime time.Time `json:"-"`
}

// SupervisorState is the persisted half of one supervised-restart episode.
// See docs/Session.md and docs/Recovery.md.
type SupervisorState struct {
	Restarts    int   `json:"restarts,omitempty"`
	ExhaustedAt int64 `json:"exhausted_at,omitempty"`
}

// TurnStatus is the lifecycle state of one tool-calling turn.
type TurnStatus string

const (
	// TurnInProgress means tool calls were decided and at least one has
	// not reached a terminal state. A Turn left in this state on disk
	// means Forcefield stopped unexpectedly mid-turn.
	TurnInProgress TurnStatus = "in_progress"
	// TurnComplete means the run finished the turn normally.
	TurnComplete TurnStatus = "complete"
	// TurnInterrupted means the process died mid-turn (crash/kill): the
	// calls were never re-executed and must stay terminal.
	TurnInterrupted TurnStatus = "interrupted"
	// TurnCancelled means the user (or a teardown path) cancelled the
	// turn before it finished.
	TurnCancelled TurnStatus = "cancelled"
)

// CallStatus is the execution state of one tool call within a turn.
// in_progress (pending/running) is the only non-terminal pair; every
// other value is final and never re-queued.
type CallStatus string

const (
	CallPending     CallStatus = "pending"
	CallRunning     CallStatus = "running"
	CallDone        CallStatus = "done"
	CallFailed      CallStatus = "failed"
	CallDenied      CallStatus = "denied"
	CallCancelled   CallStatus = "cancelled"
	CallInterrupted CallStatus = "interrupted"
)

// isTerminalCallStatus reports whether st ends a call's lifecycle.
func isTerminalCallStatus(st CallStatus) bool {
	switch st {
	case CallDone, CallFailed, CallDenied, CallCancelled, CallInterrupted:
		return true
	default:
		return false
	}
}

// PendingCall tracks one tool call's execution status within the active
// turn. ID/Name/Arguments mirror the assistant tool_calls batch so
// recovery can pair intent with outcome without re-running anything.
type PendingCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Status    CallStatus     `json:"status"`
	StartedAt time.Time      `json:"started_at,omitempty"`
	EndedAt   time.Time      `json:"ended_at,omitempty"`
	Attempt   int            `json:"attempt,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// TurnState is the crash-recovery envelope for the latest tool-calling
// turn. It holds at most one batch (the run loop finishes each batch
// before the next model turn), so it never grows with history.
type TurnState struct {
	ID string `json:"id,omitempty"`
	// Status is the turn lifecycle state; in_progress on disk means the
	// process stopped before the turn finished.
	Status TurnStatus `json:"status,omitempty"`
	// ModelCompleted reports whether the model response that decided
	// this turn finished streaming (true once tool calls were produced
	// or the final answer completed). Informational for debugging
	// failed runs; recovery never branches on it.
	ModelCompleted bool          `json:"model_completed,omitempty"`
	StartedAt      time.Time     `json:"started_at,omitempty"`
	EndedAt        time.Time     `json:"ended_at,omitempty"`
	Pending        []PendingCall `json:"pending,omitempty"`
}

// maxSessionMessages bounds how many messages a session file may hold.
// Beyond this, oldest messages (except the initial goal) are dropped so
// the file never grows to 10s of MB over a 5-day run. The provider sliding
// window (100) is smaller; this is the persistence bound.
const maxSessionMessages = 1000

func (s *Session) compactIfNeeded() {
	if len(s.Messages) <= maxSessionMessages {
		return
	}
	// Keep the first message (often the user's goal) plus the most recent
	// maxSessionMessages-2, reserving one slot for an observable compaction
	// marker so dropped history is explicit, not silent. The provider
	// window (100) is smaller; this is the persistence bound.
	keepFirst := 0
	if len(s.Messages) > 0 && s.Messages[0].Role == "user" {
		keepFirst = 1
	}
	keepRecent := maxSessionMessages - keepFirst - 1
	if keepRecent < 0 {
		keepRecent = 0
	}
	recentStart := len(s.Messages) - keepRecent
	if recentStart < keepFirst {
		recentStart = keepFirst
	}
	dropped := recentStart - keepFirst
	newMessages := make([]Message, 0, maxSessionMessages)
	if keepFirst == 1 {
		newMessages = append(newMessages, s.Messages[0])
	}
	marker := Message{
		Role:    "system",
		Content: fmt.Sprintf("[compacted %d older messages to bound session size; use /sessions to review recent history]", dropped),
		Time:    time.Now(),
	}
	newMessages = append(newMessages, marker)
	newMessages = append(newMessages, s.Messages[recentStart:]...)
	s.Messages = newMessages
	s.Compacted += dropped
}

// maxPersistedToolResultBytes bounds one tool-result record in the
// session file. The in-memory provider view has its own smaller cap
// (runtime maxToolResultChars); this is the persistence bound so a
// single long-lived session file cannot reach GiB scale over a 5-day
// run. 48 KiB sits inside the mandated 32-64 KiB window.
const maxPersistedToolResultBytes = 48 * 1024

// cutRuneBoundary returns the largest prefix of s within limit bytes
// that ends on a UTF-8 rune boundary, so truncation never splits a
// multi-byte character into invalid bytes.
func cutRuneBoundary(s string, limit int) int {
	if len(s) <= limit {
		return len(s)
	}
	cut := 0
	for i, r := range s {
		if i+utf8.RuneLen(r) > limit {
			break
		}
		cut = i + utf8.RuneLen(r)
	}
	if cut == 0 {
		cut = 1
	}
	return cut
}

// truncatePersistedToolResult caps content to
// maxPersistedToolResultBytes on a rune boundary, preserving valid
// UTF-8 and leaving an explicit marker with both sizes.
func truncatePersistedToolResult(content string) string {
	if len(content) <= maxPersistedToolResultBytes {
		return content
	}
	cut := cutRuneBoundary(content, maxPersistedToolResultBytes)
	return fmt.Sprintf("%s\n\n[...persisted tool output truncated at %d bytes, %d bytes total. Full output not retained in session file.]",
		content[:cut], cut, len(content))
}

// MaxModelToolResultChars is the model-visible window for one tool
// result: the bound the originating run applies before the result
// enters provider history. The composed model-visible result (head +
// center marker + tail + truncation notice) never exceeds this byte
// budget; content is cut on rune boundaries inside the budget. It
// lives here (not in the runtime) so both the run loop and
// ProviderMessages share one source of truth: the persisted record
// keeps up to 48 KiB for diagnostics, but replay must not expose more
// than the window the model originally saw.
const MaxModelToolResultChars = 6000

// Truncation chrome for the model-visible window. The format strings
// are the byte-identical successors of the old inline Sprintf strings;
// the bound constants let the window arithmetic pin the reservation
// once, without each caller re-measuring chrome.
const (
	// truncationNoticeFmt closes the composed result: it is the
	// byte-identical successor of the old inline truncation string.
	// Its first %d names the budget constant (6000), not a
	// runtime-computed cut.
	truncationNoticeFmt = "[...output truncated at %d bytes, %d bytes total. Re-run with narrower output (e.g. filters, -run, grep) if you need the rest.]"
	// windowMarkerFmt names the bytes dropped between head and tail
	// instead of silently eliding them.
	windowMarkerFmt = "[...center elided: %d bytes...]"
)

// Window-budget constants. The unit is bytes, not characters: digits
// reserve room for the formatted figures so the bounds below are true
// composition upper limits, not estimates.
const (
	// modelToolResultTailBytes is the suffix window the composed result
	// preserves: recent rows and the tool's own truncation note land
	// here, where callers look for freshness.
	modelToolResultTailBytes = 600
	// sizeDigitsReserve is the worst-case decimal digit count for the
	// marker's elided figure and the notice's total-bytes figure
	// (999,999,999 bytes ≈ 1 GiB), so the bounds stay true for any
	// input this program can realistically pass through.
	sizeDigitsReserve = 9
	// maxRowCleanSkim is the farthest the tail window's head skims
	// forward to open on a line boundary instead of mid-row.
	maxRowCleanSkim = 63
	// windowMarkerMax bounds the center marker: its literal frame plus
	// the reserved digits for the elided figure.
	windowMarkerMax = len("[...center elided: ") + sizeDigitsReserve + len(" bytes...]")
	// truncationNoticeMax bounds the closing notice. Literal base 127
	// is the format weight with the first %d resolved to the constant
	// 6000 (4 digits); it was verified against fmt.Sprintf with
	// 999999999 total before pinning, and one sizeDigitsReserve covers
	// the total figure's digits.
	truncationNoticeMax = 127 + sizeDigitsReserve
)

// modelHeadBudget is the head-window reservation: everything the
// composed result may spend besides the head itself. Reservation
// happens before window selection — the tail, the three "\n\n"
// separators, and the bounded chrome are subtracted up front, so head
// selection can never orphan the tail or the chrome.
//
//	MaxModelToolResultChars − modelToolResultTailBytes − 3·len("\n\n")
//	− windowMarkerMax − truncationNoticeMax	= 5220 (pinned).
func modelHeadBudget() int {
	return MaxModelToolResultChars - modelToolResultTailBytes - 3*len("\n\n") - windowMarkerMax - truncationNoticeMax
}

// TruncateModelToolResult caps content to MaxModelToolResultChars while
// preserving valid UTF-8, a fresh-row tail, an explicit marker naming
// the dropped bytes, and the truncation notice. Content within the
// bound is returned unchanged.
//
// Live runs (the runtime's truncateToolResult in
// internal/runtime/runtime_messages.go) and replay (ProviderMessages in
// this package) share this one implementation: live and replay parity,
// so a resumed session never widens the window the model originally
// saw. Ordering asymmetry (pre-existing, documented here): the live
// path truncates raw output before the scrub step, while replay
// truncates the already-scrubbed persisted record — scrub-length
// deltas can therefore make the two paths differ byte for byte. The
// bounds hold on both paths; only the exact bytes at the window edges
// may track the scrubbed lengths.
func TruncateModelToolResult(content string) string {
	if len(content) <= MaxModelToolResultChars {
		return content
	}
	return truncateModelResult(content, MaxModelToolResultChars, modelHeadBudget(), modelToolResultTailBytes)
}

// runeHeadLen decides how much of s may enter a result as head content:
// the cutRuneBoundary length, demoted to an empty head when it would tear
// the first rune. cutRuneBoundary's zero-cut fallback forces a one-byte
// cut even when the headroom is smaller than the first rune (tiny test
// budgets, negative guard headroom); a torn leading byte is never
// allowed into the composed result, so the head empties instead.
// cutRuneBoundary itself stays untouched: its other callers (persisted
// records, argument strings) always pass real limits and keep the
// fallback.
func runeHeadLen(s string, limit int) int {
	if len(s) == 0 {
		return 0
	}
	cut := cutRuneBoundary(s, limit)
	if cut > len(s) || (cut < len(s) && !utf8.RuneStart(s[cut])) {
		return 0
	}
	return cut
}

// truncateModelResult composes head + center marker + tail + notice
// within maxChars. Expects len(content) > maxChars and
// headBudget + tailBytes ≤ maxChars (the TruncateModelToolResult
// constants satisfy both; the guard chain below still holds the bound
// for callers that pass tighter budgets, e.g. tests).
//
// Fit guarantee by reservation: head ≤ headBudget via runeHeadLen (the
// cutRuneBoundary cut, emptied rather than torn when the first rune
// does not fit the headroom), tail ≤ tailBytes (the window is a suffix
// of the input, so its far end is len(content) and a trailing partial
// rune cannot exist by construction), marker ≤ windowMarkerMax, notice
// ≤ truncationNoticeMax, and the three "\n\n" separators are the only
// other spend — so the composed result never exceeds
// MaxModelToolResultChars.
//
// Non-overlap proof: for len(content) > maxChars ≥ headBudget +
// tailBytes, the head window ends at headBudget while the tail window
// starts at len(content) − tailBytes > headBudget, leaving
// len(content) − headBudget − tailBytes bytes always elided between
// them (181+ at the pin) — the windows can never overlap, so head and
// tail never claim the same bytes.
func truncateModelResult(content string, maxChars, headBudget, tailBytes int) string {
	head := content[:runeHeadLen(content, headBudget)]
	// Cut on a rune boundary so multi-byte characters at the edges are
	// never split into invalid UTF-8, which would poison every later
	// provider request that replays this tool result. The tail start
	// snaps forward to a rune start; its far end is the input's end.
	tailStart := len(content) - tailBytes
	if tailStart < 0 {
		tailStart = 0
	}
	for tailStart < len(content) && !utf8.RuneStart(content[tailStart]) {
		tailStart++
	}
	// Skim to a clean row: when a newline arrives within the skim
	// limit, the tail opens on the line after it. The byte after a
	// newline is always a rune start, so no second snap is needed.
	if skim := len(content) - tailStart; skim > 0 {
		limit := maxRowCleanSkim
		if limit > skim {
			limit = skim
		}
		if nl := strings.IndexByte(content[tailStart:tailStart+limit], '\n'); nl >= 0 {
			tailStart += nl + 1
		}
	}

	compose := func(headLen, tailStart int) string {
		tail := content[tailStart:]
		elided := len(content) - headLen - len(tail)
		return content[:headLen] + "\n\n" + fmt.Sprintf(windowMarkerFmt, elided) + "\n\n" + tail + "\n\n" +
			fmt.Sprintf(truncationNoticeFmt, maxChars, len(content))
	}

	out := compose(len(head), tailStart)
	if len(out) <= maxChars {
		return out
	}

	// Guard chain (unreachable with the pinned constants below a 1 GiB
	// input, but not something to trust): shrink the tail first,
	// re-snapping the moved start on its new rune position; the
	// recomputed elided figure follows automatically.
	tailStart += len(out) - maxChars
	if tailStart > len(content) {
		tailStart = len(content)
	}
	for tailStart < len(content) && !utf8.RuneStart(content[tailStart]) {
		tailStart++
	}
	out = compose(len(head), tailStart)
	if len(out) <= maxChars {
		return out
	}
	// Still over: shrink the head next. runeHeadLen keeps the cut
	// rune-aligned within the shrunken head; when the remaining
	// headroom is smaller than the first rune (or empty), the head
	// empties entirely instead of tearing that rune.
	out = compose(runeHeadLen(head, len(head)-(len(out)-maxChars)), tailStart)
	if len(out) <= maxChars {
		return out
	}
	// Absolute stop: a rune-snapped hard clamp of everything above.
	return out[:cutRuneBoundary(out, maxChars)]
}

// maxPersistedArgStringBytes caps any single string value inside
// persisted tool-call arguments. 8 KiB keeps commands, paths, and
// snippets intact while bounding the per-call record alongside the
// 48 KiB tool-result cap.
const maxPersistedArgStringBytes = 8 * 1024

// truncatePersistedArgString caps one argument string on a rune
// boundary with an explicit marker carrying both sizes.
func truncatePersistedArgString(s string) string {
	if len(s) <= maxPersistedArgStringBytes {
		return s
	}
	cut := cutRuneBoundary(s, maxPersistedArgStringBytes)
	return fmt.Sprintf("%s\n[...persisted argument truncated at %d bytes, %d bytes total. Full value not retained in session file.]",
		s[:cut], cut, len(s))
}

// sanitizePersistedArguments returns a copy of args with secrets
// scrubbed and every nested string value bounded. Inputs are never
// mutated. Non-string scalars pass through; unknown container types
// pass through by reference (they cannot be bounded without changing
// their shape).
func sanitizePersistedArguments(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	return capPersistedArgValue(redact.ScrubMap(args)).(map[string]any)
}

// capPersistedArgValue bounds strings recursively, rebuilding every
// map and slice it descends into so the caller's structures are never
// shared with the persisted record.
func capPersistedArgValue(v any) any {
	switch t := v.(type) {
	case string:
		return truncatePersistedArgString(t)
	case map[string]any:
		if t == nil {
			return t
		}
		out := make(map[string]any, len(t))
		for k, v2 := range t {
			out[k] = capPersistedArgValue(v2)
		}
		return out
	case []any:
		if t == nil {
			return t
		}
		out := make([]any, len(t))
		for i, v2 := range t {
			out[i] = capPersistedArgValue(v2)
		}
		return out
	case []string:
		if t == nil {
			return t
		}
		out := make([]string, len(t))
		for i, s := range t {
			out[i] = truncatePersistedArgString(s)
		}
		return out
	case []map[string]any:
		if t == nil {
			return t
		}
		out := make([]map[string]any, len(t))
		for i, m := range t {
			out[i] = capPersistedArgValue(m).(map[string]any)
		}
		return out
	default:
		return v
	}
}

// ProviderMessages converts session history to provider messages.
// Tool results are fenced so the model treats them as data, not
// instructions (prompt-injection mitigation). System compaction markers
// are persistence observability only and are skipped here; the provider
// sliding window already bounds what the model sees.
func (s *Session) ProviderMessages() []providers.Message {
	messages := make([]providers.Message, 0, len(s.Messages))

	for _, msg := range s.Messages {
		if msg.Role == "system" {
			continue
		}
		content := msg.Content
		if msg.Role == string(providers.ToolRole) {
			// Replay the model-visible window, not the full persisted
			// record: the originating run truncated this result to
			// MaxModelToolResultChars before the model ever saw it, and
			// resume must not silently widen that window (up to 48 KiB
			// stays on disk for diagnostics and the transcript).
			content = FenceToolResult(msg.Name, TruncateModelToolResult(content))
		}
		messages = append(messages, providers.Message{
			Role:       providers.Role(msg.Role),
			Content:    content,
			ToolCalls:  msg.ToolCalls,
			ToolCallID: msg.ToolCallID,
			Name:       msg.Name,
		})
	}

	return messages
}

// AddProviderMessage appends a full provider message with fidelity, storing
// tool fields when present. Prefer this for assistant tool_calls and tool
// results; AddMessage remains for simple user/assistant text.
func (s *Session) AddProviderMessage(msg providers.Message) {
	// Scrub secrets before persistence so session files and provider replay
	// never contain raw keys. This is defense-in-depth even though
	// sensitive files now require Ask. Tool-call arguments get the same
	// treatment (copied — the caller's map is never mutated): commands,
	// paths, and file content routinely carry credentials.
	msg.Content = ScrubContent(msg.Content)
	if msg.Role == providers.ToolRole {
		msg.Content = truncatePersistedToolResult(msg.Content)
	}
	if len(msg.ToolCalls) > 0 {
		calls := make([]providers.ToolCall, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			tc.Arguments = sanitizePersistedArguments(tc.Arguments)
			calls[i] = tc
		}
		msg.ToolCalls = calls
	}
	s.Messages = append(s.Messages, Message{
		Role:       string(msg.Role),
		Content:    msg.Content,
		Time:       time.Now(),
		ToolCalls:  msg.ToolCalls,
		ToolCallID: msg.ToolCallID,
		Name:       msg.Name,
	})
	s.UpdatedAt = time.Now()
	s.compactIfNeeded()
}

// hasAssistantToolCall reports whether a tool-call ID already appears
// in any persisted assistant batch.
func (s *Session) hasAssistantToolCall(id string) bool {
	if s == nil || id == "" {
		return false
	}
	for _, m := range s.Messages {
		if m.Role != string(providers.AssistantRole) {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID == id {
				return true
			}
		}
	}
	return false
}

// hasToolResult reports whether a tool result with id is already
// persisted.
func (s *Session) hasToolResult(id string) bool {
	if s == nil || id == "" {
		return false
	}
	for _, m := range s.Messages {
		if m.Role == string(providers.ToolRole) && m.ToolCallID == id {
			return true
		}
	}
	return false
}

// AddAssistantToolCalls appends an assistant message that contains tool calls
// (and optional accompanying text). It is the session-persistence side of the
// in-memory messages = append(assistant, ToolCalls) in runtime.run.
func (s *Session) AddAssistantToolCalls(content string, toolCalls []providers.ToolCall) {
	// Duplicate IDs reuse execution without re-running (see runtime
	// idempotency): persisting a second assistant pair with the same ID
	// would bloat history and risk strict-provider replay rejection, so
	// filter to IDs not yet recorded. The same non-empty ID twice in one
	// batch keeps only its first occurrence. Empty IDs carry no identity
	// and always persist. Normal unique calls are untouched.
	filtered := make([]providers.ToolCall, 0, len(toolCalls))
	seen := make(map[string]struct{}, len(toolCalls))
	for _, tc := range toolCalls {
		if tc.ID != "" {
			if _, ok := seen[tc.ID]; ok {
				continue
			}
			seen[tc.ID] = struct{}{}
			if s.hasAssistantToolCall(tc.ID) {
				continue
			}
		}
		filtered = append(filtered, tc)
	}
	if len(filtered) == 0 && len(toolCalls) > 0 {
		return
	}
	s.AddProviderMessage(providers.Message{
		Role:      providers.AssistantRole,
		Content:   content,
		ToolCalls: filtered,
	})
}

// AddToolResult appends a tool result message (role=="tool") linked to a
// previous assistant tool call via ToolCallID.
func (s *Session) AddToolResult(toolCallID, name, content string) {
	// A repeated ID reuses the recorded result (never re-executed): do
	// not persist a second result pair with the same ID.
	if toolCallID != "" && s.hasToolResult(toolCallID) {
		return
	}
	s.AddProviderMessage(providers.Message{
		Role:       providers.ToolRole,
		ToolCallID: toolCallID,
		Name:       name,
		Content:    content,
	})
}

// RepairInterruptedTurn synthesizes cancelled results for dangling tool calls
// (and drops unpairable ID-less calls) so the session stays resumable.
// Returns the change count; callers Save when positive. See docs/Session.md.
func (s *Session) RepairInterruptedTurn() int {
	if s == nil {
		return 0
	}
	resultIDs := make(map[string]struct{}, len(s.Messages))
	unpairedEmptyResults := 0
	for _, m := range s.Messages {
		if m.Role == string(providers.ToolRole) {
			if m.ToolCallID != "" {
				resultIDs[m.ToolCallID] = struct{}{}
			} else {
				unpairedEmptyResults++
			}
		}
	}
	type orphan struct {
		id   string
		name string
	}
	var orphans []orphan
	droppedEmpty := 0
	for i := range s.Messages {
		m := &s.Messages[i]
		if m.Role != string(providers.AssistantRole) {
			continue
		}
		kept := m.ToolCalls[:0]
		droppedHere := 0
		for _, tc := range m.ToolCalls {
			if tc.ID == "" {
				// Pair positionally with an ID-less result when one
				// exists; otherwise the call is unrecoverable by ID
				// and must not replay as a dangling call.
				if unpairedEmptyResults > 0 {
					unpairedEmptyResults--
					kept = append(kept, tc)
				} else {
					droppedEmpty++
					droppedHere++
				}
				continue
			}
			if _, ok := resultIDs[tc.ID]; ok {
				kept = append(kept, tc)
				continue
			}
			orphans = append(orphans, orphan{id: tc.ID, name: tc.Name})
			resultIDs[tc.ID] = struct{}{} // same ID twice: repair once
			kept = append(kept, tc)
		}
		// Clear the tail so dropped calls do not linger in the backing
		// array beyond the kept prefix.
		for j := len(kept); j < len(m.ToolCalls); j++ {
			m.ToolCalls[j] = providers.ToolCall{}
		}
		m.ToolCalls = kept
		if droppedHere > 0 && len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) == "" {
			m.Content = "[empty tool call dropped: turn interrupted before the result was recorded]"
		}
	}
	for _, o := range orphans {
		s.AddToolResult(o.id, o.name, "tool execution cancelled (turn interrupted before the result was recorded)")
	}
	return len(orphans) + droppedEmpty
}

// BeginTurn starts a new tool-calling turn envelope, replacing any
// finished turn's record. The run loop finishes each tool batch before
// the next model turn, so at most one turn is ever in flight and the
// envelope stays bounded. It returns the new turn ID.
func (s *Session) BeginTurn() string {
	if s == nil {
		return ""
	}
	id := uuid.NewString()
	now := time.Now()
	s.Turn = &TurnState{
		ID:        id,
		Status:    TurnInProgress,
		StartedAt: now,
	}
	s.UpdatedAt = now
	return id
}

// ensureTurn returns the active in-progress turn, starting one when
// there is none or the last one already reached a terminal state.
func (s *Session) ensureTurn() *TurnState {
	if s == nil {
		return nil
	}
	if s.Turn == nil || s.Turn.Status != TurnInProgress {
		s.BeginTurn()
	}
	return s.Turn
}

// AddPendingCall records a decided tool call as running before its
// execution completes. Call it alongside the assistant tool_calls batch
// persist, under the same Save, so intent and status commit atomically.
// A duplicate ID is ignored: the first record wins. String arguments are
// scrubbed (a copied map — the caller's is never mutated) so secrets in
// commands, paths, or content never reach the session file.
func (s *Session) AddPendingCall(call providers.ToolCall) {
	if s == nil || call.ID == "" {
		return
	}
	turn := s.ensureTurn()
	if turn == nil {
		return
	}
	for _, p := range turn.Pending {
		if p.ID == call.ID {
			return
		}
	}
	now := time.Now()
	turn.Pending = append(turn.Pending, PendingCall{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: sanitizePersistedArguments(call.Arguments),
		Status:    CallRunning,
		StartedAt: now,
	})
	turn.ModelCompleted = true // calls exist only after a full response
	s.UpdatedAt = now
}

// ResolvePendingCall marks a pending call terminal. Unknown IDs are
// ignored so late or duplicate terminal events can never corrupt the
// record. Terminal states are final: resolving twice keeps the first.
func (s *Session) ResolvePendingCall(id string, status CallStatus, errMsg string) {
	if s == nil || s.Turn == nil || id == "" {
		return
	}
	if !isTerminalCallStatus(status) {
		return
	}
	for i := range s.Turn.Pending {
		p := &s.Turn.Pending[i]
		if p.ID != id || isTerminalCallStatus(p.Status) {
			continue
		}
		p.Status = status
		p.EndedAt = time.Now()
		p.Error = redact.Scrub(errMsg)
		s.UpdatedAt = p.EndedAt
		return
	}
}

// EndTurn closes the active turn with a terminal status. It only
// transitions out of in_progress, so a late Done can never overwrite an
// already-recorded cancel or interrupt. Unsettled calls settle with the
// turn (cancelled turns cancel them, interrupted ones interrupt them; a
// complete turn with a still-running call marks it interrupted, since its
// outcome was never observed). It returns true when it changed anything;
// callers should Save then.
func (s *Session) EndTurn(status TurnStatus, modelCompleted bool) bool {
	if s == nil || s.Turn == nil {
		return false
	}
	if s.Turn.Status != TurnInProgress {
		return false
	}
	switch status {
	case TurnComplete, TurnInterrupted, TurnCancelled:
		s.Turn.Status = status
	default:
		return false
	}
	now := time.Now()
	s.Turn.EndedAt = now
	if modelCompleted {
		s.Turn.ModelCompleted = modelCompleted
	}
	for i := range s.Turn.Pending {
		p := &s.Turn.Pending[i]
		if isTerminalCallStatus(p.Status) {
			continue
		}
		if status == TurnCancelled {
			p.Status = CallCancelled
		} else {
			p.Status = CallInterrupted
		}
		p.EndedAt = now
	}
	s.UpdatedAt = now
	return true
}

// CancelTurn marks the active turn and its unsettled calls cancelled.
// It is idempotent and a no-op without an in-progress turn; callers
// should Save when it reports a change.
func (s *Session) CancelTurn() bool {
	if s == nil || s.Turn == nil || s.Turn.Status != TurnInProgress {
		return false
	}
	now := time.Now()
	s.Turn.Status = TurnCancelled
	s.Turn.EndedAt = now
	for i := range s.Turn.Pending {
		p := &s.Turn.Pending[i]
		if !isTerminalCallStatus(p.Status) {
			p.Status = CallCancelled
			p.EndedAt = now
		}
	}
	s.UpdatedAt = now
	return true
}

// RecoverInterruptedTurn detects a turn that was in progress when
// Forcefield stopped unexpectedly (crash/kill) and marks it and its
// unsettled calls interrupted. Interrupted calls are terminal: recovery
// never re-executes anything, it only records. Pair with
// RepairInterruptedTurn (which synthesizes the missing tool result
// messages) and Save when it reports a change.
func (s *Session) RecoverInterruptedTurn() bool {
	if s == nil || s.Turn == nil || s.Turn.Status != TurnInProgress {
		return false
	}
	now := time.Now()
	s.Turn.Status = TurnInterrupted
	s.Turn.EndedAt = now
	for i := range s.Turn.Pending {
		p := &s.Turn.Pending[i]
		if !isTerminalCallStatus(p.Status) {
			p.Status = CallInterrupted
			p.EndedAt = now
		}
	}
	s.UpdatedAt = now
	return true
}

// AppendToolCallToLastAssistant appends one ToolCall to the last assistant
// message's ToolCalls when that message is the current turn's assistant
// tool_calls batch. If the last message is not an assistant with existing
// tool calls, it creates a new assistant message containing just this call.
// This allows the TUI to persist incremental EventToolStart events as a
// single batched assistant message per turn.
//
// Turn ownership is enforced by a time window: only messages from the last
// few seconds are considered part of the same turn. Without this, two
// separate turns that happen to be consecutive assistant tool batches (e.g.
// after a resume where the last persisted message is an old assistant batch)
// would be incorrectly coalesced, corrupting the batch.
func (s *Session) AppendToolCallToLastAssistant(call providers.ToolCall, content string) {
	content = ScrubContent(content)
	// A repeated ID reuses execution elsewhere: never persist it twice,
	// even across separate assistant batches.
	if call.ID != "" && s.hasAssistantToolCall(call.ID) {
		return
	}
	if n := len(s.Messages); n > 0 {
		last := &s.Messages[n-1]
		if last.Role == string(providers.AssistantRole) && len(last.ToolCalls) > 0 {
			// Same-turn check: last message must be recent and no tool result
			// has been interleaved (last.Role is still assistant, not tool).
			// The 10s window is generous for concurrent scheduler starts but
			// prevents cross-turn coalescing after a resume or long pause.
			if time.Since(last.Time) < 10*time.Second {
				// Deduplicate by ID in case the same call is appended twice
				for _, existing := range last.ToolCalls {
					if existing.ID == call.ID {
						return
					}
				}
				// Same sanitization as the normal persistence path so
				// incrementally recorded calls cannot smuggle raw secrets
				// or unbounded values into the session file.
				call.Arguments = sanitizePersistedArguments(call.Arguments)
				last.ToolCalls = append(last.ToolCalls, call)
				if content != "" && last.Content == "" {
					last.Content = content
				}
				s.UpdatedAt = time.Now()
				return
			}
		}
	}
	s.AddAssistantToolCalls(content, []providers.ToolCall{call})
}
