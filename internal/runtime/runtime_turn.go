package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"forcefield/internal/providers"
	"forcefield/internal/redact"
	"forcefield/internal/tools"
)

// maxTurnBytes caps one model turn (8 MiB); tripping it fails
// non-transient. See docs/Runtime.md.
const maxTurnBytes = 8 << 20

// streamEventBytes estimates one stream event's contribution to the
// turn accumulator. Exact for text/thinking; tool-call arguments are
// provider-decoded JSON (maps/slices/scalars), sized structurally.
// This is a safety bound, not accounting: overcounting only trips
// earlier on absurd turns.
func streamEventBytes(event providers.StreamEvent) int {
	n := len(event.Text) + len(event.Thinking)
	for _, tc := range event.ToolCalls {
		n += len(tc.ID) + len(tc.Name) + argBytes(tc.Arguments)
	}
	return n
}

func argBytes(args map[string]any) int {
	n := 0
	for k, v := range args {
		n += len(k) + valueBytes(v)
	}
	return n
}

func valueBytes(v any) int {
	switch t := v.(type) {
	case string:
		return len(t)
	case []byte:
		return len(t)
	case map[string]any:
		return argBytes(t)
	case []any:
		n := 0
		for _, e := range t {
			n += valueBytes(e)
		}
		return n
	default:
		return 32
	}
}

// maxTurnRetries bounds turn-level provider retries on top of the first
// attempt: a clean transient failure (nothing emitted yet) is retried at
// most twice more, so a sustained outage fails after 3 attempts instead
// of stalling the run.
const maxTurnRetries = 2

// transientError marks a model-turn failure the runtime classifies as
// transient: a later turn (automatic or manual) may succeed. It preserves
// the wrapped chain so errors.Is/As callers keep working.
type transientError struct{ err error }

func (e *transientError) Error() string {
	return e.err.Error() + " (transient provider failure; retrying may succeed)"
}

func (e *transientError) Unwrap() error { return e.err }

// IsTransientError reports whether the runtime marked err as a transient
// provider failure.
func IsTransientError(err error) bool {
	var te *transientError
	return errors.As(err, &te)
}

// markTransient wraps err when the provider classifies it transient;
// anything else (auth, quota, invalid requests, cancellations) passes
// through unchanged.
func markTransient(err error) error {
	if err == nil || !providers.IsTransient(err) {
		return err
	}
	return &transientError{err: err}
}

// canRetryTurn reports whether a failed model turn may be retried
// without risking duplicate output: only a clean failure (no text,
// thinking, or tool calls emitted yet) that is transient, with attempts
// remaining and a live context. Anything already emitted must never be
// replayed — the TUI already showed it and the session may reference it.
func canRetryTurn(attempt int, emitted bool, err error, ctx context.Context) bool {
	if attempt >= maxTurnRetries {
		return false
	}
	if emitted {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	return providers.IsTransient(err)
}

// runModelTurn streams and assembles one provider response, retrying
// clean transient failures with bounded backoff. Retries never replay
// output and never reach tool execution (the scheduler runs only after a
// turn succeeds), so a retried request cannot duplicate tool calls or
// persist inconsistent state.
func (r *Runtime) runModelTurn(ctx context.Context, messages []providers.Message, emit func(Event) bool, snap runSnapshot) (providers.Response, error) {
	if err := checkAuthWithSnapshot(snap.authRequired, snap.authEnvVar, snap.providerName); err != nil {
		return providers.Response{}, err
	}

	r.applyReasoningTo(snap.provider, snap.providerName, snap.modelName)
	if snap.provider == nil {
		return providers.Response{}, fmt.Errorf("model provider not initialized")
	}
	var defs []tools.Definition
	if snap.manager != nil && toolCallingAllowed(snap) {
		defs = snap.manager.Definitions()
	}

	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return providers.Response{}, err
		}
		if !emit(Event{Type: EventThinking}) {
			return providers.Response{}, context.Canceled
		}
		resp, emitted, err := r.streamOneTurn(ctx, messages, emit, snap, defs)
		if err == nil {
			return resp, nil
		}
		if !canRetryTurn(attempt, emitted, err, ctx) {
			return providers.Response{}, markTransient(err)
		}
		delay := providers.TurnRetryDelay(attempt, err)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return providers.Response{}, ctx.Err()
		}
	}
}

// streamOneTurn streams and assembles a single provider attempt. It
// reports whether anything user-visible (thinking, text, or tool calls)
// was emitted, so the caller can decide whether a retry is safe.
func (r *Runtime) streamOneTurn(ctx context.Context, messages []providers.Message, emit func(Event) bool, snap runSnapshot, defs []tools.Definition) (providers.Response, bool, error) {
	stream, err := snap.provider.StreamChat(ctx, messages, defs)
	if err != nil {
		// Provider errors can echo request or response fragments;
		// scrub before the error is surfaced, logged, or persisted.
		// The chain is preserved for errors.Is/As callers.
		return providers.Response{}, false, redact.ScrubError(fmt.Errorf("model call failed: %w", err))
	}

	var response providers.Response
	var content strings.Builder
	emitted := false
	sawDone := false
	turnBytes := 0
	// Select on ctx alongside the provider channel so a hung provider
	// that never closes its stream cannot wedge the run forever: on
	// cancellation the turn ends promptly, and the provider's own
	// goroutine exits via its context checks (all built-in adapters
	// observe ctx on send and on read).
	for {
		select {
		case <-ctx.Done():
			return providers.Response{}, emitted, ctx.Err()
		case event, ok := <-stream:
			if !ok {
				if err := ctx.Err(); err != nil {
					return providers.Response{}, emitted, err
				}
				// A stream that closes without a terminal Done marker is
				// incomplete, not success. Reporting it as a valid turn
				// would confuse partial output with completion.
				if !sawDone {
					return providers.Response{}, emitted, fmt.Errorf("model stream ended without a terminal marker (incomplete response)")
				}
				response.Content = content.String()
				return response, emitted, nil
			}
			if event.Done {
				sawDone = true
			}
			if event.Err != nil {
				return providers.Response{}, emitted, redact.ScrubError(fmt.Errorf("model stream failed: %w", event.Err))
			}

			turnBytes += streamEventBytes(event)
			if turnBytes > maxTurnBytes {
				return providers.Response{}, emitted, fmt.Errorf("model turn exceeded %d bytes without terminating (runaway stream)", maxTurnBytes)
			}

			if event.Thinking != "" {
				emitted = true
				if !emit(Event{Type: EventThinking, Thinking: event.Thinking}) {
					return providers.Response{}, emitted, context.Canceled
				}
			}

			if event.Text != "" {
				emitted = true
				// Accumulate in a Builder (amortized append) instead of
				// += on the result string (realloc+copy per chunk).
				content.WriteString(event.Text)
				if !emit(Event{Type: EventText, Text: event.Text}) {
					return providers.Response{}, emitted, context.Canceled
				}
			}

			if len(event.ToolCalls) > 0 {
				emitted = true
			}
			response.ToolCalls = append(response.ToolCalls, event.ToolCalls...)
			if event.Usage != nil {
				response.Usage = *event.Usage
			}
			if event.StopReason != providers.FinishNone {
				response.StopReason = event.StopReason
			}
		}
	}
}
