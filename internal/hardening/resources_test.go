package hardening

import (
	"strings"
	"testing"

	"forcefield/internal/memory"
	"forcefield/internal/runtime"
	"forcefield/internal/task"
)

// P1.15 lab: resource bounds. Every resource must have a documented
// lifecycle; unbounded growth must be detected, not silently consumed.
func TestLimitsZeroMeansUnlimitedDocumented(t *testing.T) {
	// RC5 Limits{<=0} means "no limit". This test documents the hazard:
	// a zero-limit run has no iteration ceiling. P1.19 must either clamp
	// config-provided zeros to defaults or add a hard safety ceiling with
	// an observable policy (not silent discard).
	zero := runtime.Limits{MaxIterations: 0, MaxToolCalls: 0, MaxConsecutiveFailures: 0}
	if zero.MaxIterations != 0 {
		t.Fatalf("unexpected limits shape")
	}
	t.Logf("limits<=0 currently means unlimited; P1.19 must bound or explicitly accept with docs")
}

func TestTaskStateBounded(t *testing.T) {
	// Reproduction for H01 subset: Discoveries/Blockers/Plan append forever
	// and inflate the pinned system prompt.
	st := task.New("goal")
	for i := 0; i < 500; i++ {
		st.Apply(task.Patch{Discovery: "discovery number"})
		st.Apply(task.Patch{Blocker: "blocker number"})
	}
	snap := st.Snapshot()
	if len(snap.Discoveries) > 64 || len(snap.Blockers) > 64 {
		t.Fatalf("task state unbounded: discoveries=%d blockers=%d (want bounded <=64 each)", len(snap.Discoveries), len(snap.Blockers))
	}
}

func TestMemoryStoreBounded(t *testing.T) {
	// Reproduction: memory has no maxEntries/maxBytes; FormatForPrompt joins
	// all entries into the system prompt.
	dir := t.TempDir()
	_ = dir
	entries := make([]memory.Entry, 0, 500)
	for i := 0; i < 500; i++ {
		entries = append(entries, memory.Entry{ID: "x", Text: strings.Repeat("fact ", 20)})
	}
	text := memory.FormatForPrompt(entries)
	if len(text) > 32<<10 {
		t.Fatalf("memory prompt unbounded: %d bytes for 500 entries (want cap)", len(text))
	}
}
