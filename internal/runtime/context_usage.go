package runtime

import "forcefield/internal/providers"

// UsageInfo describes estimated context consumption for one history.
// EstTokens is a deterministic local estimate built with EstimateTokens —
// it is never billed usage. Limit <= 0 means the model's window is
// unknown; callers must fall back to message-count bounding and say so.
// Kept/Evicted preview the turn-window selection.
type UsageInfo struct {
	EstTokens   int
	Limit       int
	Reserve     int
	MaxMessages int
	Kept        int
	Evicted     int
	Summarize   bool
}

// UsageInfo estimates the token cost of history against the active
// per-turn budget. It reads a consistent snapshot and never starts a
// model turn, so commands can report usage without side effects.
func (r *Runtime) UsageInfo(history []providers.Message) UsageInfo {
	var out UsageInfo
	if r == nil {
		return out
	}
	snap := r.snapshotRunState()
	for _, m := range history {
		out.EstTokens += messageTokens(m)
	}
	out.Limit = snap.contextBudget.Limit
	out.Reserve = snap.contextBudget.effectiveReserve()
	out.MaxMessages = snap.contextBudget.effectiveMaxMessages()
	out.Summarize = snap.contextBudget.Summarize
	prompt := ""
	if snap.agent != nil {
		prompt = snap.agent.BuildSystemPrompt()
	}
	_, sel := snap.contextBudget.SelectContext(
		providers.Message{Role: providers.SystemRole, Content: prompt}, history)
	out.Kept = sel.Kept
	out.Evicted = sel.Evicted
	return out
}
