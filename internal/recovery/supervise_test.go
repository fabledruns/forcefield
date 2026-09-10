package recovery

import (
	"context"
	"errors"
	"testing"
	"time"
)

// scriptChild replays canned child outcomes and counts invocations.
// A negative code stands in for a spawn/wait failure.
func scriptChild(codes ...int) (ChildFunc, *int) {
	calls := 0
	return func(context.Context) (int, error) {
		if calls >= len(codes) {
			return 0, errors.New("child called past end of script")
		}
		code := codes[calls]
		calls++
		if code < 0 {
			return 0, errors.New("spawn failure")
		}
		return code, nil
	}, &calls
}

// recordSleep captures waits without waiting. A nil slice only observes
// through events.
func recordSleep(waits *[]time.Duration) SleepFunc {
	return func(_ context.Context, d time.Duration) {
		if waits != nil {
			*waits = append(*waits, d)
		}
	}
}

func TestSuperviseFirstTryOutcomes(t *testing.T) {
	for _, code := range []int{ExitOK, ExitTerminal, ExitNeedsHuman} {
		child, calls := scriptChild(code)
		var waits []time.Duration
		var events []SuperviseEvent
		got := Supervise(context.Background(), Budget{MaxRestarts: 3}, child, recordSleep(&waits), func(e SuperviseEvent) {
			events = append(events, e)
			if e.Retry {
				waits = append(waits, e.Wait)
			}
		})
		if got != code {
			t.Errorf("code %d: Supervise = %d, want passthrough", code, got)
		}
		if *calls != 1 {
			t.Errorf("code %d: child called %d times, want 1 (no restart)", code, *calls)
		}
		if len(waits) != 0 {
			t.Errorf("code %d: waited %v, want no backoff", code, waits)
		}
		if len(events) != 1 || !events[0].Final || events[0].Code != code {
			t.Errorf("code %d: events = %+v, want one final event", code, events)
		}
	}
}

func TestSuperviseTerminalClassesNeverRetry(t *testing.T) {
	// ExitTerminal is what quota/billing, auth, invalid-request and
	// protocol failures classify to (see exit_test.go): the supervisor
	// must take it at face value and stop after one attempt.
	child, calls := scriptChild(ExitTerminal)
	var waits []time.Duration
	got := Supervise(context.Background(), Budget{MaxRestarts: 10}, child, recordSleep(&waits), nil)
	if got != ExitTerminal {
		t.Errorf("Supervise = %d, want %d", got, ExitTerminal)
	}
	if *calls != 1 || len(waits) != 0 {
		t.Errorf("calls = %d, waits = %v; terminal must stop after one attempt", *calls, waits)
	}
}

func TestSuperviseRetriesThenSucceeds(t *testing.T) {
	child, calls := scriptChild(ExitRetryable, ExitRetryable, ExitOK)
	budget := Budget{MaxRestarts: 5, BaseBackoff: time.Second, MaxBackoff: time.Minute}
	var events []SuperviseEvent
	got := Supervise(context.Background(), budget, child, recordSleep(nil), func(e SuperviseEvent) {
		events = append(events, e)
	})
	if got != ExitOK {
		t.Errorf("Supervise = %d, want %d", got, ExitOK)
	}
	if *calls != 3 {
		t.Errorf("child called %d times, want 3", *calls)
	}
	if len(events) != 3 || !events[0].Retry || !events[1].Retry || !events[2].Final {
		t.Fatalf("events = %+v, want retry, retry, final", events)
	}
	if events[0].Wait != budget.Backoff(0) || events[1].Wait != budget.Backoff(1) {
		t.Errorf("waits = %v, %v; want bounded exponential %v, %v",
			events[0].Wait, events[1].Wait, budget.Backoff(0), budget.Backoff(1))
	}
}

func TestSuperviseBudgetExhaustion(t *testing.T) {
	child, calls := scriptChild(ExitRetryable, ExitRetryable, ExitRetryable, ExitRetryable)
	budget := Budget{MaxRestarts: 2, BaseBackoff: time.Second, MaxBackoff: time.Minute}
	var waits []time.Duration
	var events []SuperviseEvent
	got := Supervise(context.Background(), budget, child, recordSleep(&waits), func(e SuperviseEvent) {
		events = append(events, e)
	})
	// Budget spent: stop with the last code (still interrupted), not a
	// lie about terminal failure and not another restart.
	if got != ExitRetryable {
		t.Errorf("Supervise = %d, want %d (last code, budget spent)", got, ExitRetryable)
	}
	if *calls != 3 {
		t.Errorf("child called %d times, want 1 initial + 2 restarts", *calls)
	}
	wantWaits := []time.Duration{budget.Backoff(0), budget.Backoff(1)}
	if len(waits) != len(wantWaits) {
		t.Fatalf("waits = %v, want %v", waits, wantWaits)
	}
	for i, w := range wantWaits {
		if waits[i] != w {
			t.Errorf("wait %d = %v, want %v", i, waits[i], w)
		}
	}
	last := events[len(events)-1]
	if !last.Final || !last.Exhausted || last.Code != ExitRetryable {
		t.Errorf("final event = %+v, want exhausted stop with code 3", last)
	}
}

func TestSuperviseZeroBudgetMeansOneAttempt(t *testing.T) {
	child, calls := scriptChild(ExitRetryable, ExitOK)
	var waits []time.Duration
	got := Supervise(context.Background(), Budget{}, child, recordSleep(&waits), nil)
	if got != ExitRetryable {
		t.Errorf("Supervise = %d, want %d", got, ExitRetryable)
	}
	if *calls != 1 || len(waits) != 0 {
		t.Errorf("calls = %d, waits = %v; zero budget restarts nothing", *calls, waits)
	}
}

func TestSuperviseBackoffGrowsAndCaps(t *testing.T) {
	child, _ := scriptChild(ExitRetryable, ExitRetryable, ExitRetryable, ExitRetryable, ExitRetryable, ExitRetryable)
	budget := Budget{MaxRestarts: 5, BaseBackoff: time.Second, MaxBackoff: 3 * time.Second}
	var waits []time.Duration
	Supervise(context.Background(), budget, child, recordSleep(&waits), nil)
	want := []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second, 3 * time.Second}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	for i, w := range want {
		if waits[i] != w {
			t.Errorf("wait %d = %v, want %v", i, waits[i], w)
		}
	}
}

func TestSuperviseSpawnFailureFailsClosed(t *testing.T) {
	child, calls := scriptChild(-1)
	var waits []time.Duration
	var events []SuperviseEvent
	got := Supervise(context.Background(), Budget{MaxRestarts: 5}, child, recordSleep(&waits), func(e SuperviseEvent) {
		events = append(events, e)
	})
	if got != 1 {
		t.Errorf("Supervise = %d, want 1 (supervisor-level failure)", got)
	}
	if *calls != 1 || len(waits) != 0 {
		t.Errorf("calls = %d, waits = %v; process failure must not retry", *calls, waits)
	}
	if len(events) != 1 || !events[0].Final || events[0].Err == nil {
		t.Errorf("events = %+v, want one final error event", events)
	}
}

func TestSuperviseUnknownCodeFailsClosed(t *testing.T) {
	// Exit 1 from the child means child-side setup failure (bad flags,
	// unreadable session): unprovable, never recovery.
	for _, code := range []int{1, 7, 127, -0} {
		if code == 0 || code == 2 || code == 3 || code == 4 {
			continue
		}
		child, calls := scriptChild(code, ExitOK)
		var waits []time.Duration
		got := Supervise(context.Background(), Budget{MaxRestarts: 5}, child, recordSleep(&waits), nil)
		if got != 1 {
			t.Errorf("child code %d: Supervise = %d, want 1", code, got)
		}
		if *calls != 1 || len(waits) != 0 {
			t.Errorf("child code %d: calls = %d, waits = %v; unknown codes must not retry", code, *calls, waits)
		}
	}
}

func TestSuperviseCancelledWaitStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	child, calls := scriptChild(ExitRetryable, ExitOK)
	sleep := func(ctx context.Context, d time.Duration) {
		cancel() // abandoned wait: supervision is being torn down
	}
	got := Supervise(ctx, Budget{MaxRestarts: 5}, child, sleep, nil)
	if got != ExitNeedsHuman {
		t.Errorf("Supervise = %d, want %d (cancelled wait)", got, ExitNeedsHuman)
	}
	if *calls != 1 {
		t.Errorf("child called %d times, want 1 (no attempt after cancel)", *calls)
	}
}

func TestSuperviseNilGuards(t *testing.T) {
	if got := Supervise(context.Background(), Budget{}, nil, nil, nil); got != 1 {
		t.Errorf("nil child: Supervise = %d, want 1", got)
	}
	// Nil emit and nil sleep are safe when no wait happens.
	child, _ := scriptChild(ExitOK)
	if got := Supervise(context.Background(), Budget{}, child, nil, nil); got != ExitOK {
		t.Errorf("nil emit/sleep: Supervise = %d, want %d", got, ExitOK)
	}
}
