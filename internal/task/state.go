// Package task defines TaskState: the compact, persistent working memory
// the agent runtime keeps for a single goal. It tracks the plan, what's
// been discovered, what's blocking progress, and how the task eventually
// resolved, without requiring the model to re-derive that context from
// raw conversation history on every turn.
package task

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// StepStatus is the state of a single plan step.
type StepStatus string

const (
	StepPending    StepStatus = "pending"
	StepInProgress StepStatus = "in_progress"
	StepDone       StepStatus = "done"
	StepBlocked    StepStatus = "blocked"
)

// Step is one item in the agent's plan.
type Step struct {
	Text   string     `json:"text"`
	Status StepStatus `json:"status"`
}

// Verification is how thoroughly the agent has checked its own work.
type Verification string

const (
	VerificationNone       Verification = "none"
	VerificationInProgress Verification = "in_progress"
	VerificationPassed     Verification = "passed"
	VerificationFailed     Verification = "failed"
)

// Status is the outcome of a run, reported once the model stops issuing
// tool calls. It is never "verified" unless the model actually recorded a
// passing verification step.
type Status string

const (
	StatusInProgress Status = "in_progress"
	StatusVerified   Status = "verified"
	StatusPartial    Status = "partial"
	StatusBlocked    Status = "blocked"
	StatusFailed     Status = "failed"
)

// ToolActivity is a compact per-tool-name execution count, so the model
// (and anything inspecting a persisted task) can see how much work has
// happened without every raw tool result being replayed.
type ToolActivity struct {
	Name     string `json:"name"`
	Calls    int    `json:"calls"`
	Failures int    `json:"failures"`
}

// Snapshot is a plain, mutex-free copy of State suitable for JSON
// persistence (e.g. inside a session file) or display.
type Snapshot struct {
	Goal                string         `json:"goal"`
	Status              Status         `json:"status"`
	Iteration           int            `json:"iteration"`
	Phase               string         `json:"phase"`
	Plan                []Step         `json:"plan"`
	CurrentStep         string         `json:"current_step"`
	Discoveries         []string       `json:"discoveries"`
	Blockers            []string       `json:"blockers"`
	Verification        Verification   `json:"verification"`
	VerificationNote    string         `json:"verification_note"`
	ToolCalls           int            `json:"tool_calls"`
	ConsecutiveFailures int            `json:"consecutive_failures"`
	ToolActivity        []ToolActivity `json:"tool_activity"`
}

// State is the mutable, concurrency-safe working memory for one run. A new
// State is created per top-level task and threaded through the run via
// context.
type State struct {
	mu sync.Mutex
	s  Snapshot

	activity map[string]*ToolActivity
}

// New creates a fresh State for goal.
func New(goal string) *State {
	return &State{
		s: Snapshot{
			Goal:         goal,
			Status:       StatusInProgress,
			Verification: VerificationNone,
		},
		activity: make(map[string]*ToolActivity),
	}
}

// BeginIteration increments and returns the current iteration count.
func (t *State) BeginIteration() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.s.Iteration++
	return t.s.Iteration
}

func (t *State) Iteration() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.s.Iteration
}

// RecordTool logs one completed tool call outcome. Consecutive tool
// failures accumulate ConsecutiveFailures, which the runtime uses to
// detect a stuck agent; any success resets the streak.
func (t *State) RecordTool(name string, success bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.s.ToolCalls++

	a, ok := t.activity[name]
	if !ok {
		a = &ToolActivity{Name: name}
		t.activity[name] = a
	}
	a.Calls++
	if !success {
		a.Failures++
		t.s.ConsecutiveFailures++
	} else {
		t.s.ConsecutiveFailures = 0
	}
}

func (t *State) ToolCallCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.s.ToolCalls
}

func (t *State) ConsecutiveFailures() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.s.ConsecutiveFailures
}

// Patch is a partial update to task state, as submitted by the model via
// the update_task_state tool. Every field is optional; only non-zero
// fields are applied.
type Patch struct {
	Phase            string
	Plan             []Step // non-nil replaces the plan wholesale
	CurrentStep      string
	Discovery        string // appended to Discoveries
	Blocker          string // appended to Blockers
	ClearBlockers    bool
	Verification     Verification
	VerificationNote string
	Status           Status
}

// Apply merges p into the state.
func (t *State) Apply(p Patch) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if p.Phase != "" {
		t.s.Phase = p.Phase
	}
	if p.Plan != nil {
		t.s.Plan = p.Plan
	}
	if p.CurrentStep != "" {
		t.s.CurrentStep = p.CurrentStep
	}
	if p.Discovery != "" {
		t.s.Discoveries = append(t.s.Discoveries, p.Discovery)
	}
	if p.ClearBlockers {
		t.s.Blockers = nil
	}
	if p.Blocker != "" {
		t.s.Blockers = append(t.s.Blockers, p.Blocker)
	}
	if p.Verification != "" {
		t.s.Verification = p.Verification
	}
	if p.VerificationNote != "" {
		t.s.VerificationNote = p.VerificationNote
	}
	if p.Status != "" {
		t.s.Status = p.Status
	}
}

// SetStatus force-sets the overall status, used by the runtime itself
// (e.g. when a limit is hit) rather than the model.
func (t *State) SetStatus(status Status) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.s.Status = status
}

// FinalStatus computes the run's outcome once the model has stopped
// requesting tools. It never reports "verified" unless the model itself
// recorded a passing verification; a task that used tools but never
// verified is reported as "partial" rather than silently upgraded.
func (t *State) FinalStatus() Status {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.s.Status == StatusBlocked || t.s.Status == StatusFailed {
		return t.s.Status
	}
	switch t.s.Verification {
	case VerificationPassed:
		return StatusVerified
	case VerificationFailed:
		return StatusFailed
	}
	if len(t.s.Blockers) > 0 {
		return StatusBlocked
	}
	if t.s.ToolCalls == 0 {
		// No tools were ever used - a plain conversational answer, not a
		// multi-step task with unverified changes.
		return StatusVerified
	}
	return StatusPartial
}

// Snapshot returns a point-in-time, JSON-serializable copy of the state.
func (t *State) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	snap := t.s
	snap.Plan = append([]Step(nil), t.s.Plan...)
	snap.Discoveries = append([]string(nil), t.s.Discoveries...)
	snap.Blockers = append([]string(nil), t.s.Blockers...)
	snap.ToolActivity = make([]ToolActivity, 0, len(t.activity))
	for _, a := range t.activity {
		snap.ToolActivity = append(snap.ToolActivity, *a)
	}
	return snap
}

// Summary renders a compact, human/model-readable digest of the current
// state. It returns "" until the model has actually recorded anything
// beyond the bare goal, so trivial single-turn chats never see a task
// block in their prompt.
func (t *State) Summary() string {
	snap := t.Snapshot()

	if snap.Phase == "" && len(snap.Plan) == 0 && len(snap.Discoveries) == 0 &&
		len(snap.Blockers) == 0 && snap.Verification == VerificationNone && snap.CurrentStep == "" {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Iteration: %d\n", snap.Iteration)
	if snap.Phase != "" {
		fmt.Fprintf(&b, "Phase: %s\n", snap.Phase)
	}
	if snap.CurrentStep != "" {
		fmt.Fprintf(&b, "Current step: %s\n", snap.CurrentStep)
	}
	if len(snap.Plan) > 0 {
		b.WriteString("Plan:\n")
		for _, step := range snap.Plan {
			fmt.Fprintf(&b, "  [%s] %s\n", step.Status, step.Text)
		}
	}
	if len(snap.Discoveries) > 0 {
		b.WriteString("Discoveries:\n")
		for _, d := range lastN(snap.Discoveries, 8) {
			fmt.Fprintf(&b, "  - %s\n", d)
		}
	}
	if len(snap.Blockers) > 0 {
		b.WriteString("Open blockers:\n")
		for _, blk := range snap.Blockers {
			fmt.Fprintf(&b, "  - %s\n", blk)
		}
	}
	fmt.Fprintf(&b, "Verification: %s", snap.Verification)
	if snap.VerificationNote != "" {
		fmt.Fprintf(&b, " (%s)", snap.VerificationNote)
	}
	b.WriteString("\n")

	return strings.TrimRight(b.String(), "\n")
}

func lastN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

type ctxKey struct{}

// WithState returns a context carrying t, retrievable via FromContext.
func WithState(ctx context.Context, t *State) context.Context {
	return context.WithValue(ctx, ctxKey{}, t)
}

// FromContext retrieves the State stored by WithState, if any.
func FromContext(ctx context.Context) (*State, bool) {
	t, ok := ctx.Value(ctxKey{}).(*State)
	return t, ok
}
