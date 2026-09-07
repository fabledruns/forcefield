package runtime

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"forcefield/internal/providers"
)

// EstimateTokens approximates the token cost of text. ASCII runs at ~4
// runes per token; other BMP scripts at ~2 per token; CJK/emoji at ~1 per
// token. It is deliberately conservative (overestimates) and deterministic:
// the runtime uses it only to decide what fits, never to bill or report
// usage. Empty text costs 0; non-empty text costs at least 1. Overcounting
// CJK is safe (earlier truncation); undercounting would overshoot the
// provider window.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	var quarters int
	for _, r := range text {
		switch {
		case r < 128:
			quarters++
		case r < 0x2E80:
			quarters += 2
		default:
			quarters += 4
		}
	}
	est := (quarters + 3) / 4
	if est < 1 {
		return 1
	}
	return est
}

// messageTokens estimates one message including role overhead and any
// tool calls (names + JSON-encoded arguments). Tool results count by
// content like any other message.
func messageTokens(m providers.Message) int {
	total := 4 // role + framing overhead
	total += EstimateTokens(m.Content)
	total += EstimateTokens(string(m.Role))
	total += EstimateTokens(m.Name)
	for _, tc := range m.ToolCalls {
		total += 20 // id/name framing per call
		total += EstimateTokens(tc.ID)
		total += EstimateTokens(tc.Name)
		if len(tc.Arguments) > 0 {
			if raw, err := json.Marshal(tc.Arguments); err == nil {
				total += EstimateTokens(string(raw))
			} else {
				total += EstimateTokens(fmt.Sprintf("%v", tc.Arguments))
			}
		}
	}
	total += EstimateTokens(m.ToolCallID)
	return total
}

// ContextBudget bounds what the runtime sends to the provider on one
// turn. Limit is the model's context window in tokens (0 = unknown, fall
// back to MaxMessages). Reserve is space kept for the next response and
// tool-call framing. MaxMessages caps turn count regardless of tokens so
// degenerate tiny-message histories still terminate.
type ContextBudget struct {
	// Limit is the context window in tokens; <=0 means unknown.
	Limit int
	// Reserve is tokens kept for the reply; <=0 means DefaultReserve.
	Reserve int
	// MaxMessages caps history messages (excluding system); <=0 means default.
	MaxMessages int
	// Summarize, when true, replaces evicted middle turns with a compact
	// deterministic digest instead of dropping them silently.
	Summarize bool
}

// DefaultContextBudget matches the historic behavior: message-count
// windowing only, no token bound, no summary block.
func DefaultContextBudget() ContextBudget {
	return ContextBudget{
		Limit:       0,
		Reserve:     providers.DefaultReserveTokens,
		MaxMessages: maxContextMessages,
		Summarize:   false,
	}
}

// effectiveReserve returns the reply reservation, defaulting sensibly.
func (b ContextBudget) effectiveReserve() int {
	if b.Reserve > 0 {
		return b.Reserve
	}
	return providers.DefaultReserveTokens
}

// effectiveMaxMessages returns the message cap, defaulting sensibly.
func (b ContextBudget) effectiveMaxMessages() int {
	if b.MaxMessages > 0 {
		return b.MaxMessages
	}
	return maxContextMessages
}

// effectiveTokenBudget returns the token room for history (excluding the
// reply reservation), or <=0 when the window is unknown.
func (b ContextBudget) effectiveTokenBudget() int {
	if b.Limit <= 0 {
		return 0
	}
	room := b.Limit - b.effectiveReserve()
	if room < 0 {
		return 0
	}
	return room
}

// BudgetForModel resolves a ContextBudget for a model ID with explicit
// overrides winning over the provider capability table. Zero/negative
// overrides fall back; unknown models stay at 0 (unknown) rather than a
// fabricated window.
func BudgetForModel(modelID string, limitOverride, reserveOverride, maxMsgOverride int, summarize bool) ContextBudget {
	return BudgetForCaps(modelID, limitOverride, reserveOverride, maxMsgOverride, summarize, providers.Capabilities{})
}

// BudgetForCaps is BudgetForModel with negotiated provider capabilities:
// explicit overrides win, then live provider-reported limits, then the
// static model table, then conservative defaults. A zero Capabilities
// degrades exactly to BudgetForModel.
func BudgetForCaps(modelID string, limitOverride, reserveOverride, maxMsgOverride int, summarize bool, caps providers.Capabilities) ContextBudget {
	b := DefaultContextBudget()
	b.Summarize = summarize
	if limitOverride > 0 {
		b.Limit = limitOverride
	} else if caps.ContextWindow > 0 {
		b.Limit = caps.ContextWindow
	} else if window, _ := providers.ContextLimitsForModel(modelID); window > 0 {
		b.Limit = window
	}
	if reserveOverride > 0 {
		b.Reserve = reserveOverride
	} else if caps.MaxOutputTokens > 0 {
		b.Reserve = caps.MaxOutputTokens
	} else if _, maxOut := providers.ContextLimitsForModel(modelID); maxOut > 0 {
		b.Reserve = maxOut
	}
	if maxMsgOverride > 0 {
		b.MaxMessages = maxMsgOverride
	}
	return b
}

// contextSelection describes what SelectContext kept for observability.
type contextSelection struct {
	// Kept counts history messages kept (excluding system/summary).
	Kept int
	// Evicted counts history messages dropped or digested.
	Evicted int
	// EstimatedTokens counts the selected window (system + summary + kept).
	EstimatedTokens int
	// Summarized reports whether a digest block was inserted.
	Summarized bool
}

// SelectContext windows history to fit budget, always preserving the
// system prompt, the first user message (task goal), recent turns, and
// tool call/result pairing. It never splits an assistant tool_calls
// message from its following tool results: they form one atomic group.
//
// Layout: system + [goal, if outside the tail] + [digest, if evicted and
// Summarize] + most-recent groups that fit both the token room and the
// message cap. When the window is unknown (Limit<=0) only the message
// cap applies — the historic behavior, now pair-aware.
func (b ContextBudget) SelectContext(system providers.Message, history []providers.Message) ([]providers.Message, contextSelection) {
	sel := contextSelection{}
	if len(history) == 0 {
		out := []providers.Message{system}
		sel.EstimatedTokens = messageTokens(system)
		return out, sel
	}

	maxMsgs := b.effectiveMaxMessages()
	tokenRoom := b.effectiveTokenBudget() // <=0 = unknown, count-only
	systemTokens := messageTokens(system)

	// Fast path: everything fits by count, and by tokens when known.
	if len(history) <= maxMsgs {
		if tokenRoom <= 0 {
			out := make([]providers.Message, 0, len(history)+1)
			out = append(out, system)
			out = append(out, history...)
			total := systemTokens
			for _, m := range history {
				total += messageTokens(m)
			}
			sel.Kept = len(history)
			sel.EstimatedTokens = total
			return out, sel
		}
		total := systemTokens
		for _, m := range history {
			total += messageTokens(m)
		}
		if total <= tokenRoom {
			out := make([]providers.Message, 0, len(history)+1)
			out = append(out, system)
			out = append(out, history...)
			sel.Kept = len(history)
			sel.EstimatedTokens = total
			return out, sel
		}
	}

	groups := groupHistory(history)
	goalIdx := goalGroupIndex(history, groups)

	// Greedily keep the most recent groups that fit. The goal group is
	// pinned separately when it falls outside the tail.
	room := tokenRoom
	if room <= 0 {
		room = 1 << 30 // count-only mode: token room unbounded
	}
	room -= systemTokens

	// Reserve goal cost up front when it must be pinned, so the tail
	// cannot crowd it out.
	goalTokens := 0
	goalLen := 0
	pinGoal := goalIdx >= 0
	if pinGoal {
		goalLen = len(groups[goalIdx].members)
		for _, mi := range groups[goalIdx].members {
			goalTokens += messageTokens(history[mi])
		}
	}

	// Walk newest-first collecting groups that fit both caps. While the
	// goal still needs pinning, the tail reserves one slot for it, so a
	// pinned goal plus tail never exceeds the message cap (matching the
	// historic system + goal + tail bound).
	kept := make([]bool, len(groups))
	keptCount := 0  // history messages kept
	keptTokens := 0 // tokens of kept groups (excl. system/goal-reserve)
	need := pinGoal // whether the goal still needs pinning
	for gi := len(groups) - 1; gi >= 0; gi-- {
		gTokens := 0
		for _, mi := range groups[gi].members {
			gTokens += messageTokens(history[mi])
		}
		allowance := room
		if need {
			allowance -= goalTokens
		}
		msgCap := maxMsgs
		if need && gi != goalIdx {
			msgCap -= goalLen
		}
		if keptCount+len(groups[gi].members) > msgCap {
			// Message cap reached — but the goal pin still applies
			// below even if it also exceeds the cap by itself.
			if !(need && gi == goalIdx) {
				continue
			}
		}
		if keptTokens+gTokens > allowance {
			if !(need && gi == goalIdx) {
				continue
			}
		}
		kept[gi] = true
		keptTokens += gTokens
		keptCount += len(groups[gi].members)
		if gi == goalIdx {
			need = false
		}
	}

	out := make([]providers.Message, 0, keptCount+3)
	out = append(out, system)
	total := systemTokens

	// Pin the goal first when it was not in the kept tail.
	if pinGoal && !kept[goalIdx] {
		for _, mi := range groups[goalIdx].members {
			out = append(out, history[mi])
			total += messageTokens(history[mi])
		}
		sel.Kept += len(groups[goalIdx].members)
	}

	// Count evicted groups (excluding the pinned goal).
	evictedGroups := 0
	evictedMsgs := 0
	for gi, k := range kept {
		if k || (gi == goalIdx && pinGoal && !kept[gi]) {
			continue
		}
		evictedGroups++
		evictedMsgs += len(groups[gi].members)
	}
	_ = evictedGroups

	// Digest of evicted middle when enabled and something was dropped.
	if b.Summarize && evictedMsgs > 0 {
		digest := digestEvicted(history, groups, kept, goalIdx, pinGoal)
		digestMsg := providers.Message{Role: providers.UserRole, Content: digest}
		out = append(out, digestMsg)
		total += messageTokens(digestMsg)
		sel.Summarized = true
	}

	for gi := range groups {
		if !kept[gi] {
			continue
		}
		for _, mi := range groups[gi].members {
			out = append(out, history[mi])
			total += messageTokens(history[mi])
		}
		sel.Kept += len(groups[gi].members)
	}
	// When the goal was pinned AND kept (goal inside tail), the loop
	// above already emitted it once — correct. When pinned and not kept,
	// Kept counts pin + tail without duplication.
	sel.Evicted = len(history) - sel.Kept
	sel.EstimatedTokens = total
	return out, sel
}

// historyGroup is one atomic unit: either a lone message or an
// assistant tool_calls message plus its consecutive tool results.
type historyGroup struct {
	members []int // indexes into history
}

// groupHistory folds history into atomic groups so selection never
// orphans a tool result from its call or vice versa.
func groupHistory(history []providers.Message) []historyGroup {
	var groups []historyGroup
	i := 0
	for i < len(history) {
		m := history[i]
		if m.Role == providers.AssistantRole && len(m.ToolCalls) > 0 {
			g := historyGroup{members: []int{i}}
			i++
			for i < len(history) && history[i].Role == providers.ToolRole {
				g.members = append(g.members, i)
				i++
			}
			groups = append(groups, g)
			continue
		}
		// Stray tool results without a preceding assistant call still
		// group with adjacent tool results to avoid splitting a batch.
		if m.Role == providers.ToolRole {
			g := historyGroup{members: []int{i}}
			i++
			for i < len(history) && history[i].Role == providers.ToolRole {
				g.members = append(g.members, i)
				i++
			}
			groups = append(groups, g)
			continue
		}
		groups = append(groups, historyGroup{members: []int{i}})
		i++
	}
	return groups
}

// goalGroupIndex finds the group holding the first non-empty user
// message (the task goal), or -1 when there is none.
func goalGroupIndex(history []providers.Message, groups []historyGroup) int {
	for gi, g := range groups {
		for _, mi := range g.members {
			if history[mi].Role == providers.UserRole && strings.TrimSpace(history[mi].Content) != "" {
				return gi
			}
		}
	}
	return -1
}

// digestEvicted renders a deterministic extractive digest of dropped
// groups: counts plus one short stub per evicted user/assistant turn and
// the tool names used. It contains no model-generated text and never
// includes full tool output — just enough to preserve intent.
func digestEvicted(history []providers.Message, groups []historyGroup, kept []bool, goalIdx int, pinGoal bool) string {
	type stub struct {
		role  string
		text  string
		tools []string
	}
	var stubs []stub
	toolSet := map[string]struct{}{}
	evicted := 0
	for gi, g := range groups {
		if kept[gi] {
			continue
		}
		if gi == goalIdx && pinGoal {
			continue // pinned, not evicted
		}
		evicted += len(g.members)
		for _, mi := range g.members {
			m := history[mi]
			for _, tc := range m.ToolCalls {
				toolSet[tc.Name] = struct{}{}
			}
			if m.Role == providers.ToolRole {
				continue // results summarized by tool-use counts
			}
			text := strings.TrimSpace(m.Content)
			if text == "" {
				continue
			}
			if len(stubs) >= 10 {
				continue
			}
			const maxStub = 200
			if len(text) > maxStub {
				cut := maxStub
				for cut > 0 && !utf8.ValidString(text[:cut]) {
					cut--
				}
				text = text[:cut] + "…"
			}
			var tools []string
			for _, tc := range m.ToolCalls {
				tools = append(tools, tc.Name)
			}
			stubs = append(stubs, stub{role: string(m.Role), text: text, tools: tools})
			_ = tools
		}
	}
	var names []string
	for n := range toolSet {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	fmt.Fprintf(&b, "[Context compacted: %d older message(s) omitted to fit the model's context window. ", evicted)
	if len(names) > 0 {
		fmt.Fprintf(&b, "Tools used in omitted turns: %s. ", strings.Join(names, ", "))
	}
	b.WriteString("Key excerpts preserved below; recent turns follow in full.]\n")
	for _, s := range stubs {
		if len(s.tools) > 0 {
			fmt.Fprintf(&b, "- %s (tools: %s): %s\n", s.role, strings.Join(s.tools, ","), s.text)
		} else {
			fmt.Fprintf(&b, "- %s: %s\n", s.role, s.text)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
