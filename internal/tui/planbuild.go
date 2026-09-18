package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/recovery"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// turnKind tracks what the in-flight turn is for. Chat and build turns
// share the runtime's normal agent policy; only plan turns narrow it to
// the read-only subset (see internal/runtime/planmode.go).
type turnKind int

const (
	turnChat turnKind = iota
	turnPlan
	turnBuild
)

// turnStartedMsg kicks the stream pump for turns begun from slash
// commands (/plan, /build). Chat submits return their pump command
// directly from handleKey; commands run inside Dispatch with no command
// channel, so they deliver this message through notify (program.Send)
// instead. Update drops it unless the same generation is still waiting.
type turnStartedMsg struct{ gen uint64 }

// StartPlan begins a read-only planning turn for /plan through the same
// pipeline as a chat submit: user message, heal, save, then the runtime
// stream in plan mode.
func (m *model) StartPlan(task string) error {
	return m.beginTurn(task, turnPlan)
}

// StartBuild executes the accepted plan for /build. A missing or empty
// plan is an error; workspace or conversation drift since /plan warns
// but never blocks.
func (m *model) StartBuild() error {
	if m.runActive() {
		return fmt.Errorf("a run is already in progress — wait for it or cancel it first")
	}
	if m.session == nil || m.session.Plan == nil ||
		strings.TrimSpace(m.session.Plan.Body) == "" {
		return fmt.Errorf("No plan yet — run /plan <task> first.")
	}
	plan := m.session.Plan
	if warn := m.planDrift(plan); warn != "" {
		m.Println("WARNING: %s — proceeding anyway.", warn)
	}
	plan.Status = session.PlanBuilding
	if err := m.session.Save(); err != nil {
		return fmt.Errorf("persist plan state: %w", err)
	}
	return m.beginTurn("Build the accepted plan:\n\n"+plan.Body, turnBuild)
}

// beginTurn records task exactly like a chat submit and opens the runtime
// stream under kind's policy. It shares the chat path's ordering
// (transcript, session, heal, save, stream) so planning and build turns
// persist, recover, and cancel identically.
func (m *model) beginTurn(task string, kind turnKind) error {
	if m.runActive() {
		return fmt.Errorf("a run is already in progress — wait for it or cancel it first")
	}
	if m.runtime == nil || m.session == nil {
		return fmt.Errorf("runtime not available")
	}
	m.entries = append(m.entries, chatEntry{Role: roleUser, Content: task})
	m.session.AddMessage("user", task)
	// A previous turn may have been cancelled after its tool_calls batch
	// was persisted but before results arrived; heal before the new turn
	// is replayed so every call the provider sees has a result.
	recovery.Heal(m.session)
	if err := m.session.Save(); err != nil {
		m.entries = append(m.entries, chatEntry{
			Role:    roleError,
			Content: fmt.Sprintf("failed to save session: %v", err),
		})
	}

	mode := runtime.ModeChat
	if kind == turnPlan {
		mode = runtime.ModePlan
	}
	streamCtx, cancel := context.WithCancel(context.Background())
	stream, err := m.runtime.StreamChatWithMode(streamCtx, m.session.ProviderMessages(), mode)
	if err != nil {
		cancel()
		m.waiting = false
		m.entries = append(m.entries, chatEntry{Role: roleError, Content: fmt.Sprintf("stream failed: %v", err)})
		m.refreshTranscript()
		return nil
	}

	m.stream = stream
	m.cancelStream = cancel
	m.streamGen++
	m.loadingFrame = 0
	m.waiting = true
	m.following = true
	m.turnKind = kind
	m.planBuffer = ""
	m.refreshTranscript()
	if m.notify != nil {
		m.notify(turnStartedMsg{gen: m.streamGen})
	}
	return nil
}

// planDrift describes how the world moved since the plan was accepted:
// plan status, conversation growth, and workspace tree changes. Empty
// means the plan still matches its acceptance snapshot.
func (m *model) planDrift(plan *session.PlanState) string {
	var parts []string
	if plan.Status != session.PlanDraft {
		parts = append(parts, fmt.Sprintf("plan is %s, not a fresh draft", plan.Status))
	}
	if m.session != nil {
		if grown := len(m.session.Messages) - plan.BaseMsgCount; grown > 0 {
			parts = append(parts, fmt.Sprintf("conversation grew by %d messages since /plan", grown))
		}
	}
	if plan.BaseTree != "" && m.runtime != nil {
		if cur, err := m.runtime.TreeSignature(context.Background()); err == nil && cur != "" && cur != plan.BaseTree {
			parts = append(parts, "workspace changed since /plan")
		}
	}
	return strings.Join(parts, "; ")
}

// acceptPlanBody stores a finished planning turn's text as the session's
// draft plan. An empty turn stores nothing; either way the transcript
// says what happened.
func (m *model) acceptPlanBody(body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		m.Println("Plan turn produced no text — no plan saved.")
		return
	}
	tree := ""
	if m.runtime != nil {
		if sig, err := m.runtime.TreeSignature(context.Background()); err == nil {
			tree = sig
		}
	}
	count := 0
	if m.session != nil {
		count = len(m.session.Messages)
	}
	m.session.Plan = &session.PlanState{
		Body:         body,
		Status:       session.PlanDraft,
		CreatedAt:    time.Now(),
		BaseTree:     tree,
		BaseMsgCount: count,
	}
	if err := m.session.Save(); err != nil {
		m.entries = append(m.entries, chatEntry{
			Role:    roleError,
			Content: fmt.Sprintf("failed to save plan: %v", err),
		})
		return
	}
	m.Println("Plan saved — review it, then /build to execute.")
}

// setBuildStatus moves the accepted plan to status and optionally notes
// it in the transcript. A nil plan (session switched mid-turn) is a
// no-op: there is nothing to transition.
func (m *model) setBuildStatus(status, line string) {
	if m.session == nil || m.session.Plan == nil {
		return
	}
	m.session.Plan.Status = status
	if err := m.session.Save(); err != nil {
		m.entries = append(m.entries, chatEntry{
			Role:    roleError,
			Content: fmt.Sprintf("failed to save plan: %v", err),
		})
		return
	}
	if line != "" {
		m.Println("%s", line)
	}
}

// streamPumpCmd starts the loading indicator and the first chunk read for
// the active stream. Chat submits and turnStartedMsg share it so both
// entry points pump identically.
func (m *model) streamPumpCmd() tea.Cmd {
	if loadingSupportsGradient() {
		return tea.Batch(
			loadingTickCmd(),
			waitForChunk(m.stream, m.streamGen),
		)
	}
	return waitForChunk(m.stream, m.streamGen)
}
