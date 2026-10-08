package runtime

import (
	"forcefield/internal/agent"
	"forcefield/internal/providers"
	"forcefield/internal/session"
	"forcefield/internal/task"
)

func (r *Runtime) buildMessages(history []providers.Message) []providers.Message {
	if r == nil {
		return append([]providers.Message{{Role: providers.SystemRole}}, history...)
	}
	r.mu.RLock()
	agent := r.agent
	cfg := r.cfg
	provider := r.provider
	activeAgent := r.activeAgent
	r.mu.RUnlock()
	budget := DefaultContextBudget()
	if cfg != nil {
		budget = contextBudgetFromConfig(cfg, cfg.Model.Name, activeAgent, providers.ResolveCapabilities(provider, cfg.Model.Name))
	}
	return buildMessagesWithBudget(history, agent, budget)
}

// buildMessagesWithAgent builds the bounded history window for an explicit
// agent snapshot so background runs never read live switchable state.
// It uses the default budget (message-count windowing, pair-aware); use
// buildMessagesWithBudget when a resolved per-model budget is available.
func buildMessagesWithAgent(history []providers.Message, agent *agent.Agent) []providers.Message {
	return buildMessagesWithBudget(history, agent, DefaultContextBudget())
}

// buildMessagesWithBudget is buildMessagesWithAgent with an explicit
// context budget: token-aware when the model's window is known, otherwise
// message-count windowing. It always preserves the system prompt, the
// first user message (task goal), recent turns, and tool call/result
// pairing, reserving space for the next model response.
func buildMessagesWithBudget(history []providers.Message, agent *agent.Agent, budget ContextBudget) []providers.Message {
	prompt := ""
	if agent != nil {
		prompt = agent.BuildSystemPrompt()
	}
	system := providers.Message{
		Role:    providers.SystemRole,
		Content: prompt,
	}
	out, _ := budget.SelectContext(system, history)
	return out
}

// windowForProvider splits a full run history (system at [0]) into the
// budgeted provider view for one turn. The full history is never mutated:
// callers keep appending to it while each model request stays within the
// snapshot's context budget.
func windowForProvider(messages []providers.Message, snap runSnapshot) []providers.Message {
	if len(messages) == 0 {
		return buildMessagesWithBudget(nil, snap.agent, snap.contextBudget)
	}
	if messages[0].Role != providers.SystemRole {
		return buildMessagesWithBudget(messages, snap.agent, snap.contextBudget)
	}
	out, _ := snap.contextBudget.SelectContext(messages[0], messages[1:])
	return out
}

// estimateMessagesTokens sums the context-budget token estimates for a
// provider-bound message window. Used for trace metadata only.
func estimateMessagesTokens(messages []providers.Message) int {
	total := 0
	for _, m := range messages {
		total += messageTokens(m)
	}
	return total
}

// buildPromptBase renders the run-fixed part of the system prompt: the
// agent's base prompt plus the mode overlay (so plan-mode runs keep
// their constraint on every iteration). The second result is false when
// there is no agent, in which case refreshSystemPrompt leaves the
// system message untouched, exactly as before.
func buildPromptBase(snap runSnapshot) (string, bool) {
	if snap.agent == nil {
		return "", false
	}
	return snap.agent.BuildSystemPrompt() + snap.mode.planOverlay(), true
}

// refreshSystemPrompt adds the current task digest to the system message.
// base is the run-fixed prompt from buildPromptBase; only the digest
// suffix is recomputed per iteration.
func refreshSystemPrompt(messages []providers.Message, base string, ok bool, state *task.State) {
	if len(messages) == 0 || messages[0].Role != providers.SystemRole {
		return
	}
	if !ok {
		return
	}

	summary := state.Summary()
	if summary == "" {
		messages[0].Content = base
		return
	}

	messages[0].Content = base + "\n\n## Current Task State\n\n" + summary +
		"\n\nUpdate this via update_task_state as your understanding of the task evolves."
}

// truncateToolResult caps one tool result to the model-visible window.
// It delegates to the session layer so the run loop and session replay
// share one implementation (see session.TruncateModelToolResult).
func truncateToolResult(content string) string {
	return session.TruncateModelToolResult(content)
}

// goalFrom returns the first user message for task-state display.
func goalFrom(messages []providers.Message) string {
	for _, m := range messages {
		if m.Role == providers.UserRole && m.Content != "" {
			return m.Content
		}
	}
	return ""
}
