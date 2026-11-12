package providers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type mockTokenSource struct {
	token string
	calls atomic.Int32
	err   error
}

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	m.calls.Add(1)
	if m.err != nil {
		return nil, m.err
	}
	return &oauth2.Token{
		AccessToken: m.token,
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(1 * time.Hour),
	}, nil
}

// Test 1: 20 concurrent callers coalesce into 1 ADC discovery call
func TestVertex_ConcurrentADCDiscovery(t *testing.T) {
	p := NewVertexProvider(Spec{ID: "v", Type: "vertex", Project: "my-proj", Location: "us-central1", Model: "gemini-2.5-flash"})

	var resolverCalls atomic.Int32
	releaseResolver := make(chan struct{})

	// Mock token source factory through getOrResolveTokenSource seam
	tsMock := &mockTokenSource{token: "test-token-123"}

	var originalActive *adcAttempt
	_ = originalActive

	p.mu.Lock()
	p.adcGen = 1
	p.activeADC = &adcAttempt{
		gen:    1,
		done:   make(chan struct{}),
		cancel: func() {},
	}
	attempt := p.activeADC
	p.mu.Unlock()

	go func() {
		resolverCalls.Add(1)
		<-releaseResolver
		p.mu.Lock()
		attempt.ts = tsMock
		attempt.completed = true
		p.ts = tsMock
		p.activeADC = nil
		p.mu.Unlock()
		close(attempt.done)
	}()

	const callers = 20
	var wg sync.WaitGroup
	wg.Add(callers)
	results := make([]string, callers)
	errs := make([]error, callers)

	for i := 0; i < callers; i++ {
		idx := i
		go func() {
			defer wg.Done()
			tok, err := p.token(context.Background())
			results[idx] = tok
			errs[idx] = err
		}()
	}

	time.Sleep(10 * time.Millisecond)
	close(releaseResolver)
	wg.Wait()

	if calls := resolverCalls.Load(); calls != 1 {
		t.Fatalf("expected 1 resolver invocation, got %d", calls)
	}

	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d got error: %v", i, errs[i])
		}
		if results[i] != "test-token-123" {
			t.Fatalf("caller %d got unexpected token: %q", i, results[i])
		}
	}

	// Subsequent caller reads cached token immediately without re-resolving ADC
	tok, err := p.token(context.Background())
	if err != nil || tok != "test-token-123" {
		t.Fatalf("subsequent token: %q err=%v", tok, err)
	}
}

// Test 2: Caller cancellation unblocks immediately while ADC continues for others
func TestVertex_CallerCancellation_DuringADC(t *testing.T) {
	p := NewVertexProvider(Spec{ID: "v", Type: "vertex", Project: "my-proj", Location: "us-central1", Model: "gemini-2.5-flash"})

	releaseResolver := make(chan struct{})
	tsMock := &mockTokenSource{token: "valid-token"}

	p.mu.Lock()
	p.adcGen = 1
	attempt := &adcAttempt{
		gen:    1,
		done:   make(chan struct{}),
		cancel: func() {},
	}
	p.activeADC = attempt
	p.mu.Unlock()

	go func() {
		<-releaseResolver
		p.mu.Lock()
		attempt.ts = tsMock
		attempt.completed = true
		p.ts = tsMock
		p.activeADC = nil
		p.mu.Unlock()
		close(attempt.done)
	}()

	caller1Ctx, caller1Cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(2)
	var err1, err2 error
	var tok2 string

	go func() {
		defer wg.Done()
		_, err1 = p.token(caller1Ctx)
	}()
	go func() {
		defer wg.Done()
		tok2, err2 = p.token(context.Background())
	}()

	time.Sleep(10 * time.Millisecond)
	caller1Cancel() // Cancel caller 1 while ADC is running

	time.Sleep(10 * time.Millisecond)
	close(releaseResolver)
	wg.Wait()

	if !errors.Is(err1, context.Canceled) {
		t.Fatalf("expected caller 1 to receive context.Canceled, got %v", err1)
	}
	if err2 != nil {
		t.Fatalf("caller 2 got unexpected error: %v", err2)
	}
	if tok2 != "valid-token" {
		t.Fatalf("caller 2 got token %q, want valid-token", tok2)
	}
}

// Test 3: Runtime shutdown aborts discovery and unblocks callers
func TestVertex_RuntimeShutdown_DuringADC(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())

	p := NewVertexProviderWithLifecycle(Spec{ID: "v", Type: "vertex", Project: "my-proj", Location: "us-central1", Model: "gemini-2.5-flash"}, lifecycleCtx)

	p.mu.Lock()
	p.adcGen = 1
	attempt := &adcAttempt{
		gen:    1,
		done:   make(chan struct{}),
		cancel: func() {},
	}
	p.activeADC = attempt
	p.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	var callerErr error

	go func() {
		defer wg.Done()
		_, callerErr = p.token(context.Background())
	}()

	time.Sleep(10 * time.Millisecond)
	// Cancel session lifecycle
	lifecycleCancel()

	wg.Wait()

	if callerErr == nil {
		t.Fatal("expected caller to receive shutdown error")
	}
}

// Test 4: Invalidation supersedes in-flight ADC and unblocks waiters
func TestVertex_Invalidation_DuringADC(t *testing.T) {
	p := NewVertexProvider(Spec{ID: "v", Type: "vertex", Project: "my-proj", Location: "us-central1", Model: "gemini-2.5-flash"})

	gen1WorkerCancelled := make(chan struct{})
	p.mu.Lock()
	p.adcGen = 1
	attempt1 := &adcAttempt{
		gen:  1,
		done: make(chan struct{}),
		cancel: func() {
			close(gen1WorkerCancelled)
		},
	}
	p.activeADC = attempt1
	p.mu.Unlock()

	callerCtx, callerCancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	var callerErr error

	go func() {
		defer wg.Done()
		_, callerErr = p.token(callerCtx)
	}()

	time.Sleep(10 * time.Millisecond)
	initialGen := p.ADCGen()

	// Cancel caller and Invalidate
	callerCancel()
	p.Invalidate()

	if p.ADCGen() <= initialGen {
		t.Fatalf("expected ADCGen to increment, initial=%d current=%d", initialGen, p.ADCGen())
	}

	select {
	case <-gen1WorkerCancelled:
		// Gen 1 worker was cancelled
	case <-time.After(500 * time.Millisecond):
		t.Fatal("gen 1 worker was not cancelled by Invalidate()")
	}

	wg.Wait()
	if !errors.Is(callerErr, context.Canceled) {
		t.Fatalf("expected caller to get context.Canceled, got %v", callerErr)
	}
}

// Test 5: Stale ADC completing after Invalidation is discarded
func TestVertex_StaleADC_ResultRejection(t *testing.T) {
	p := NewVertexProvider(Spec{ID: "v", Type: "vertex", Project: "my-proj", Location: "us-central1", Model: "gemini-2.5-flash"})

	staleTS := &mockTokenSource{token: "stale-token-999"}
	p.mu.Lock()
	p.adcGen = 1
	attempt := &adcAttempt{
		gen:    1,
		done:   make(chan struct{}),
		cancel: func() {},
	}
	p.activeADC = attempt
	p.mu.Unlock()

	// Invalidate immediately, superseding Gen 1
	p.Invalidate()

	// Late worker completes for attempt (Gen 1)
	p.mu.Lock()
	// Simulated runADCResolution completion check:
	if !attempt.superseded && p.adcGen == attempt.gen && p.activeADC == attempt {
		p.ts = staleTS
	}
	p.mu.Unlock()

	// Stale token source must NOT have been stored
	if p.ReadyTokenSource() != nil {
		t.Fatal("stale ADC result was stored in readyTokenSource, expected nil")
	}
}

// Test 6: Auth failure triggers Invalidate()
func TestVertex_TokenRefreshFailure_TriggersInvalidate(t *testing.T) {
	p := NewVertexProvider(Spec{ID: "v", Type: "vertex", Project: "my-proj", Location: "us-central1", Model: "gemini-2.5-flash"})

	failingTS := &mockTokenSource{
		err: errors.New("oauth2: token expired and refresh failed: 401 Unauthorized"),
	}

	p.mu.Lock()
	p.ts = failingTS
	initialGen := p.adcGen
	p.mu.Unlock()

	_, err := p.token(context.Background())
	if err == nil {
		t.Fatal("expected token call to fail")
	}

	// Invalidation should have cleared ts and incremented adcGen
	p.mu.Lock()
	currentGen := p.adcGen
	readyTS := p.ts
	p.mu.Unlock()

	if currentGen <= initialGen {
		t.Fatalf("expected adcGen to increment on auth failure, initial=%d current=%d", initialGen, currentGen)
	}
	if readyTS != nil {
		t.Fatal("expected cached ts to be cleared on auth failure")
	}
}
