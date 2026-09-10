package recovery

import (
	"testing"
	"time"
)

func TestBudgetAllow(t *testing.T) {
	b := Budget{MaxRestarts: 3, BaseBackoff: time.Second, MaxBackoff: time.Minute}
	for _, used := range []int{0, 1, 2} {
		if !b.Allow(used) {
			t.Errorf("Allow(%d) with MaxRestarts 3 = false, want true", used)
		}
	}
	for _, used := range []int{3, 4, 100} {
		if b.Allow(used) {
			t.Errorf("Allow(%d) with MaxRestarts 3 = true, want false", used)
		}
	}

	// Zero budget restarts nothing; negative use counts as zero spent.
	zero := Budget{}
	if zero.Allow(0) {
		t.Error("zero Budget must not allow restarts")
	}
	if !b.Allow(-1) {
		t.Error("negative used must count as zero spent")
	}
}

func TestBudgetBackoff(t *testing.T) {
	b := Budget{MaxRestarts: 5, BaseBackoff: 5 * time.Second, MaxBackoff: time.Minute}
	want := []time.Duration{
		5 * time.Second,
		10 * time.Second,
		20 * time.Second,
		40 * time.Second,
		60 * time.Second, // capped
		60 * time.Second, // stays capped
	}
	for attempt, w := range want {
		if got := b.Backoff(attempt); got != w {
			t.Errorf("Backoff(%d) = %v, want %v", attempt, got, w)
		}
	}
	if got := b.Backoff(-3); got != 5*time.Second {
		t.Errorf("Backoff(-3) = %v, want base 5s", got)
	}

	// Unset bounds resolve to the documented defaults.
	var unset Budget
	if got := unset.Backoff(0); got != DefaultBaseBackoff {
		t.Errorf("unset Backoff(0) = %v, want %v", got, DefaultBaseBackoff)
	}
	if got := unset.Backoff(100); got != DefaultMaxBackoff {
		t.Errorf("unset Backoff(100) = %v, want cap %v", got, DefaultMaxBackoff)
	}
}
