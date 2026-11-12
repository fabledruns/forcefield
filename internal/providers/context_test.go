package providers

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

// T41: Pending Zero Goroutines
func TestComposeRequestContext_PendingZeroGoroutines(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	defer lifecycleCancel()
	callerCtx := context.Background()

	runtime.GC()
	baseGoroutines := runtime.NumGoroutine()

	reqCtx, cleanup := ComposeRequestContext(lifecycleCtx, callerCtx, 0)
	defer cleanup()

	// While pending, AfterFunc does not run a dedicated monitoring goroutine.
	currentGoroutines := runtime.NumGoroutine()
	if currentGoroutines > baseGoroutines+2 {
		t.Errorf("expected minimal/zero new goroutines while pending, got base=%d current=%d", baseGoroutines, currentGoroutines)
	}

	if reqCtx.Err() != nil {
		t.Fatalf("expected active context, got %v", reqCtx.Err())
	}
}

// T42: Cancel Spawns Goroutine
func TestComposeRequestContext_CancelSpawnsGoroutine(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	callerCtx := context.Background()

	reqCtx, cleanup := ComposeRequestContext(lifecycleCtx, callerCtx, 0)
	defer cleanup()

	lifecycleCancel()

	select {
	case <-reqCtx.Done():
		if !errors.Is(context.Cause(reqCtx), ErrRuntimeShutdown) {
			t.Fatalf("expected cause %v, got %v", ErrRuntimeShutdown, context.Cause(reqCtx))
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for reqCtx to be cancelled by lifecycle")
	}
}

// T43: Already-Cancelled Fast Path
func TestComposeRequestContext_AlreadyCancelledFastPath(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	lifecycleCancel() // Pre-cancelled
	callerCtx := context.Background()

	reqCtx, cleanup := ComposeRequestContext(lifecycleCtx, callerCtx, 0)
	defer cleanup()

	if reqCtx.Err() == nil {
		t.Fatal("expected reqCtx to be immediately cancelled on pre-cancelled lifecycle")
	}
	if !errors.Is(context.Cause(reqCtx), ErrRuntimeShutdown) {
		t.Fatalf("expected cause %v, got %v", ErrRuntimeShutdown, context.Cause(reqCtx))
	}
}

// T44: Stop Prevents Run
func TestComposeRequestContext_StopPreventsRun(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	defer lifecycleCancel()
	callerCtx := context.Background()

	reqCtx, cleanup := ComposeRequestContext(lifecycleCtx, callerCtx, 0)
	cleanup() // Clean up before lifecycle cancel

	// Now cancel lifecycle
	lifecycleCancel()

	// Cause should be context.Canceled (from cleanup), NOT ErrRuntimeShutdown
	if errors.Is(context.Cause(reqCtx), ErrRuntimeShutdown) {
		t.Fatal("did not expect ErrRuntimeShutdown cause after cleanup was already called")
	}
}

// T45: Non-Waiting Stop Behavior
func TestComposeRequestContext_NonWaitingStopBehavior(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	defer lifecycleCancel()

	var mu sync.Mutex
	callbackRan := false
	stop := context.AfterFunc(lifecycleCtx, func() {
		mu.Lock()
		callbackRan = true
		mu.Unlock()
	})

	stopped := stop()
	if !stopped {
		t.Fatal("expected stop() to return true when lifecycle is still active")
	}

	lifecycleCancel()

	mu.Lock()
	ran := callbackRan
	mu.Unlock()
	if ran {
		t.Fatal("callback should not have run after successful stop()")
	}
}

// T46: Lifecycle Cancellation Propagation
func TestComposeRequestContext_LifecyclePropagation(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	callerCtx := context.Background()

	reqCtx, cleanup := ComposeRequestContext(lifecycleCtx, callerCtx, 0)
	defer cleanup()

	lifecycleCancel()

	select {
	case <-reqCtx.Done():
		if !errors.Is(context.Cause(reqCtx), ErrRuntimeShutdown) {
			t.Fatalf("expected cause %v, got %v", ErrRuntimeShutdown, context.Cause(reqCtx))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for reqCtx cancellation")
	}
}

// T47: Caller Cancellation Propagation
func TestComposeRequestContext_CallerCancelPropagation(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	defer lifecycleCancel()
	callerCtx, callerCancel := context.WithCancel(context.Background())

	reqCtx, cleanup := ComposeRequestContext(lifecycleCtx, callerCtx, 0)
	defer cleanup()

	callerCancel()

	select {
	case <-reqCtx.Done():
		if reqCtx.Err() != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", reqCtx.Err())
		}
		// Cause should NOT be ErrRuntimeShutdown
		if errors.Is(context.Cause(reqCtx), ErrRuntimeShutdown) {
			t.Fatal("caller cancel should not produce ErrRuntimeShutdown cause")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for caller cancellation propagation")
	}
}

// T48: Timeout Expiration
func TestComposeRequestContext_TimeoutExpiration(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	defer lifecycleCancel()
	callerCtx := context.Background()

	reqCtx, cleanup := ComposeRequestContext(lifecycleCtx, callerCtx, 20*time.Millisecond)
	defer cleanup()

	select {
	case <-reqCtx.Done():
		if reqCtx.Err() != context.DeadlineExceeded {
			t.Fatalf("expected DeadlineExceeded, got %v", reqCtx.Err())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for timeout expiration")
	}
}

// T49: Cleanup Barrier Coordinates Concurrent Cancellation
func TestComposeRequestContext_CleanupBarrier(t *testing.T) {
	for i := 0; i < 20; i++ {
		lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
		callerCtx := context.Background()

		reqCtx, cleanup := ComposeRequestContext(lifecycleCtx, callerCtx, 0)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			lifecycleCancel()
		}()
		go func() {
			defer wg.Done()
			cleanup()
		}()
		wg.Wait()

		if reqCtx.Err() == nil {
			t.Fatal("expected reqCtx to be cancelled")
		}
	}
}

// T50: Idempotent Cleanup
func TestComposeRequestContext_IdempotentCleanup(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	defer lifecycleCancel()
	callerCtx := context.Background()

	_, cleanup := ComposeRequestContext(lifecycleCtx, callerCtx, 0)

	// Multiple calls should not panic
	cleanup()
	cleanup()
	cleanup()
}
