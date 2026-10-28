package runtime

import (
	"context"
	"testing"

	"forcefield/internal/providers"
)

// TestCanRetryTurnInFlight pins the wave-1 hardening: the provider's
// one-request gate fires when a just-cancelled sibling (delegate child)
// is still draining its stream. That overlap is brief, remembered by
// nobody, and happened before anything was emitted, so a clean retry is
// exactly the recovery - not provider weather (never transport-retried),
// not a run failure.
func TestCanRetryTurnInFlight(t *testing.T) {
	ctx := context.Background()

	if !canRetryTurn(0, false, providers.ErrInFlight, ctx) {
		t.Error("clean in-flight failure must retry at the turn level")
	}
	// Emitted output can still never be replayed, in-flight or not.
	if canRetryTurn(0, true, providers.ErrInFlight, ctx) {
		t.Error("in-flight retry must stay clean-only")
	}
	// The general transient rule is unchanged.
	if !canRetryTurn(0, false, context.DeadlineExceeded, ctx) {
		t.Error("deadline-exceeded must stay transient")
	}
	if canRetryTurn(0, false, context.Canceled, cancelled(t)) {
		t.Error("cancelled context must not retry")
	}
	if canRetryTurn(maxTurnRetries, false, providers.ErrInFlight, ctx) {
		t.Error("attempt cap still forbids endless in-flight retries")
	}
}

// cancelled returns an already-cancelled context so the ctx.Err() guard of
// canRetryTurn is exercised on its own, separate from the error argument.
func cancelled(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	t.Cleanup(cancel)
	return ctx
}
