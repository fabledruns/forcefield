package providers

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

type mockBedrockClient struct {
	id int
}

func (m *mockBedrockClient) Converse(ctx context.Context, params *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	text := "ok"
	role := brtypes.ConversationRoleAssistant
	return &bedrockruntime.ConverseOutput{
		Output: &brtypes.ConverseOutputMemberMessage{
			Value: brtypes.Message{
				Role: role,
				Content: []brtypes.ContentBlock{
					&brtypes.ContentBlockMemberText{Value: text},
				},
			},
		},
		StopReason: brtypes.StopReasonEndTurn,
	}, nil
}

func (m *mockBedrockClient) ConverseStream(ctx context.Context, params *bedrockruntime.ConverseStreamInput, optFns ...func(*bedrockruntime.Options)) (bedrockStream, error) {
	return nil, errors.New("not implemented")
}

// T01 / Test 1: 50 concurrent callers coalesce into 1 initialization attempt
func TestBedrock_ConcurrentTurns_CoalesceAttempt(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	defer lifecycleCancel()

	p := NewBedrockProviderWithLifecycle(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"}, lifecycleCtx)

	var factoryCalls atomic.Int32
	factoryRelease := make(chan struct{})

	p.newClientWithHTTP = func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
		factoryCalls.Add(1)
		<-factoryRelease
		return &mockBedrockClient{id: 42}, nil
	}

	const callers = 50
	var wg sync.WaitGroup
	wg.Add(callers)
	results := make([]bedrockConverseClient, callers)
	errs := make([]error, callers)

	for i := 0; i < callers; i++ {
		idx := i
		go func() {
			defer wg.Done()
			client, err := p.getCache().getOrInitClient(context.Background(), p)
			results[idx] = client
			errs[idx] = err
		}()
	}

	// Wait for factory to be entered
	for factoryCalls.Load() == 0 {
		time.Sleep(2 * time.Millisecond)
	}

	// Release factory
	close(factoryRelease)
	wg.Wait()

	if calls := factoryCalls.Load(); calls != 1 {
		t.Fatalf("expected factory called exactly 1 time, got %d", calls)
	}

	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d got error: %v", i, errs[i])
		}
		if results[i] == nil {
			t.Fatalf("caller %d got nil client", i)
		}
		if results[i] != results[0] {
			t.Fatalf("caller %d got different client instance", i)
		}
	}

	// Subsequent call returns cached client immediately without calling factory
	subsequent, err := p.getCache().getOrInitClient(context.Background(), p)
	if err != nil {
		t.Fatalf("subsequent call got error: %v", err)
	}
	if subsequent != results[0] {
		t.Fatal("subsequent call did not return cached client")
	}
	if calls := factoryCalls.Load(); calls != 1 {
		t.Fatalf("expected factory calls to remain 1, got %d", calls)
	}
}

// Test 2: Caller cancellation does not poison shared initialization for other callers
func TestBedrock_CallerCancellation_DoesNotPoisonSharedInit(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	defer lifecycleCancel()

	p := NewBedrockProviderWithLifecycle(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"}, lifecycleCtx)

	factoryRelease := make(chan struct{})
	p.newClientWithHTTP = func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
		<-factoryRelease
		return &mockBedrockClient{id: 1}, nil
	}

	caller1Ctx, caller1Cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(3)

	var err1, err2, err3 error
	var client2, client3 bedrockConverseClient

	go func() {
		defer wg.Done()
		_, err1 = p.getCache().getOrInitClient(caller1Ctx, p)
	}()
	go func() {
		defer wg.Done()
		client2, err2 = p.getCache().getOrInitClient(context.Background(), p)
	}()
	go func() {
		defer wg.Done()
		client3, err3 = p.getCache().getOrInitClient(context.Background(), p)
	}()

	time.Sleep(10 * time.Millisecond)

	// Cancel caller 1 while initialization is in-flight
	caller1Cancel()

	// Wait a moment, then release factory
	time.Sleep(10 * time.Millisecond)
	close(factoryRelease)
	wg.Wait()

	if !errors.Is(err1, context.Canceled) {
		t.Fatalf("expected caller 1 to receive context.Canceled, got %v", err1)
	}
	if err2 != nil {
		t.Fatalf("caller 2 got unexpected error: %v", err2)
	}
	if err3 != nil {
		t.Fatalf("caller 3 got unexpected error: %v", err3)
	}
	if client2 == nil || client3 == nil || client2 != client3 {
		t.Fatal("callers 2 and 3 should have received the same non-nil cached client")
	}
}

// Test 3: Runtime shutdown cancels in-flight worker
func TestBedrock_RuntimeShutdown_CancelsWorker(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())

	p := NewBedrockProviderWithLifecycle(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"}, lifecycleCtx)

	workerCancelled := make(chan struct{})
	p.newClientWithHTTP = func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
		<-ctx.Done()
		close(workerCancelled)
		return nil, ctx.Err()
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var callerErr error
	go func() {
		defer wg.Done()
		_, callerErr = p.getCache().getOrInitClient(context.Background(), p)
	}()

	time.Sleep(15 * time.Millisecond)
	// Cancel lifecycle
	lifecycleCancel()

	wg.Wait()

	select {
	case <-workerCancelled:
		// Worker observed context cancellation
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker did not observe cancellation")
	}

	if callerErr == nil {
		t.Fatal("expected caller to receive cancellation error")
	}
}

// Test 4: Invalidation cancels in-flight worker and increments generation
func TestBedrock_Invalidation_CancelsWorker(t *testing.T) {
	p := NewBedrockProvider(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"})

	gen1Started := make(chan struct{})
	gen1Cancelled := make(chan struct{})

	var genCalls atomic.Int32
	p.newClientWithHTTP = func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
		call := genCalls.Add(1)
		if call == 1 {
			close(gen1Started)
			<-ctx.Done()
			close(gen1Cancelled)
			return nil, ctx.Err()
		}
		return &mockBedrockClient{id: int(call)}, nil
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Caller should loop and receive gen 2 client
		client, err := p.getCache().getOrInitClient(context.Background(), p)
		if err != nil {
			t.Errorf("caller got error: %v", err)
		}
		if client == nil {
			t.Errorf("caller got nil client")
		}
	}()

	<-gen1Started
	initialGen := p.Generation()

	// Invalidate while gen 1 is running
	p.Invalidate()

	if p.Generation() <= initialGen {
		t.Fatalf("expected generation to increment, got initial=%d current=%d", initialGen, p.Generation())
	}

	select {
	case <-gen1Cancelled:
		// Gen 1 worker context was cancelled
	case <-time.After(500 * time.Millisecond):
		t.Fatal("gen 1 worker context was not cancelled by Invalidate()")
	}

	wg.Wait()
}

// Test 5: Stale worker completing after Invalidation is discarded
func TestBedrock_OldWorker_CompletingAfterInvalidation_Discarded(t *testing.T) {
	p := NewBedrockProvider(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"})

	callerCtx, callerCancel := context.WithCancel(context.Background())
	gen1WorkerDone := make(chan struct{})
	p.newClientWithHTTP = func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
		// Worker ignores ctx.Done() and returns client late
		time.Sleep(30 * time.Millisecond)
		close(gen1WorkerDone)
		return &mockBedrockClient{id: 999}, nil
	}

	go func() {
		_, _ = p.getCache().getOrInitClient(callerCtx, p)
	}()

	time.Sleep(10 * time.Millisecond)
	// Cancel caller context and Invalidate
	callerCancel()
	p.Invalidate()

	<-gen1WorkerDone
	time.Sleep(10 * time.Millisecond)

	// Ready client must NOT be the stale worker's result (id 999)
	if p.ReadyClient() != nil {
		t.Fatal("stale worker result should have been discarded, but readyClient is non-nil")
	}
}

// Test 6: Preserving AWS SDK Redirect Security and Transport Retention
func TestBedrock_RedirectSecurity_TunedBuildableClient(t *testing.T) {
	p := NewBedrockProvider(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"})

	var capturedClient aws.HTTPClient
	p.newClientWithHTTP = func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
		capturedClient = httpClient
		return &mockBedrockClient{id: 100}, nil
	}

	_, err := p.getCache().getOrInitClient(context.Background(), p)
	if err != nil {
		t.Fatalf("failed to init client: %v", err)
	}

	// Verify that capturedClient is an *awshttp.BuildableClient
	buildable, ok := capturedClient.(*awshttp.BuildableClient)
	if !ok {
		t.Fatalf("expected capturedClient to be *awshttp.BuildableClient, got %T", capturedClient)
	}

	// Verify that transport handle was retained for deterministic cleanup
	tr := p.RetainedTransport()
	if tr == nil {
		t.Fatal("expected retainedTransport to be non-nil")
	}
	if tr.IdleConnTimeout != 30*time.Second {
		t.Errorf("expected IdleConnTimeout=30s, got %v", tr.IdleConnTimeout)
	}

	_ = buildable
	// Verify that Close() cleans up without panicking
	if err := p.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if p.ReadyClient() != nil {
		t.Fatal("expected readyClient to be nil after Close()")
	}
}

// Test 7: Secret Safety - AuthSnapshot captures zero secret keys or tokens
func TestBedrock_AuthSnapshot_ZeroSecrets(t *testing.T) {
	// Set dummy env
	os.Setenv("AWS_ACCESS_KEY_ID", "AKIAIOSFODNN7EXAMPLE")
	os.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	os.Setenv("AWS_SESSION_TOKEN", "AQoDYXdzEJr111111111111111111111111")
	defer func() {
		os.Unsetenv("AWS_ACCESS_KEY_ID")
		os.Unsetenv("AWS_SECRET_ACCESS_KEY")
		os.Unsetenv("AWS_SESSION_TOKEN")
	}()

	spec := Spec{ID: "b", Type: "bedrock", Region: "us-west-2", Profile: "default"}
	snap := currentAuthSnapshot(spec, 1)

	if snap.AccessKeyID != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("got AccessKeyID %q, want AKIAIOSFODNN7EXAMPLE", snap.AccessKeyID)
	}
	if snap.Region != "us-west-2" {
		t.Errorf("got Region %q, want us-west-2", snap.Region)
	}
	if snap.Profile != "default" {
		t.Errorf("got Profile %q, want default", snap.Profile)
	}
	if snap.ConfigGen != 1 {
		t.Errorf("got ConfigGen %d, want 1", snap.ConfigGen)
	}
}

// Test 8: Auth failure latching and dual-track recovery
func TestBedrock_DualTrackRecovery_LatchAndReload(t *testing.T) {
	p := NewBedrockProvider(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"})

	var initCount atomic.Int32
	authErr := &smithyAPIError{code: "UnrecognizedClientException", message: "The security token included in the request is invalid"}

	p.newClientWithHTTP = func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
		count := initCount.Add(1)
		if count == 1 {
			return nil, authErr
		}
		return &mockBedrockClient{id: int(count)}, nil
	}

	// Turn 1 fails with auth error
	_, err := p.getCache().getOrInitClient(context.Background(), p)
	if err == nil {
		t.Fatal("expected turn 1 to fail with auth error")
	}

	// Turn 2: snapshot unchanged -> latched! Fails fast without invoking factory
	_, err = p.getCache().getOrInitClient(context.Background(), p)
	if err == nil {
		t.Fatal("expected turn 2 to fail fast from latch")
	}
	if initCount.Load() != 1 {
		t.Fatalf("expected factory not to be invoked while latched, got count=%d", initCount.Load())
	}

	// Track 2: Explicit Reload (/provider reload) resets latch
	p.Reload()

	// Turn 3: Should proceed with fresh factory invocation
	client, err := p.getCache().getOrInitClient(context.Background(), p)
	if err != nil {
		t.Fatalf("turn 3 after reload failed: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client on turn 3")
	}
	if initCount.Load() != 2 {
		t.Fatalf("expected factory count=2 after reload, got %d", initCount.Load())
	}
}

// Test 9: Subagent cloning via CloneForRun shares cache pointer safely
func TestBedrock_Subagent_RunscopeCloneSharesCache(t *testing.T) {
	p := NewBedrockProvider(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"})

	var initCount atomic.Int32
	p.newClientWithHTTP = func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
		initCount.Add(1)
		return &mockBedrockClient{id: 777}, nil
	}

	// Warm parent
	client1, err := p.getCache().getOrInitClient(context.Background(), p)
	if err != nil || client1 == nil {
		t.Fatalf("failed to init parent client: %v", err)
	}

	// Clone for subagent
	cloned, err := p.CloneForRun()
	if err != nil {
		t.Fatalf("CloneForRun failed: %v", err)
	}
	bedrockClone, ok := cloned.(*BedrockProvider)
	if !ok {
		t.Fatalf("expected *BedrockProvider, got %T", cloned)
	}

	// Subagent should read already-cached client without re-initializing
	client2, err := bedrockClone.getCache().getOrInitClient(context.Background(), bedrockClone)
	if err != nil || client2 == nil {
		t.Fatalf("failed to init clone client: %v", err)
	}

	if client1 != client2 {
		t.Fatal("parent and cloned subagent did not share cached client")
	}
	if initCount.Load() != 1 {
		t.Fatalf("expected factory called exactly once across parent and subagent, got %d", initCount.Load())
	}
}

// Test 10: Selective Invalidation - Model access denied does NOT invalidate client
func TestBedrock_SelectiveInvalidation_AccessDeniedRetainsClient(t *testing.T) {
	p := NewBedrockProvider(Spec{ID: "b", Type: "bedrock", Region: "us-east-1", Model: "m"})

	p.newClientWithHTTP = func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
		return &mockBedrockClient{id: 888}, nil
	}

	// Warm client
	_, err := p.getCache().getOrInitClient(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if p.ReadyClient() == nil {
		t.Fatal("expected readyClient non-nil")
	}

	// Simulate AccessDeniedException (e.g. missing model access)
	accessDeniedErr := &smithyAPIError{code: "AccessDeniedException", message: "You don't have access to the model"}
	p.handleSelectiveInvalidation(accessDeniedErr)

	// Client must REMAIN cached
	if p.ReadyClient() == nil {
		t.Fatal("AccessDeniedException should not invalidate cached client")
	}

	// Simulate UnrecognizedClientException
	unrecogErr := &smithyAPIError{code: "UnrecognizedClientException", message: "Invalid credentials"}
	p.handleSelectiveInvalidation(unrecogErr)

	// Now client MUST be invalidated
	if p.ReadyClient() != nil {
		t.Fatal("UnrecognizedClientException should invalidate cached client")
	}
}
