package runtime

import (
	"context"
	"errors"
	"sync"
	"time"
)

// RuntimeState represents the lifecycle phase of a Runtime's request acceptance.
type RuntimeState int

const (
	// StateAccepting indicates the runtime is active and accepting new requests.
	StateAccepting RuntimeState = iota
	// StateClosing indicates shutdown has commenced; no new requests are accepted.
	StateClosing
	// StateClosed indicates all in-flight requests have drained and subsystems are torn down.
	StateClosed
)

var (
	// ErrRuntimeClosed is returned when attempting an operation on a closing or closed runtime.
	ErrRuntimeClosed = errors.New("runtime is closed")
	// ErrShutdownTimeout is returned when in-flight operations do not drain within the timeout.
	ErrShutdownTimeout = errors.New("runtime shutdown drain timeout exceeded")
)

type requestTracker struct {
	mu              sync.RWMutex
	state           RuntimeState
	activeWg        sync.WaitGroup
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
	closeOnce       sync.Once
	teardownOnce    sync.Once
}

func newRequestTracker() *requestTracker {
	ctx, cancel := context.WithCancel(context.Background())
	return &requestTracker{
		state:           StateAccepting,
		lifecycleCtx:    ctx,
		lifecycleCancel: cancel,
	}
}

// registerOperation attempts to register an active operation.
// If the runtime is not accepting new requests, it returns ErrRuntimeClosed.
// Otherwise, it increments the active waitgroup and returns a deregister function.
func (rt *requestTracker) registerOperation() (func(), error) {
	if rt == nil {
		return func() {}, nil
	}
	rt.mu.RLock()
	if rt.state != StateAccepting {
		rt.mu.RUnlock()
		return nil, ErrRuntimeClosed
	}
	rt.activeWg.Add(1)
	rt.mu.RUnlock()

	var deregOnce sync.Once
	return func() {
		deregOnce.Do(func() {
			rt.activeWg.Done()
		})
	}, nil
}

func (rt *requestTracker) isAccepting() bool {
	if rt == nil {
		return true
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.state == StateAccepting
}

func (rt *requestTracker) lifecycleContext() context.Context {
	if rt == nil {
		return context.Background()
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	if rt.lifecycleCtx != nil {
		return rt.lifecycleCtx
	}
	return context.Background()
}

// closeWithTimeout performs the graceful shutdown and drain sequence.
// 1. Sets state to StateClosing under lock.
// 2. Broadcasts cancellation via lifecycleCancel().
// 3. Waits on activeWg with the specified drainTimeout.
// 4. Invokes teardownFunc synchronously if drained, or defers it to a background goroutine if timed out.
func (rt *requestTracker) closeWithTimeout(drainTimeout time.Duration, teardownFunc func() error) error {
	if rt == nil {
		if teardownFunc != nil {
			return teardownFunc()
		}
		return nil
	}

	var closeErr error
	rt.closeOnce.Do(func() {
		// 1. Atomically transition to StateClosing
		rt.mu.Lock()
		rt.state = StateClosing
		cancel := rt.lifecycleCancel
		rt.mu.Unlock()

		// 2. Broadcast lifecycle context cancellation
		if cancel != nil {
			cancel()
		}

		// 3. Wait on activeWg drain
		drained := make(chan struct{})
		go func() {
			rt.activeWg.Wait()
			close(drained)
		}()

		var drainErr error
		if drainTimeout > 0 {
			select {
			case <-drained:
				// Drained cleanly within deadline
			case <-time.After(drainTimeout):
				drainErr = ErrShutdownTimeout
			}
		} else {
			<-drained
		}

		// 4. Teardown handling:
		// If drained cleanly, execute teardownFunc immediately.
		// If drain timed out, do NOT pull resources out from under running operations!
		// Instead, launch a background watcher to execute teardown once operations eventually complete.
		if drainErr == nil {
			rt.teardownOnce.Do(func() {
				if teardownFunc != nil {
					closeErr = teardownFunc()
				}
				rt.mu.Lock()
				rt.state = StateClosed
				rt.mu.Unlock()
			})
		} else {
			closeErr = drainErr
			go func() {
				<-drained
				rt.teardownOnce.Do(func() {
					if teardownFunc != nil {
						_ = teardownFunc()
					}
					rt.mu.Lock()
					rt.state = StateClosed
					rt.mu.Unlock()
				})
			}()
		}
	})

	return closeErr
}
