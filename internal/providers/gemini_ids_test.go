package providers

import (
	"regexp"
	"testing"
)

// TestSyntheticCallIDsSurviveRestart pins that minted Gemini tool-call IDs
// can never collide with IDs persisted by an earlier process. The previous
// process-local counter restarted at call-1 on every launch, so a resumed
// session already containing call-1 would silently drop the next run's
// first call from history. UUID-based IDs never share that namespace.
func TestSyntheticCallIDsSurviveRestart(t *testing.T) {
	legacy := regexp.MustCompile(`^call-[0-9]+$`)
	// A session written by an old binary can contain any counter value;
	// model one already holding the first counter ID.
	persisted := map[string]bool{"call-1": true}

	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := nextSyntheticCallID()
		if id == "" {
			t.Fatal("empty synthetic ID")
		}
		if legacy.MatchString(id) {
			t.Fatalf("synthetic ID %q reuses the legacy counter namespace and can collide after restart", id)
		}
		if persisted[id] {
			t.Fatalf("synthetic ID %q collides with a persisted ID", id)
		}
		if seen[id] {
			t.Fatalf("duplicate synthetic ID %q", id)
		}
		seen[id] = true
	}
}
