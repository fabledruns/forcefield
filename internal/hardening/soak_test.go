package hardening

import (
	"testing"

	"forcefield/internal/session"
)

// P1.15 lab: accelerated soak modeling 60-100 iteration growth mechanisms
// deterministically (no 5-day CI run).
func TestAcceleratedSoakMessageGrowthBounded(t *testing.T) {
	t.Chdir(t.TempDir())
	sess := session.New()
	// Model 100 iterations x (1 assistant + 1 tool message).
	for i := 0; i < 100; i++ {
		sess.AddAssistantToolCalls("assistant turn output", nil)
		sess.AddToolResult("call-id", "echo", "tool result content")
	}
	if err := sess.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := session.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Messages) > 1000 {
		t.Fatalf("soak grew past session cap: %d", len(loaded.Messages))
	}
	t.Logf("100-iteration soak: %d messages persisted (cap 1000)", len(loaded.Messages))
}

func TestRepeatedCompactionStable(t *testing.T) {
	t.Chdir(t.TempDir())
	sess := session.New()
	for round := 0; round < 5; round++ {
		for i := 0; i < 400; i++ {
			sess.AddMessage("user", "round filler")
		}
		if err := sess.Save(); err != nil {
			t.Fatalf("round %d Save: %v", round, err)
		}
		loaded, err := session.Load(sess.ID)
		if err != nil {
			t.Fatalf("round %d Load: %v", round, err)
		}
		if len(loaded.Messages) > 1000 {
			t.Fatalf("round %d grew past cap: %d", round, len(loaded.Messages))
		}
		*sess = *loaded
	}
}
