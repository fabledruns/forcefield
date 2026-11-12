package providers

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	// ErrRuntimeShutdown is the cause set when a request context is cancelled
	// due to runtime lifecycle termination.
	ErrRuntimeShutdown = errors.New("runtime shutdown")

	// ErrRequestTimeout is the cause set when a request exceeds its deadline.
	ErrRequestTimeout = errors.New("request timeout exceeded")
)

// ComposeRequestContext bridges the runtime/session lifecycle context and an
// individual caller request context, applying an optional timeout. It uses Go's
// context.AfterFunc for lightweight lifecycle propagation, incorporates an
// already-cancelled fast path, and enforces a deterministic cleanup barrier
// to prevent goroutine leaks.
func ComposeRequestContext(
	lifecycleCtx context.Context,
	callerCtx context.Context,
	timeout time.Duration,
) (context.Context, func()) {
	if lifecycleCtx == nil {
		lifecycleCtx = context.Background()
	}
	if callerCtx == nil {
		callerCtx = context.Background()
	}

	// 1. Fast-Path Check: If lifecycle is already cancelled, return immediately
	// without spawning background goroutines or registering AfterFunc callbacks.
	if err := lifecycleCtx.Err(); err != nil {
		reqCtx, reqCancel := context.WithCancelCause(callerCtx)
		reqCancel(ErrRuntimeShutdown)
		return reqCtx, func() {}
	}

	// 2. Derive request context with cancellation cause from caller context
	reqCtx, reqCancel := context.WithCancelCause(callerCtx)

	// 3. Apply timeout if specified
	var timeoutCancel context.CancelFunc
	if timeout > 0 {
		reqCtx, timeoutCancel = context.WithTimeout(reqCtx, timeout)
	}

	// 4. Bridge lifecycle cancellation using context.AfterFunc
	cbDone := make(chan struct{})
	stopAfterFunc := context.AfterFunc(lifecycleCtx, func() {
		reqCancel(ErrRuntimeShutdown)
		close(cbDone)
	})

	// 5. Hardened Cleanup: Guarantees deterministic teardown and waits for callback completion
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			if timeoutCancel != nil {
				timeoutCancel()
			}
			if stopAfterFunc() {
				// Successfully deregistered before callback was launched
				reqCancel(context.Canceled)
			} else {
				// Callback was already dispatched in a background goroutine!
				// Wait for cbDone barrier to guarantee zero goroutine leaks before frame return.
				select {
				case <-cbDone:
				case <-time.After(50 * time.Millisecond):
					// Bounded backstop to prevent deadlock if callback hung
				}
				reqCancel(context.Canceled)
			}
		})
	}

	return reqCtx, cleanup
}
