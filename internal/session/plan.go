package session

import "time"

// Plan statuses: a plan is drafted by /plan, moves to building while
// /build runs it, and ends done or partial (cancelled, errored, or
// blocked). Partial plans keep their body so a later /build can continue.
const (
	PlanDraft    = "draft"
	PlanBuilding = "building"
	PlanDone     = "done"
	PlanPartial  = "partial"
)

// PlanState is the persisted /plan and /build state for one session. It
// is run-management metadata like Turn: replay and the transcript ignore
// it. Nil means no plan was ever accepted in this session. Every field is
// additive and omitempty, so old session files load with a nil plan.
type PlanState struct {
	// Body is the accepted plan text, exactly as the planning turn wrote it.
	Body string `json:"body"`
	// Status is one of PlanDraft, PlanBuilding, PlanDone, PlanPartial.
	Status string `json:"status"`
	// CreatedAt is when the plan was accepted.
	CreatedAt time.Time `json:"created_at,omitempty"`
	// BaseTree is the hex hash of the workspace status output when the
	// plan was accepted, empty when the workspace is not a git
	// repository or git is unavailable. /build compares it to warn
	// about drift, never to block.
	BaseTree string `json:"base_tree,omitempty"`
	// BaseMsgCount is len(Messages) when the plan was accepted, used to
	// notice conversation drift since planning.
	BaseMsgCount int `json:"base_msg_count,omitempty"`
}

// clonePlan deep-copies a plan envelope for Save rollback so a failed
// save restores plan state exactly.
func clonePlan(p *PlanState) *PlanState {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}
