package runtime

import (
	"testing"

	"forcefield/internal/providers"
)

func TestUsageInfo_EstimatesHistory(t *testing.T) {
	r := newTestRuntime(&scriptedProvider{turns: testTurns()})
	history := []providers.Message{
		{Role: providers.UserRole, Content: "hello there, this is a test message"},
		{Role: providers.AssistantRole, Content: "hi back"},
	}
	info := r.UsageInfo(history)
	if info.EstTokens <= 0 {
		t.Fatalf("UsageInfo.EstTokens = %d, want > 0", info.EstTokens)
	}
	if info.MaxMessages != maxContextMessages {
		t.Fatalf("UsageInfo.MaxMessages = %d, want %d", info.MaxMessages, maxContextMessages)
	}
	// No config on the test runtime: the window is unknown, never fabricated.
	if info.Limit != 0 {
		t.Fatalf("UsageInfo.Limit = %d, want 0 (unknown)", info.Limit)
	}
	if info.Kept != len(history) || info.Evicted != 0 {
		t.Fatalf("UsageInfo selection = %d kept, %d evicted; want %d kept, 0 evicted",
			info.Kept, info.Evicted, len(history))
	}
}

func TestUsageInfo_NilRuntimeIsZero(t *testing.T) {
	var r *Runtime
	if info := r.UsageInfo(nil); info != (UsageInfo{}) {
		t.Fatalf("nil UsageInfo = %+v, want zero", info)
	}
}
