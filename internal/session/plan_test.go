package session

import (
	"os"
	"testing"
	"time"
)

// TestPlan_RoundTripsThroughSaveAndLoad is the smallest plan persistence
// behavior: a stored plan survives a save/load cycle verbatim.
func TestPlan_RoundTripsThroughSaveAndLoad(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".forcefield") })

	s := New()
	s.Plan = &PlanState{Body: "1. Add the flag.\n2. Test it.", Status: PlanDraft}

	if err := s.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Plan == nil || loaded.Plan.Body != s.Plan.Body || loaded.Plan.Status != PlanDraft {
		t.Fatalf("round-tripped plan = %+v, want %+v", loaded.Plan, s.Plan)
	}
}

func TestPlan_OldFilesLoadWithNilPlan(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".forcefield") })

	s := New()
	s.AddMessage("user", "hello")

	if err := s.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Plan != nil {
		t.Fatalf("loaded plan = %+v, want nil for pre-plan files", loaded.Plan)
	}
}

func TestPlan_IgnoredByProviderMessages(t *testing.T) {
	s := New()
	s.AddMessage("user", "do the thing")
	s.Plan = &PlanState{Body: "the plan", Status: PlanDraft}

	pm := s.ProviderMessages()
	if len(pm) != 1 || pm[0].Content != "do the thing" {
		t.Fatalf("ProviderMessages() = %+v, want only the conversation", pm)
	}
	for _, m := range pm {
		if m.Content == "the plan" {
			t.Fatalf("plan body leaked into provider messages: %+v", pm)
		}
	}
}

func TestPlan_FullStateRoundTrips(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll(".forcefield") })

	created := time.Now().Truncate(time.Second)
	s := New()
	s.AddMessage("user", "task")
	s.Plan = &PlanState{
		Body:         "steps",
		Status:       PlanPartial,
		CreatedAt:    created,
		BaseTree:     "deadbeef",
		BaseMsgCount: 4,
	}

	if err := s.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := Load(s.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	got := loaded.Plan
	if got == nil || got.Body != "steps" || got.Status != PlanPartial ||
		!got.CreatedAt.Equal(created) || got.BaseTree != "deadbeef" || got.BaseMsgCount != 4 {
		t.Fatalf("round-tripped plan = %+v, want full state", got)
	}
}
