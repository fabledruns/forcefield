package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestTracker_AcceptingAndRegister(t *testing.T) {
	rt := newRequestTracker()
	if !rt.isAccepting() {
		t.Fatalf("expected tracker to be accepting initially")
	}

	dereg, err := rt.registerOperation()
	if err != nil {
		t.Fatalf("expected successful registration, got: %v", err)
	}

	dereg() // should complete cleanly
}

func TestRequestTracker_RejectAfterClosing(t *testing.T) {
	rt := newRequestTracker()

	err := rt.closeWithTimeout(time.Second, nil)
	if err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}

	if rt.isAccepting() {
		t.Fatalf("expected tracker to not be accepting after close")
	}

	_, err = rt.registerOperation()
	if !errors.Is(err, ErrRuntimeClosed) {
		t.Fatalf("expected ErrRuntimeClosed, got: %v", err)
	}
}

func TestRequestTracker_DrainCompletion(t *testing.T) {
	rt := newRequestTracker()

	dereg1, _ := rt.registerOperation()
	dereg2, _ := rt.registerOperation()

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- rt.closeWithTimeout(time.Second, nil)
	}()

	// Simulate work then release
	dereg1()
	dereg2()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("expected clean close without timeout, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for close to drain")
	}
}

func TestRequestTracker_DrainTimeoutDeferredTeardown(t *testing.T) {
	rt := newRequestTracker()

	dereg, err := rt.registerOperation()
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	var teardownCalled atomic.Bool
	teardownFn := func() error {
		teardownCalled.Store(true)
		return nil
	}

	// Close with small timeout (50ms) while operation is still active
	err = rt.closeWithTimeout(50*time.Millisecond, teardownFn)
	if !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("expected ErrShutdownTimeout, got: %v", err)
	}

	// Teardown must NOT have been called while operation is still running!
	if teardownCalled.Load() {
		t.Fatalf("teardown was called prematurely while operation was still active")
	}

	// Now finish the operation
	dereg()

	// Wait for background teardown to execute
	deadline := time.Now().Add(500 * time.Millisecond)
	for !teardownCalled.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for deferred teardown to be called")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRequestTracker_ConcurrentRegisterAndClose(t *testing.T) {
	for i := 0; i < 20; i++ {
		rt := newRequestTracker()
		var wg sync.WaitGroup
		start := make(chan struct{})

		// 10 concurrent registering goroutines
		for j := 0; j < 10; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				dereg, err := rt.registerOperation()
				if err == nil {
					dereg()
				}
			}()
		}

		// 1 closer
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = rt.closeWithTimeout(time.Second, nil)
		}()

		close(start)
		wg.Wait()
	}
}

func TestRequestTracker_IdempotentClose(t *testing.T) {
	rt := newRequestTracker()
	var teardownCount atomic.Int32

	teardownFn := func() error {
		teardownCount.Add(1)
		return nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = rt.closeWithTimeout(time.Second, teardownFn)
		}()
	}
	wg.Wait()

	if count := teardownCount.Load(); count != 1 {
		t.Fatalf("expected teardown to run exactly once, got %d", count)
	}
}

func TestRuntime_LifecycleContext(t *testing.T) {
	rt := &Runtime{}
	ctx := rt.LifecycleContext()
	if ctx == nil {
		t.Fatalf("expected non-nil lifecycle context")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("expected active context, got: %v", err)
	}

	_ = rt.Close()

	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled after Close, got: %v", err)
	}
}
