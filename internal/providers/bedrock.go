package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"forcefield/internal/tools"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtdoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithy "github.com/aws/smithy-go"
)

// bedrockStream is the event source for one ConverseStream call: the
// SDK's *bedrockruntime.ConverseStreamEventStream satisfies it, and
// tests inject fakes (the SDK documents Reader as the mock point).
type bedrockStream interface {
	Events() <-chan brtypes.ConverseStreamOutput
	Close() error
	Err() error
}

// bedrockConverseClient is the Converse/ConverseStream surface the adapter
// needs. Tests inject fakes; production wraps the SDK client.
type bedrockConverseClient interface {
	Converse(ctx context.Context, params *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
	ConverseStream(ctx context.Context, params *bedrockruntime.ConverseStreamInput, optFns ...func(*bedrockruntime.Options)) (bedrockStream, error)
}

// sdkBedrockClient adapts the real SDK client to bedrockConverseClient.
type sdkBedrockClient struct {
	client *bedrockruntime.Client
}

func (s sdkBedrockClient) Converse(ctx context.Context, params *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	return s.client.Converse(ctx, params, optFns...)
}

func (s sdkBedrockClient) ConverseStream(ctx context.Context, params *bedrockruntime.ConverseStreamInput, optFns ...func(*bedrockruntime.Options)) (bedrockStream, error) {
	out, err := s.client.ConverseStream(ctx, params, optFns...)
	if err != nil {
		return nil, err
	}
	return out.GetStream(), nil
}

// ErrAttemptSuperseded indicates an in-flight client initialization attempt was
// superseded by an explicit Invalidate() call.
var ErrAttemptSuperseded = errors.New("bedrock client initialization superseded by invalidation")

type initAttempt struct {
	gen        uint64
	done       chan struct{}
	cancel     context.CancelFunc
	client     bedrockConverseClient
	tr         *http.Transport
	err        error
	superseded bool
	completed  bool
}

// AuthSnapshot captures non-secret, observable metadata about the active AWS
// authentication environment to support secret-safe automatic recovery.
type AuthSnapshot struct {
	AccessKeyID string
	Profile     string
	Region      string
	CredsMTime  time.Time
	ConfigMTime time.Time
	SSOMTime    time.Time
	ConfigGen   uint64
}

// Equal returns true if two AuthSnapshot values have matching metadata.
func (s AuthSnapshot) Equal(other AuthSnapshot) bool {
	return s.AccessKeyID == other.AccessKeyID &&
		s.Profile == other.Profile &&
		s.Region == other.Region &&
		s.ConfigGen == other.ConfigGen &&
		s.CredsMTime.Equal(other.CredsMTime) &&
		s.ConfigMTime.Equal(other.ConfigMTime) &&
		s.SSOMTime.Equal(other.SSOMTime)
}

type authFailedLatch struct {
	latched  bool
	err      error
	snapshot AuthSnapshot
}

func (l *authFailedLatch) IsLatched(current AuthSnapshot) bool {
	if l == nil || !l.latched {
		return false
	}
	// If snapshot changed, automatically unlatch!
	if !l.snapshot.Equal(current) {
		l.latched = false
		l.err = nil
		return false
	}
	return true
}

func (l *authFailedLatch) Latch(err error, snap AuthSnapshot) {
	if l == nil {
		return
	}
	l.latched = true
	l.err = err
	l.snapshot = snap
}

func (l *authFailedLatch) Reset() {
	if l == nil {
		return
	}
	l.latched = false
	l.err = nil
	l.snapshot = AuthSnapshot{}
}

func (l *authFailedLatch) Err() error {
	if l == nil {
		return nil
	}
	return l.err
}

type bedrockClientCache struct {
	mu              sync.Mutex
	lifecycleCtx    context.Context
	generation      uint64
	active          *initAttempt
	readyClient     bedrockConverseClient
	tr              *http.Transport
	authLatch       *authFailedLatch
	configGen       uint64
	onAttemptJoined func(gen uint64) // Test hook
}

func newBedrockClientCache(lifecycleCtx context.Context) *bedrockClientCache {
	if lifecycleCtx == nil {
		lifecycleCtx = context.Background()
	}
	return &bedrockClientCache{
		lifecycleCtx: lifecycleCtx,
	}
}

// BedrockProvider talks to Amazon Bedrock via the Converse API using the
// maintained AWS SDK for Go v2. A dedicated adapter is warranted: request
// signing (SigV4), the standard AWS credential chain, Converse message
// shapes, and the ConverseStream event protocol differ from every other
// transport Forcefield speaks.
//
// Retry ownership is explicit: the SDK retryer is disabled
// (aws.NopRetryer, single attempt) and Forcefield's own bounded policy in
// this file decides what retries. Multiplying SDK retries with transport
// retries would turn one model turn into an unbounded, potentially
// billable burst.
type BedrockProvider struct {
	spec  Spec
	retry retryPolicy
	gate  *requestGate
	cache *bedrockClientCache

	// newClient builds the SDK client. It is a seam so tests run without
	// AWS credentials or network; production builds from the spec.
	newClient         func(ctx context.Context, spec Spec) (bedrockConverseClient, error)
	newClientWithHTTP func(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error)
}

// NewBedrockProvider builds a provider from a resolved spec. Construction
// is total and touches neither credentials nor network: the SDK client is
// built lazily per request so capability probes and picker listings work
// without AWS configuration.
func NewBedrockProvider(spec Spec) *BedrockProvider {
	return NewBedrockProviderWithLifecycle(spec, context.Background())
}

// NewBedrockProviderWithLifecycle builds a provider bound to a session lifecycle context.
func NewBedrockProviderWithLifecycle(spec Spec, lifecycleCtx context.Context) *BedrockProvider {
	if lifecycleCtx == nil {
		lifecycleCtx = context.Background()
	}
	return &BedrockProvider{
		spec:      spec,
		retry:     defaultRetryPolicy,
		gate:      newRequestGate(),
		cache:     newBedrockClientCache(lifecycleCtx),
		newClient: defaultBedrockClient,
	}
}

func (b *BedrockProvider) getCache() *bedrockClientCache {
	if b == nil {
		return nil
	}
	if b.cache == nil {
		b.cache = newBedrockClientCache(context.Background())
	}
	return b.cache
}

// SetLifecycleContext configures the session lifecycle context for the client cache.
func (b *BedrockProvider) SetLifecycleContext(ctx context.Context) {
	if b == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c := b.getCache()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lifecycleCtx = ctx
}

// SetOnAttemptJoined configures an internal synchronization hook for tests.
func (b *BedrockProvider) SetOnAttemptJoined(hook func(gen uint64)) {
	if b == nil {
		return
	}
	c := b.getCache()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onAttemptJoined = hook
}

// Generation returns the current generation counter of the cache (for testing).
func (b *BedrockProvider) Generation() uint64 {
	if b == nil || b.cache == nil {
		return 0
	}
	b.cache.mu.Lock()
	defer b.cache.mu.Unlock()
	return b.cache.generation
}

// ReadyClient returns the currently cached client (for testing).
func (b *BedrockProvider) ReadyClient() bedrockConverseClient {
	if b == nil || b.cache == nil {
		return nil
	}
	b.cache.mu.Lock()
	defer b.cache.mu.Unlock()
	return b.cache.readyClient
}

// RetainedTransport returns the retained HTTP transport handle (for testing).
func (b *BedrockProvider) RetainedTransport() *http.Transport {
	if b == nil || b.cache == nil {
		return nil
	}
	b.cache.mu.Lock()
	defer b.cache.mu.Unlock()
	return b.cache.tr
}

// Invalidate invalidates the cached client and closes idle connections.
func (b *BedrockProvider) Invalidate() {
	if b == nil {
		return
	}
	b.getCache().Invalidate()
}

// Close closes the provider, invalidating cached state and closing idle transport connections.
func (b *BedrockProvider) Close() error {
	if b == nil {
		return nil
	}
	b.Invalidate()
	return nil
}

// Reload triggers an explicit configuration reload, incrementing generation and resetting the auth latch.
func (b *BedrockProvider) Reload() {
	if b == nil {
		return
	}
	c := b.getCache()
	c.mu.Lock()
	c.configGen++
	if c.authLatch != nil {
		c.authLatch.Reset()
	}
	c.mu.Unlock()
	c.Invalidate()
}

func (c *bedrockClientCache) Invalidate() {
	c.mu.Lock()
	c.generation++      // Immediately supersedes any in-flight attempt
	c.readyClient = nil // Clears cached client
	oldTr := c.tr
	c.tr = nil

	var attemptToCancel context.CancelFunc
	if c.active != nil {
		attempt := c.active
		c.active = nil
		attempt.superseded = true
		attempt.err = ErrAttemptSuperseded
		close(attempt.done) // Immediately unblocks all waiters of this attempt!
		attemptToCancel = attempt.cancel
	}
	c.mu.Unlock()

	// Deterministically close idle TCP sockets on client retirement
	if oldTr != nil {
		oldTr.CloseIdleConnections()
	}

	// Cancel detached worker outside provider mutex to avoid lock inversion
	if attemptToCancel != nil {
		attemptToCancel()
	}
}

func (c *bedrockClientCache) getOrInitClient(ctx context.Context, b *BedrockProvider) (bedrockConverseClient, error) {
	for {
		c.mu.Lock()
		// Linearization Stage 1: Fast-path shutdown check
		if err := c.lifecycleCtx.Err(); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		// Fast-path auth failure check (0ms failure on uncorrected static keys)
		snap := currentAuthSnapshot(b.spec, c.configGen)
		if c.authLatch != nil && c.authLatch.IsLatched(snap) {
			err := c.authLatch.Err()
			c.mu.Unlock()
			return nil, err
		}
		if c.readyClient != nil {
			client := c.readyClient
			c.mu.Unlock()
			return client, nil
		}
		if c.active == nil {
			c.generation++
			workerCtx, cancel := context.WithTimeout(c.lifecycleCtx, 30*time.Second)
			c.active = &initAttempt{
				gen:    c.generation,
				done:   make(chan struct{}),
				cancel: cancel,
			}
			go c.runInit(c.active, workerCtx, b, snap)
		}
		attempt := c.active
		gen := attempt.gen
		hook := c.onAttemptJoined
		c.mu.Unlock()

		// Test hook invoked STRICTLY outside lock to prevent deadlocks
		if hook != nil {
			hook(gen)
		}

		select {
		case <-attempt.done:
			// Linearization Stage 2: Prioritized shutdown check on completion
			if err := c.lifecycleCtx.Err(); err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if errors.Is(attempt.err, ErrAttemptSuperseded) {
				// Linearization Stage 3: Shutdown check before retry loop
				if err := c.lifecycleCtx.Err(); err != nil {
					return nil, err
				}
				continue // Seamlessly join or start next generation
			}
			if attempt.err != nil {
				return nil, attempt.err
			}
			return attempt.client, nil
		case <-ctx.Done():
			// Individual caller cancelled or timed out!
			return nil, ctx.Err()
		case <-c.lifecycleCtx.Done():
			// Runtime shutdown unblocks all waiters immediately
			return nil, c.lifecycleCtx.Err()
		}
	}
}

func (c *bedrockClientCache) runInit(attempt *initAttempt, workerCtx context.Context, b *BedrockProvider, snap AuthSnapshot) {
	defer attempt.cancel() // Prevent context leak on worker completion

	var capturedTr *http.Transport
	buildableClient := awshttp.NewBuildableClient().WithTransportOptions(func(tr *http.Transport) {
		tr.MaxIdleConns = 100
		tr.IdleConnTimeout = 30 * time.Second
		tr.ForceAttemptHTTP2 = true
		tr.Proxy = http.ProxyFromEnvironment
		capturedTr = tr
	})
	if capturedTr == nil {
		capturedTr = buildableClient.GetTransport()
	}

	client, err := b.callNewClient(workerCtx, b.spec, buildableClient)

	c.mu.Lock()
	defer c.mu.Unlock()

	// If Invalidate() ran while worker executed, attempt.superseded is true,
	// done was already closed, and err was set to ErrAttemptSuperseded.
	if attempt.superseded || c.generation != attempt.gen || c.active != attempt {
		// STALE WORKER DISCARD:
		// Close idle connections of discarded transport immediately to avoid FD leaks!
		if capturedTr != nil {
			capturedTr.CloseIdleConnections()
		}
		return
	}

	// Normal completion
	attempt.completed = true
	attempt.client = client
	attempt.tr = capturedTr
	attempt.err = err
	c.active = nil

	if err == nil {
		c.readyClient = client
		c.tr = capturedTr
		if c.authLatch != nil {
			c.authLatch.Reset()
		}
	} else {
		c.readyClient = nil
		if capturedTr != nil {
			capturedTr.CloseIdleConnections()
		}
		kind, _, _ := bedrockErrorInfo(err)
		if kind == ErrKindAuth {
			if c.authLatch == nil {
				c.authLatch = &authFailedLatch{}
			}
			c.authLatch.Latch(err, snap)
		}
	}

	close(attempt.done) // Wakes all waiters who joined THIS specific attempt
}

func currentAuthSnapshot(spec Spec, configGen uint64) AuthSnapshot {
	snap := AuthSnapshot{
		AccessKeyID: os.Getenv("AWS_ACCESS_KEY_ID"),
		Profile:     spec.Profile,
		Region:      spec.Region,
		ConfigGen:   configGen,
	}
	if snap.Profile == "" {
		snap.Profile = os.Getenv("AWS_PROFILE")
	}
	if snap.Region == "" {
		snap.Region = os.Getenv("AWS_REGION")
	}

	home, _ := os.UserHomeDir()

	credsFile := os.Getenv("AWS_SHARED_CREDENTIALS_FILE")
	if credsFile == "" && home != "" {
		credsFile = filepath.Join(home, ".aws", "credentials")
	}
	if credsFile != "" {
		if fi, err := os.Stat(credsFile); err == nil {
			snap.CredsMTime = fi.ModTime()
		}
	}

	configFile := os.Getenv("AWS_CONFIG_FILE")
	if configFile == "" && home != "" {
		configFile = filepath.Join(home, ".aws", "config")
	}
	if configFile != "" {
		if fi, err := os.Stat(configFile); err == nil {
			snap.ConfigMTime = fi.ModTime()
		}
	}

	if home != "" {
		ssoDir := filepath.Join(home, ".aws", "sso", "cache")
		if entries, err := os.ReadDir(ssoDir); err == nil {
			var latest time.Time
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
					if info, err := entry.Info(); err == nil {
						if info.ModTime().After(latest) {
							latest = info.ModTime()
						}
					}
				}
			}
			snap.SSOMTime = latest
		}
	}

	return snap
}

func (b *BedrockProvider) handleSelectiveInvalidation(err error) {
	if err == nil {
		return
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		switch code {
		case "UnrecognizedClientException", "InvalidAccessKeyId", "InvalidTokenError":
			b.Invalidate()
		}
		return
	}
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "sso") && (strings.Contains(errStr, "expired") || strings.Contains(errStr, "invalid_grant")) {
		b.Invalidate()
	}
}

func (b *BedrockProvider) callNewClient(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
	if b.newClientWithHTTP != nil {
		return b.newClientWithHTTP(ctx, spec, httpClient)
	}
	if b.newClient != nil {
		return b.newClient(ctx, spec)
	}
	return defaultBedrockClientWithHTTP(ctx, spec, httpClient)
}

// displayName is the service name used in error messages.
func (b *BedrockProvider) displayName() string {
	if b.spec.Label != "" {
		return b.spec.Label
	}
	return "Amazon Bedrock"
}

// Capabilities reports what this transport supports. Parallel tool calls
// are reported as unsupported until per-model parallel behavior is
// verified; the runtime then runs tool batches sequentially, which is
// always safe.
func (b *BedrockProvider) Capabilities() Capabilities {
	base := Capabilities{
		Streaming:       true,
		ToolCalling:     true,
		MaxOutputTokens: 4096,
	}
	return b.spec.CapabilitiesOverride.Apply(base)
}

// defaultBedrockClient builds the SDK client for one request. The SDK
// retryer is disabled so Forcefield's policy owns every retry; the shared
// header-timeout HTTP client bounds the connection phase.
func defaultBedrockClient(ctx context.Context, spec Spec) (bedrockConverseClient, error) {
	return defaultBedrockClientWithHTTP(ctx, spec, nil)
}

func defaultBedrockClientWithHTTP(ctx context.Context, spec Spec, httpClient aws.HTTPClient) (bedrockConverseClient, error) {
	if strings.TrimSpace(spec.Region) == "" {
		return nil, fmt.Errorf("provider %q has no region - set providers.%s.region (or AWS_REGION)", spec.ID, spec.ID)
	}
	if httpClient == nil {
		httpClient = defaultBedrockHTTPClient()
	}
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(spec.Region),
		// Single attempt: Forcefield retries transient failures itself
		// with quota awareness the SDK retryer lacks.
		awsconfig.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }),
		awsconfig.WithHTTPClient(httpClient),
	}
	if strings.TrimSpace(spec.Profile) != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(spec.Profile))
	}
	if strings.TrimSpace(spec.BaseURL) != "" {
		endpoint := strings.TrimRight(spec.BaseURL, "/")
		if _, err := urlParseRequestURI(endpoint); err != nil {
			return nil, fmt.Errorf("provider %q base_url %q is not a valid endpoint override: %w", spec.ID, spec.BaseURL, err)
		}
		region := spec.Region
		opts = append(opts, awsconfig.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(func(service, _ string, _ ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{URL: endpoint, SigningRegion: region}, nil
			}),
		))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return sdkBedrockClient{client: bedrockruntime.NewFromConfig(cfg)}, nil
}

func defaultBedrockHTTPClient() *awshttp.BuildableClient {
	return awshttp.NewBuildableClient().WithTransportOptions(func(tr *http.Transport) {
		tr.MaxIdleConns = 100
		tr.IdleConnTimeout = 30 * time.Second
		tr.ForceAttemptHTTP2 = true
		tr.Proxy = http.ProxyFromEnvironment
	})
}

// bedrockHTTPClient returns the shared header-timeout transport for SDK
// use. Streaming bodies are bounded by the idle watchdog instead.
func bedrockHTTPClient(timeoutSeconds int) *http.Client {
	return clientForTimeout(timeoutSeconds)
}

// statusHint turns Bedrock failures into a concrete next step. The
// smithy error message already travels in the statusError body, so the
// hints carry guidance only.
func (b *BedrockProvider) statusHint(kind ErrorKind) string {
	switch kind {
	case ErrKindAuth:
		return fmt.Sprintf("check AWS credentials (environment, shared profile, or IAM role) and that model access is granted for %q in region %q (Bedrock console → Model access)", b.spec.Model, b.spec.Region)
	case ErrKindNotFound:
		return fmt.Sprintf("model %q was not found in region %q - verify the model ID, inference profile, or ARN and that it is available there", b.spec.Model, b.spec.Region)
	case ErrKindInvalidRequest:
		return "Bedrock rejected the request - the model may not support one of the requested features"
	default:
		return ""
	}
}

// bedrockErrorInfo classifies an SDK error: the normalized kind, whether
// Forcefield may retry it, and a redaction-safe message. Credential values
// never appear in SDK errors (SigV4 signs with them; messages carry names
// and codes only), and request content is never included.
func bedrockErrorInfo(err error) (kind ErrorKind, retryable bool, msg string) {
	if err == nil {
		return ErrKindUnknown, false, ""
	}
	if errors.Is(err, context.Canceled) {
		return ErrKindCanceled, false, "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrKindTimeout, true, "request timed out"
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		message := apiErr.ErrorMessage()
		// Quota exhaustion can arrive under throttling codes with
		// quota-worded messages; it must never retry regardless of
		// which code carried it.
		if code == "ServiceQuotaExceededException" || quotaExhausted(message) {
			return ErrKindQuota, false, message
		}
		switch code {
		case "ThrottlingException":
			return ErrKindRateLimit, true, message
		case "ModelTimeoutException":
			return ErrKindTimeout, true, message
		case "InternalServerException", "ServiceUnavailableException",
			"ModelStreamErrorException", "ModelNotReadyException":
			return ErrKindServer, true, message
		case "AccessDeniedException",
			"UnrecognizedClientException", "InvalidSignatureException",
			"ExpiredTokenException", "MissingAuthenticationTokenException",
			"InvalidAccessKeyId", "SignatureDoesNotMatch":
			return ErrKindAuth, false, message
		case "ValidationException":
			return ErrKindInvalidRequest, false, message
		case "ResourceNotFoundException":
			return ErrKindNotFound, false, message
		default:
			switch apiErr.ErrorFault() {
			case smithy.FaultServer:
				return ErrKindServer, true, message
			case smithy.FaultClient:
				return ErrKindInvalidRequest, false, message
			default:
				return ErrKindUnknown, false, message
			}
		}
	}
	// Transport-level failures: reuse the shared classification so
	// timeouts and connection resets retry like every other transport.
	switch Classify(err) {
	case ErrKindTimeout:
		return ErrKindTimeout, true, err.Error()
	case ErrKindConnection:
		return ErrKindConnection, true, err.Error()
	}
	// Credential-chain failures mention credentials but carry no values;
	// treat them as auth guidance. This is a message heuristic over SDK
	// errors, pinned by tests against the SDK's documented wording.
	if strings.Contains(strings.ToLower(err.Error()), "credential") {
		return ErrKindAuth, false, err.Error()
	}
	return ErrKindUnknown, false, err.Error()
}

// normalizeBedrockError converts an SDK failure into the shared
// *statusError vocabulary so Classify, IsTransient, and the runtime
// behave identically across transports.
func (b *BedrockProvider) normalizeBedrockError(err error, retries int) error {
	kind, retryable, msg := bedrockErrorInfo(err)
	se := &statusError{
		Provider: b.displayName(),
		Model:    b.spec.Model,
		Body:     msg,
		Kind:     kind,
		Retries:  retries,
	}
	switch kind {
	case ErrKindRateLimit:
		se.RateLimited = true
	case ErrKindQuota:
		se.RateLimited = true
		se.NonRetryable = true
	case ErrKindAuth, ErrKindNotFound, ErrKindInvalidRequest, ErrKindCanceled:
		se.NonRetryable = true
	}
	if hint := b.statusHint(kind); hint != "" {
		se.Hint = hint
	}
	_ = retryable
	return se
}

// callWithRetry runs one SDK operation under Forcefield's bounded retry
// policy. The SDK client is configured for a single attempt, so every
// retry here is deliberate, quota-aware, and counted.
func (b *BedrockProvider) callWithRetry(ctx context.Context, call func() error) error {
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := call()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		kind, retryable, _ := bedrockErrorInfo(err)
		if !retryable || kind == ErrKindCanceled {
			return b.normalizeBedrockError(err, attempt)
		}
		if attempt >= b.retry.MaxRetries {
			return b.normalizeBedrockError(err, attempt)
		}
		delay := backoffDelay(b.retry, attempt)
		select {
		case <-time.After(delay):
			continue
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// bedrockStopReasonFor maps a Converse stopReason onto its normalized
// form. Context exhaustion maps to length so the runtime emits its
// narrow-output guidance; filtering/guardrail outcomes end the turn.
func bedrockStopReasonFor(reason brtypes.StopReason) FinishReason {
	switch reason {
	case brtypes.StopReasonToolUse:
		return FinishToolCalls
	case brtypes.StopReasonMaxTokens, brtypes.StopReasonModelContextWindowExceeded:
		return FinishLength
	default:
		return FinishStop
	}
}

// toBedrockMessages converts Forcefield history into Converse form:
// system prompts become system blocks, assistant tool calls become
// assistant-turn toolUse blocks, and consecutive tool results merge into
// one user turn with multiple toolResult blocks.
func toBedrockMessages(messages []Message) (system []brtypes.SystemContentBlock, out []brtypes.Message, err error) {
	var pendingResults []brtypes.ContentBlock
	flushResults := func() {
		if len(pendingResults) > 0 {
			out = append(out, brtypes.Message{
				Role:    brtypes.ConversationRoleUser,
				Content: pendingResults,
			})
			pendingResults = nil
		}
	}
	for _, msg := range messages {
		switch msg.Role {
		case SystemRole:
			if msg.Content != "" {
				text := msg.Content
				system = append(system, &brtypes.SystemContentBlockMemberText{Value: text})
			}
		case AssistantRole:
			flushResults()
			var blocks []brtypes.ContentBlock
			if msg.Content != "" {
				blocks = append(blocks, &brtypes.ContentBlockMemberText{Value: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				doc := bedrockDocFromMap(tc.Arguments)
				id, name := tc.ID, tc.Name
				blocks = append(blocks, &brtypes.ContentBlockMemberToolUse{Value: brtypes.ToolUseBlock{
					ToolUseId: &id,
					Name:      &name,
					Input:     doc,
				}})
			}
			if len(blocks) > 0 {
				out = append(out, brtypes.Message{Role: brtypes.ConversationRoleAssistant, Content: blocks})
			}
		case ToolRole:
			if msg.ToolCallID == "" {
				return nil, nil, fmt.Errorf("tool result for %q has no tool call ID", msg.Name)
			}
			id := msg.ToolCallID
			pendingResults = append(pendingResults, &brtypes.ContentBlockMemberToolResult{Value: brtypes.ToolResultBlock{
				ToolUseId: &id,
				Content: []brtypes.ToolResultContentBlock{
					&brtypes.ToolResultContentBlockMemberText{Value: msg.Content},
				},
			}})
		case UserRole:
			flushResults()
			if msg.Content != "" {
				out = append(out, brtypes.Message{
					Role:    brtypes.ConversationRoleUser,
					Content: []brtypes.ContentBlock{&brtypes.ContentBlockMemberText{Value: msg.Content}},
				})
			}
		}
	}
	flushResults()
	return system, out, nil
}

// toBedrockTools converts tool definitions into a Converse
// ToolConfiguration. Empty definitions yield nil (no toolConfig sent).
func toBedrockTools(defs []tools.Definition) *brtypes.ToolConfiguration {
	if len(defs) == 0 {
		return nil
	}
	specs := make([]brtypes.Tool, 0, len(defs))
	for _, def := range defs {
		schema := def.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		name, desc := def.Name, def.Description
		specs = append(specs, &brtypes.ToolMemberToolSpec{Value: brtypes.ToolSpecification{
			Name:        &name,
			Description: &desc,
			InputSchema: &brtypes.ToolInputSchemaMemberJson{Value: bedrockDocFromMap(schema)},
		}})
	}
	return &brtypes.ToolConfiguration{Tools: specs}
}

// bedrockDocFromMap converts an argument map into an SDK document for
// request input schemas and toolUse blocks.
func bedrockDocFromMap(m map[string]any) brtdoc.Interface {
	if m == nil {
		m = map[string]any{}
	}
	return brtdoc.NewLazyDocument(m)
}

// bedrockMapFromDoc converts a response document (toolUse input) back
// into an argument map using the SDK's own document decoding. Numbers
// arrive as float64, matching the JSON decoding every other adapter
// performs.
func bedrockMapFromDoc(doc brtdoc.Interface) (map[string]any, error) {
	if doc == nil {
		return nil, nil
	}
	// Marshal through the SDK document encoding, then decode generically.
	// Numbers arrive as float64, matching the JSON decoding every other
	// adapter performs.
	raw, err := doc.MarshalSmithyDocument()
	if err != nil {
		return nil, fmt.Errorf("encode tool input document: %w", err)
	}
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode tool input document: %w", err)
	}
	return m, nil
}

// bedrockUsage converts Converse token usage, tolerating nil counters.
func bedrockUsage(u *brtypes.TokenUsage) *Usage {
	if u == nil {
		return nil
	}
	out := &Usage{}
	if u.InputTokens != nil {
		out.PromptTokens = int(*u.InputTokens)
	}
	if u.OutputTokens != nil {
		out.CompletionTokens = int(*u.OutputTokens)
	}
	if u.TotalTokens != nil {
		out.TotalTokens = int(*u.TotalTokens)
	} else {
		out.TotalTokens = out.PromptTokens + out.CompletionTokens
	}
	return out
}

// bedrockToolBuf accumulates one streamed toolUse block until its input
// JSON fragments are complete.
type bedrockToolBuf struct {
	ID      string
	Name    string
	ArgsBuf string
	Order   int
}

func finalizeBedrockToolCalls(pending map[int32]*bedrockToolBuf, order []int32) ([]ToolCall, error) {
	calls := make([]ToolCall, 0, len(order))
	for _, idx := range order {
		buf := pending[idx]
		var args map[string]any
		if buf.ArgsBuf != "" {
			if err := json.Unmarshal([]byte(buf.ArgsBuf), &args); err != nil {
				return nil, err
			}
		}
		calls = append(calls, ToolCall{ID: buf.ID, Name: buf.Name, Arguments: args})
	}
	return calls, nil
}

// StreamChat sends the conversation via ConverseStream and streams the
// response as StreamEvent values. The channel closes when the turn ends,
// the context is canceled, or an error occurs. Mid-stream failures are
// reported as stream errors, never replayed.
func (b *BedrockProvider) StreamChat(ctx context.Context, messages []Message, defs []tools.Definition) (<-chan StreamEvent, error) {
	if caps := b.Capabilities(); !caps.ToolCalling && len(defs) > 0 {
		return nil, unsupportedCapability(b.displayName(), "tool calling", "providers.<id>.capabilities.tool_calling")
	}
	system, converseMsgs, err := toBedrockMessages(messages)
	if err != nil {
		return nil, err
	}
	input := &bedrockruntime.ConverseStreamInput{
		ModelId:    &b.spec.Model,
		Messages:   converseMsgs,
		System:     system,
		ToolConfig: toBedrockTools(defs),
	}
	if err := b.gate.acquire(); err != nil {
		return nil, err
	}
	client, err := b.getCache().getOrInitClient(ctx, b)
	if err != nil {
		b.gate.release()
		return nil, b.normalizeBedrockError(err, 0)
	}
	var stream bedrockStream
	if err := b.callWithRetry(ctx, func() error {
		var callErr error
		stream, callErr = client.ConverseStream(ctx, input)
		return callErr
	}); err != nil {
		b.gate.release()
		b.handleSelectiveInvalidation(err)
		return nil, err
	}

	// Buffered so the event reader stays ahead of the consumer instead
	// of rendezvousing per token. Order is unchanged (single producer);
	// cancellation still preempts via ctx.
	events := make(chan StreamEvent, 32)

	go func() {
		defer close(events)
		defer b.gate.release()
		defer stream.Close()

		send := func(event StreamEvent) bool {
			select {
			case events <- event:
				return true
			case <-ctx.Done():
				return false
			}
		}

		pending := map[int32]*bedrockToolBuf{}
		order := []int32{}
		var usage *Usage
		var stop FinishReason

		finishTurn := func(reason FinishReason) {
			if len(order) > 0 {
				calls, err := finalizeBedrockToolCalls(pending, order)
				if err != nil {
					send(StreamEvent{Err: &protocolError{msg: "decode tool call arguments: " + err.Error()}})
					return
				}
				if !send(StreamEvent{ToolCalls: calls}) {
					return
				}
			}
			send(StreamEvent{Done: true, StopReason: reason, Usage: usage})
		}

		// idle bounds a stalled stream the same way the HTTP transports
		// bound stalled bodies: any delivered event resets it, so active
		// streams run as long as Bedrock keeps sending.
		idle := time.NewTimer(defaultBodyIdleTimeout)
		defer idle.Stop()
		eventCh := stream.Events()

		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-eventCh:
				if !ok {
					if err := stream.Err(); err != nil {
						if ctx.Err() != nil {
							return
						}
						kind, _, _ := bedrockErrorInfo(err)
						if kind == ErrKindCanceled {
							return
						}
						b.handleSelectiveInvalidation(err)
						send(StreamEvent{Err: b.normalizeBedrockError(err, 0)})
						return
					}
					if stop == FinishNone {
						send(StreamEvent{Err: &protocolError{msg: "stream ended without a terminal marker (messageStop); response is incomplete"}})
						return
					}
					finishTurn(stop)
					return
				}
				if !idle.Stop() {
					select {
					case <-idle.C:
					default:
					}
				}
				idle.Reset(defaultBodyIdleTimeout)

				switch e := ev.(type) {
				case *brtypes.ConverseStreamOutputMemberContentBlockDelta:
					idx := int32(0)
					if e.Value.ContentBlockIndex != nil {
						idx = *e.Value.ContentBlockIndex
					}
					switch delta := e.Value.Delta.(type) {
					case *brtypes.ContentBlockDeltaMemberText:
						if !send(StreamEvent{Text: delta.Value}) {
							return
						}
					case *brtypes.ContentBlockDeltaMemberToolUse:
						buf, seen := pending[idx]
						if !seen {
							buf = &bedrockToolBuf{Order: len(order)}
							pending[idx] = buf
							order = append(order, idx)
						}
						if delta.Value.Input != nil {
							buf.ArgsBuf += *delta.Value.Input
						}
					default:
						// Reasoning and future delta kinds are ignored
						// rather than failing the turn; text and tool
						// use carry the agent loop.
					}
				case *brtypes.ConverseStreamOutputMemberContentBlockStart:
					idx := int32(0)
					if e.Value.ContentBlockIndex != nil {
						idx = *e.Value.ContentBlockIndex
					}
					if start, ok := e.Value.Start.(*brtypes.ContentBlockStartMemberToolUse); ok {
						buf, seen := pending[idx]
						if !seen {
							buf = &bedrockToolBuf{Order: len(order)}
							pending[idx] = buf
							order = append(order, idx)
						}
						if start.Value.ToolUseId != nil {
							buf.ID += *start.Value.ToolUseId
						}
						if start.Value.Name != nil {
							buf.Name += *start.Value.Name
						}
					}
				case *brtypes.ConverseStreamOutputMemberContentBlockStop:
					// Delimited by index; assembly completes at flush.
				case *brtypes.ConverseStreamOutputMemberMessageStop:
					stop = bedrockStopReasonFor(e.Value.StopReason)
				case *brtypes.ConverseStreamOutputMemberMetadata:
					if e.Value.Usage != nil {
						usage = bedrockUsage(e.Value.Usage)
					}
				case *brtypes.ConverseStreamOutputMemberMessageStart:
					// Role marker only; nothing to emit.
				default:
					// Unknown union members are forward-compatible
					// additions; ignore rather than fail.
				}
			case <-idle.C:
				stream.Close()
				send(StreamEvent{Err: b.normalizeBedrockError(context.DeadlineExceeded, 0)})
				return
			}
		}
	}()

	return events, nil
}

// Complete performs one non-streaming Converse call and returns the full
// response.
func (b *BedrockProvider) Complete(ctx context.Context, messages []Message, defs []tools.Definition) (Response, error) {
	if caps := b.Capabilities(); !caps.ToolCalling && len(defs) > 0 {
		return Response{}, unsupportedCapability(b.displayName(), "tool calling", "providers.<id>.capabilities.tool_calling")
	}
	system, converseMsgs, err := toBedrockMessages(messages)
	if err != nil {
		return Response{}, err
	}
	input := &bedrockruntime.ConverseInput{
		ModelId:    &b.spec.Model,
		Messages:   converseMsgs,
		System:     system,
		ToolConfig: toBedrockTools(defs),
	}
	if err := b.gate.acquire(); err != nil {
		return Response{}, err
	}
	defer b.gate.release()
	client, err := b.getCache().getOrInitClient(ctx, b)
	if err != nil {
		return Response{}, b.normalizeBedrockError(err, 0)
	}
	var out *bedrockruntime.ConverseOutput
	if err := b.callWithRetry(ctx, func() error {
		var callErr error
		out, callErr = client.Converse(ctx, input)
		return callErr
	}); err != nil {
		b.handleSelectiveInvalidation(err)
		return Response{}, err
	}
	if out == nil {
		return Response{}, &protocolError{msg: "empty response from Bedrock"}
	}
	response := Response{}
	if msg, ok := out.Output.(*brtypes.ConverseOutputMemberMessage); ok {
		for _, block := range msg.Value.Content {
			switch blk := block.(type) {
			case *brtypes.ContentBlockMemberText:
				response.Content += blk.Value
			case *brtypes.ContentBlockMemberToolUse:
				args, err := bedrockMapFromDoc(blk.Value.Input)
				if err != nil {
					return Response{}, &protocolError{msg: "decode tool call arguments: " + err.Error()}
				}
				id, name := "", ""
				if blk.Value.ToolUseId != nil {
					id = *blk.Value.ToolUseId
				}
				if blk.Value.Name != nil {
					name = *blk.Value.Name
				}
				response.ToolCalls = append(response.ToolCalls, ToolCall{ID: id, Name: name, Arguments: args})
			default:
				// Image/document/reasoning blocks have no Forcefield
				// message representation yet; skip rather than fail.
			}
		}
	}
	if out.Usage != nil {
		if u := bedrockUsage(out.Usage); u != nil {
			response.Usage = *u
		}
	}
	response.StopReason = bedrockStopReasonFor(out.StopReason)
	if response.StopReason == FinishNone {
		response.StopReason = FinishStop
	}
	return response, nil
}

// urlParseRequestURI validates an endpoint-override URL: absolute
// http(s) with a host, mirroring the shared base-URL rules without
// pulling URL helpers into shared scope for one provider's field.
func urlParseRequestURI(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("endpoint %q is not a valid URL: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("endpoint %q must be an absolute http:// or https:// URL with a host", raw)
	}
	// Mirror the shared base-URL rules: query and fragment in an endpoint
	// override would corrupt the signed request path the SDK assembles.
	if strings.ContainsAny(raw, "?#") {
		return "", fmt.Errorf("endpoint %q must be a plain URL without query or fragment", raw)
	}
	return raw, nil
}
