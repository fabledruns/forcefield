package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"forcefield/internal/tools"
)

// DefaultLemonadeBaseURL is the default API endpoint for AMD Lemonade Server.
const DefaultLemonadeBaseURL = "http://localhost:13305/api/v1"

const (
	lemonadeMetaTimeout       = 750 * time.Millisecond
	lemonadeContextSuccessTTL = 30 * time.Second
	lemonadeContextFailureTTL = 5 * time.Second
)

// detectWSL2 checks whether Forcefield is executing inside Windows Subsystem
// for Linux (WSL2), used strictly for actionable connection-refused hints.
// Swappable in unit tests via isWSL2.
var isWSL2 = func() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if strings.TrimSpace(os.Getenv("WSL_DISTRO_NAME")) != "" {
		return true
	}
	raw, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(raw)), "microsoft")
}

// IsWSL2Host reports whether the current process is running under WSL2 on Linux.
func IsWSL2Host() bool {
	return isWSL2()
}

// NormalizeLemonadeEndpoints derives the API base URL (ending in /api/v1, /v1,
// /api/v0, or /v0) and the server root base URL (where /live is mounted) from
// a configured Lemonade base URL without mutating the caller's Spec.BaseURL.
func NormalizeLemonadeEndpoints(raw string) (apiBase, rootBase string) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		trimmed = DefaultLemonadeBaseURL
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		api := trimmed
		root := trimmed
		for _, suffix := range []string{"/api/v1", "/api/v0", "/v1", "/v0"} {
			if strings.HasSuffix(api, suffix) {
				root = strings.TrimRight(strings.TrimSuffix(api, suffix), "/")
				return api, root
			}
		}
		return strings.TrimRight(api, "/") + "/api/v1", root
	}

	path := strings.TrimRight(u.Path, "/")
	origin := u.Scheme + "://" + u.Host

	for _, suffix := range []string{"/api/v1", "/api/v0", "/v1", "/v0"} {
		if strings.HasSuffix(path, suffix) {
			prefix := strings.TrimRight(strings.TrimSuffix(path, suffix), "/")
			apiU := *u
			apiU.Path = prefix + suffix
			apiU.RawPath = ""
			rootU := *u
			rootU.Path = prefix
			rootU.RawPath = ""
			return strings.TrimRight(apiU.String(), "/"), strings.TrimRight(rootU.String(), "/")
		}
	}

	if path == "" {
		apiU := *u
		apiU.Path = "/api/v1"
		apiU.RawPath = ""
		return strings.TrimRight(apiU.String(), "/"), origin
	}

	// Non-standard subpath: keep apiBase as configured and strip to origin for root /live.
	return strings.TrimRight(u.String(), "/"), origin
}

// LemonadeChatEndpoint derives the OpenAI-compatible chat completions endpoint
// (/v1/chat/completions) from a configured Lemonade base URL. It strips management
// route prefixes (/api/v1, /api/v0, /api/v1/chat) and ensures the /v1 segment is
// not duplicated whether the configured URL has a trailing slash, an existing
// /v1 suffix, or no path.
func LemonadeChatEndpoint(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		trimmed = DefaultLemonadeBaseURL
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		base := trimmed
		for _, suffix := range []string{
			"/api/v1/chat/completions",
			"/api/v1/chat",
			"/v1/chat/completions",
			"/api/v1",
			"/api/v0",
			"/v1",
			"/v0",
		} {
			if strings.HasSuffix(base, suffix) {
				base = strings.TrimSuffix(base, suffix)
				break
			}
		}
		return strings.TrimRight(base, "/") + "/v1/chat/completions"
	}

	path := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{
		"/api/v1/chat/completions",
		"/api/v1/chat",
		"/v1/chat/completions",
		"/api/v1",
		"/api/v0",
		"/v1",
		"/v0",
	} {
		if strings.HasSuffix(path, suffix) {
			prefix := strings.TrimRight(strings.TrimSuffix(path, suffix), "/")
			u2 := *u
			u2.Path = prefix + "/v1/chat/completions"
			u2.RawPath = ""
			return strings.TrimRight(u2.String(), "/")
		}
	}

	if path == "" {
		u2 := *u
		u2.Path = "/v1/chat/completions"
		u2.RawPath = ""
		return strings.TrimRight(u2.String(), "/")
	}

	u2 := *u
	u2.Path = path + "/v1/chat/completions"
	u2.RawPath = ""
	return strings.TrimRight(u2.String(), "/")
}

type lemonadeContextEntry struct {
	contextWindow   int
	maxOutputTokens int
	fetchedAt       time.Time
	failed          bool
}

type lemonadeContextCache struct {
	mu      sync.Mutex
	entries map[string]lemonadeContextEntry
}

func newLemonadeContextCache() *lemonadeContextCache {
	return &lemonadeContextCache{
		entries: make(map[string]lemonadeContextEntry),
	}
}

func (c *lemonadeContextCache) get(model string) (lemonadeContextEntry, bool) {
	if c == nil || model == "" {
		return lemonadeContextEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[model]
	return e, ok
}

func (c *lemonadeContextCache) set(model string, e lemonadeContextEntry) {
	if c == nil || model == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[model] = e
}

// LemonadeProvider is Forcefield's dedicated adapter for AMD Lemonade Server.
// Unlike generic OpenAI-compatible endpoints, Lemonade supports Quad prefixes,
// top-level enable_thinking, trailing SSE usage+timings chunks after
// finish_reason, pre-load resolved_ctx_size lookup, and residency conflict codes.
var (
	_ SlotStatsProvider    = (*LemonadeProvider)(nil)
	_ RunSlotStatsProvider = (*LemonadeProvider)(nil)
)

type lemonadeRunCounters struct {
	admitted atomic.Uint64
	waits    atomic.Uint64
	waitMS   atomic.Int64
}

type LemonadeProvider struct {
	spec       Spec
	apiBase    string
	rootBase   string
	client     *http.Client
	metaClient *http.Client
	retry      retryPolicy
	gate       *requestGate
	reasoning  ReasoningConfig
	ctxCache   *lemonadeContextCache
	slotGuard  *lemonadeSlotGuard
	runStats   *lemonadeRunCounters
	now        func() time.Time
}

// NewLemonade builds a LemonadeProvider for the given resolved Spec.
// Like all Forcefield constructors, it performs zero network I/O eagerly.
func NewLemonade(spec Spec) *LemonadeProvider {
	if spec.BaseURL != "" {
		spec.BaseURL = strings.TrimRight(spec.BaseURL, "/")
	}
	apiBase, rootBase := NormalizeLemonadeEndpoints(spec.BaseURL)

	client := newDefaultClient()
	metaTimeout := lemonadeMetaTimeout
	if spec.TimeoutSeconds > 0 {
		client = clientForTimeout(spec.TimeoutSeconds)
		if d := time.Duration(spec.TimeoutSeconds) * time.Second; d < metaTimeout {
			metaTimeout = d
		}
	}
	metaClient := &http.Client{
		Timeout:       metaTimeout,
		CheckRedirect: safeRedirectCheck,
	}

	p := &LemonadeProvider{
		spec:       spec,
		apiBase:    apiBase,
		rootBase:   rootBase,
		client:     client,
		metaClient: metaClient,
		retry:      defaultRetryPolicy,
		gate:       newRequestGate(),
		ctxCache:   newLemonadeContextCache(),
		runStats:   &lemonadeRunCounters{},
		now:        time.Now,
	}
	if !spec.DisableSlotGuard {
		p.slotGuard = newLemonadeSlotGuard(apiBase, p.displayName(), metaClient, func(ctx context.Context, method, fullURL, accept string, body io.Reader) (*http.Request, error) {
			return p.newRequest(ctx, method, fullURL, accept, body)
		}, time.Now)
	}
	return p
}

// CloneForRun returns an independent run-scoped instance sharing the HTTP
// connection pool, immutable Spec, read-through context cache, and shared
// SlotGuard coordinator while keeping its own per-run requestGate and reasoning
// configuration.
func (l *LemonadeProvider) CloneForRun() (ModelProvider, error) {
	if l == nil {
		return nil, fmt.Errorf("cannot clone a nil LemonadeProvider")
	}
	c := *l
	c.gate = newRequestGate()
	c.reasoning = ReasoningConfig{}
	c.runStats = &lemonadeRunCounters{}
	return &c, nil
}

// SlotStats returns numeric-only SlotGuard admission and wait counters for this
// provider and all run-scoped clones sharing its coordinator.
func (l *LemonadeProvider) SlotStats() SlotGuardStats {
	if l == nil {
		return SlotGuardStats{}
	}
	return l.slotGuard.Stats()
}

// SlotGuardEnabled reports whether SlotGuard is active for this adapter.
func (l *LemonadeProvider) SlotGuardEnabled() bool {
	return l != nil && l.slotGuard != nil
}

// RunSlotStats returns numeric-only SlotGuard counters scoped strictly to this
// provider instance's own admitted turns (isolated per CloneForRun instance).
func (l *LemonadeProvider) RunSlotStats() SlotGuardStats {
	if l == nil || l.runStats == nil {
		return SlotGuardStats{}
	}
	return SlotGuardStats{
		Admitted:    l.runStats.admitted.Load(),
		Waits:       l.runStats.waits.Load(),
		TotalWaitMS: l.runStats.waitMS.Load(),
	}
}

func (l *LemonadeProvider) displayName() string {
	if l.spec.Label != "" {
		return l.spec.Label
	}
	if l.spec.ID != "" {
		return l.spec.ID
	}
	return "AMD Lemonade"
}

// Capabilities returns static capabilities merged with any configured overrides
// and any previously discovered model context/output limits in ctxCache.
// Fresh instances perform zero network calls and match staticTypeCapabilities["lemonade"].
func (l *LemonadeProvider) Capabilities() Capabilities {
	base := staticTypeCapabilities["lemonade"]
	if e, ok := l.ctxCache.get(l.spec.Model); ok && !e.failed {
		if e.contextWindow > 0 {
			base.ContextWindow = e.contextWindow
		}
		if e.maxOutputTokens > 0 {
			base.MaxOutputTokens = e.maxOutputTokens
		}
	}
	return l.spec.CapabilitiesOverride.Apply(base)
}

// ChatPath returns the configured chat-completions path or the default.
func (l *LemonadeProvider) ChatPath() string {
	return l.spec.EffectiveChatPath()
}

// SetReasoning stores the reasoning config for subsequent requests.
func (l *LemonadeProvider) SetReasoning(cfg ReasoningConfig) {
	deep := ReasoningConfig{Effort: cfg.Effort}
	if cfg.Thinking != nil {
		tc := ThinkingConfig{Level: cfg.Thinking.Level}
		if cfg.Thinking.Enabled != nil {
			v := *cfg.Thinking.Enabled
			tc.Enabled = &v
		}
		if cfg.Thinking.Budget != nil {
			v := *cfg.Thinking.Budget
			tc.Budget = &v
		}
		deep.Thinking = &tc
	}
	l.reasoning = deep
}

// GetReasoning returns a deep copy of the active reasoning configuration.
func (l *LemonadeProvider) GetReasoning() ReasoningConfig {
	deep := ReasoningConfig{Effort: l.reasoning.Effort}
	if l.reasoning.Thinking != nil {
		tc := ThinkingConfig{Level: l.reasoning.Thinking.Level}
		if l.reasoning.Thinking.Enabled != nil {
			v := *l.reasoning.Thinking.Enabled
			tc.Enabled = &v
		}
		if l.reasoning.Thinking.Budget != nil {
			v := *l.reasoning.Thinking.Budget
			tc.Budget = &v
		}
		deep.Thinking = &tc
	}
	return deep
}

func (l *LemonadeProvider) applyAuth(req *http.Request) error {
	mode := l.spec.NormalizedAuthMode()
	key := l.spec.APIKey
	switch mode {
	case "none":
		return nil
	case "header":
		if key == "" {
			return nil
		}
		if !IsValidAPIKey(key) {
			return invalidAPIKeyError(l.spec.ID, "")
		}
		if l.spec.AuthHeader == "" {
			return nil
		}
		value := key
		if l.spec.AuthScheme != "" {
			value = l.spec.AuthScheme + " " + key
		}
		req.Header.Set(l.spec.AuthHeader, value)
		return nil
	default: // "", "bearer"
		if key == "" {
			return nil
		}
		if !IsValidAPIKey(key) {
			return invalidAPIKeyError(l.spec.ID, "")
		}
		scheme := "Bearer"
		if l.spec.AuthScheme != "" {
			scheme = l.spec.AuthScheme
		}
		req.Header.Set("Authorization", scheme+" "+key)
		return nil
	}
}

func (l *LemonadeProvider) newRequest(ctx context.Context, method, fullURL, accept string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, fmt.Errorf("build request to %s: %w", fullURL, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if err := l.applyAuth(req); err != nil {
		return nil, err
	}
	for name, value := range l.spec.Headers {
		if !IsValidHeaderValue(value) {
			continue
		}
		req.Header.Set(name, value)
	}
	return req, nil
}

func (l *LemonadeProvider) redact(err error) error {
	return Redacted(err, l.spec.APIKey)
}

func (l *LemonadeProvider) statusHint(status int, body string) string {
	lowerBody := strings.ToLower(body)
	switch status {
	case http.StatusConflict:
		return fmt.Sprintf("Lemonade residency conflict while serving model %q (pinned model saturation or max_models limit reached) - check loaded/pinned models and VRAM with `ff doctor`", l.spec.Model)
	case http.StatusNotFound:
		return fmt.Sprintf("model %q was not found on %s - verify it is downloaded via `ff doctor` or switch models with /model", l.spec.Model, l.displayName())
	case http.StatusBadRequest:
		if strings.Contains(lowerBody, "on-demand") || strings.Contains(lowerBody, "ondemand") || strings.Contains(lowerBody, "download") {
			return fmt.Sprintf("model %q cannot be served directly (on-demand or un-downloaded model) - download or check its recipe options with `ff doctor`", l.spec.Model)
		}
		return ""
	case http.StatusUnauthorized, http.StatusForbidden:
		if l.spec.APIKey == "" {
			return fmt.Sprintf("%s rejected an unauthenticated request - configure providers.%s.api_key_env if your Lemonade server requires authentication", l.displayName(), l.spec.ID)
		}
		return fmt.Sprintf("check that the API key for %s is valid and authorized", l.displayName())
	default:
		return ""
	}
}

// WSL2LoopbackConnectionHint returns an actionable networking hint if the process
// is running inside WSL2, the target endpoint is loopback, and the error indicates
// a refused connection. Returns "" otherwise.
func WSL2LoopbackConnectionHint(targetURL string, err error) string {
	if err == nil || !IsWSL2Host() {
		return ""
	}
	u, parseErr := url.Parse(targetURL)
	if parseErr != nil || !isLoopbackHostname(u.Hostname()) {
		return ""
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "connection refused") && !strings.Contains(msg, "refused") {
		return ""
	}
	return fmt.Sprintf("WSL2 note: %s is unreachable from WSL2. If Lemonade Server is running on Windows 11, enable networkingMode=mirrored in .wslconfig or point base_url at the Windows host IP (or run lemond natively inside WSL2)", targetURL)
}

func (l *LemonadeProvider) wrapTransport(err error) error {
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%s did not respond within the configured header timeout for %s (check whether Lemonade is cold-loading %q or raise providers.%s.timeout_seconds in config.yaml): %w",
			l.displayName(), l.apiBase, l.spec.Model, l.spec.ID, err)
	}
	if hint := WSL2LoopbackConnectionHint(l.apiBase, err); hint != "" {
		return fmt.Errorf("could not reach %s at %s: %w (%s)", l.displayName(), l.apiBase, err, hint)
	}
	return fmt.Errorf("could not reach %s at %s: %w", l.displayName(), l.apiBase, err)
}

func (l *LemonadeProvider) acquireInferenceTurn(ctx context.Context) (*slotLease, error) {
	if err := l.gate.acquire(); err != nil {
		return nil, err
	}
	lease, err := l.slotGuard.Acquire(ctx, l.spec.Model)
	if err != nil {
		l.gate.release()
		return nil, l.redact(err)
	}
	if l.slotGuard != nil && l.runStats != nil {
		l.runStats.admitted.Add(1)
		if w := lease.WaitMS(); w > 0 {
			l.runStats.waits.Add(1)
			l.runStats.waitMS.Add(w)
		}
	}
	return lease, nil
}

func (l *LemonadeProvider) do(ctx context.Context, method, path, accept string, body []byte) (*http.Response, error) {
	if err := l.gate.acquire(); err != nil {
		return nil, err
	}
	resp, err := l.doWithGate(ctx, method, path, accept, body)
	if err != nil {
		l.gate.release()
		return nil, err
	}
	return resp, nil
}

func (l *LemonadeProvider) doWithGate(ctx context.Context, method, path, accept string, body []byte) (*http.Response, error) {
	buildRequest := func() (*http.Request, error) {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		return l.newRequest(ctx, method, l.apiBase+path, accept, reader)
	}

	resp, err := doWithRetry(ctx, l.client, l.retry, l.displayName(), l.spec.Model, buildRequest, l.wrapTransport)
	if err != nil {
		return nil, l.redact(annotateStatusHint(err, l.statusHint))
	}
	return resp, nil
}

// RefreshContextWindow performs a fast, read-only lookup of the active model's
// effective context window without mutating Lemonade server state or loading any
// model. Results (and offline failures) are cached briefly to avoid slowing down
// subsequent turns.
func (l *LemonadeProvider) RefreshContextWindow(ctx context.Context) int {
	if l == nil || strings.TrimSpace(l.spec.Model) == "" {
		return 0
	}
	now := l.now()
	if entry, ok := l.ctxCache.get(l.spec.Model); ok {
		ttl := lemonadeContextSuccessTTL
		if entry.failed {
			ttl = lemonadeContextFailureTTL
		}
		if now.Sub(entry.fetchedAt) < ttl {
			return entry.contextWindow
		}
	}

	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, l.metaClient.Timeout)
	defer cancel()

	escapedModel := url.PathEscape(l.spec.Model)
	var ctxWindow int
	var maxOut int
	var sawServerResponse bool

	// 1. Primary source: GET /models/{id}/options -> resolved_ctx_size.
	if status, raw, err := l.probeMetaGET(probeCtx, l.apiBase+"/models/"+escapedModel+"/options"); err == nil {
		sawServerResponse = true
		if status == http.StatusOK {
			var opts struct {
				ResolvedCtxSize int `json:"resolved_ctx_size"`
				Effective       struct {
					CtxSize int `json:"ctx_size"`
				} `json:"effective"`
			}
			if json.Unmarshal(raw, &opts) == nil {
				if opts.ResolvedCtxSize > 0 {
					ctxWindow = opts.ResolvedCtxSize
				} else if opts.Effective.CtxSize > 0 {
					ctxWindow = opts.Effective.CtxSize
				}
			}
		}
	} else {
		l.ctxCache.set(l.spec.Model, lemonadeContextEntry{
			fetchedAt: now,
			failed:    true,
		})
		return 0
	}

	// 2. Fallback: GET /models/{id} -> context_length / max_context_window / max_output_tokens.
	if status, raw, err := l.probeMetaGET(probeCtx, l.apiBase+"/models/"+escapedModel); err == nil {
		sawServerResponse = true
		if status == http.StatusOK {
			var mdl struct {
				ContextLength       int `json:"context_length"`
				MaxContextWindow    int `json:"max_context_window"`
				MaxOutputTokens     int `json:"max_output_tokens"`
				MaxCompletionTokens int `json:"max_completion_tokens"`
			}
			if json.Unmarshal(raw, &mdl) == nil {
				if ctxWindow <= 0 {
					if mdl.ContextLength > 0 {
						ctxWindow = mdl.ContextLength
					} else if mdl.MaxContextWindow > 0 {
						ctxWindow = mdl.MaxContextWindow
					}
				}
				if mdl.MaxOutputTokens > 0 {
					maxOut = mdl.MaxOutputTokens
				} else if mdl.MaxCompletionTokens > 0 {
					maxOut = mdl.MaxCompletionTokens
				}
			}
		}
	}

	// 3. Fallback: GET /health -> all_models_loaded[] matching model_name.
	if ctxWindow <= 0 {
		if status, raw, err := l.probeMetaGET(probeCtx, l.apiBase+"/health"); err == nil && status == http.StatusOK {
			sawServerResponse = true
			var health struct {
				AllModelsLoaded []struct {
					ModelName        string `json:"model_name"`
					MaxContextWindow int    `json:"max_context_window"`
					RecipeOptions    struct {
						CtxSize int `json:"ctx_size"`
					} `json:"recipe_options"`
				} `json:"all_models_loaded"`
			}
			if json.Unmarshal(raw, &health) == nil {
				for _, m := range health.AllModelsLoaded {
					if m.ModelName == l.spec.Model {
						if m.MaxContextWindow > 0 {
							ctxWindow = m.MaxContextWindow
						} else if m.RecipeOptions.CtxSize > 0 {
							ctxWindow = m.RecipeOptions.CtxSize
						}
						break
					}
				}
			}
		}
	}

	if !sawServerResponse {
		l.ctxCache.set(l.spec.Model, lemonadeContextEntry{
			fetchedAt: now,
			failed:    true,
		})
		return 0
	}

	l.ctxCache.set(l.spec.Model, lemonadeContextEntry{
		contextWindow:   ctxWindow,
		maxOutputTokens: maxOut,
		fetchedAt:       now,
		failed:          false,
	})
	return ctxWindow
}

func (l *LemonadeProvider) probeMetaGET(ctx context.Context, fullURL string) (int, []byte, error) {
	req, err := l.newRequest(ctx, http.MethodGet, fullURL, "application/json", nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := l.metaClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

type lemonadeStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type lemonadeChatRequest struct {
	Model           string                 `json:"model"`
	Messages        []ocMessage            `json:"messages"`
	Tools           []ocTool               `json:"tools,omitempty"`
	Stream          bool                   `json:"stream"`
	StreamOptions   *lemonadeStreamOptions `json:"stream_options,omitempty"`
	ReasoningEffort string                 `json:"reasoning_effort,omitempty"`
	EnableThinking  *bool                  `json:"enable_thinking,omitempty"`
}

func (l *LemonadeProvider) buildPayload(stream bool, messages []Message, defs []tools.Definition) ([]byte, error) {
	req := lemonadeChatRequest{
		Model:           l.spec.Model,
		Messages:        toOCMessages(messages),
		Tools:           toOCTools(defs),
		Stream:          stream,
		ReasoningEffort: l.reasoning.Effort,
	}
	if stream {
		req.StreamOptions = &lemonadeStreamOptions{IncludeUsage: true}
	}
	if l.reasoning.Thinking != nil && l.reasoning.Thinking.Enabled != nil {
		v := *l.reasoning.Thinking.Enabled
		req.EnableThinking = &v
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return payload, nil
}

type lemonadeUsagePayload struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type lemonadeTimingsPayload struct {
	CacheN             int     `json:"cache_n"`
	PromptN            int     `json:"prompt_n"`
	PromptMS           float64 `json:"prompt_ms"`
	PromptPerSecond    float64 `json:"prompt_per_second"`
	PredictedN         int     `json:"predicted_n"`
	PredictedMS        float64 `json:"predicted_ms"`
	PredictedPerSecond float64 `json:"predicted_per_second"`
}

type lemonadeStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			Reasoning        string            `json:"reasoning"`
			ToolCalls        []ocToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage   *lemonadeUsagePayload   `json:"usage"`
	Timings *lemonadeTimingsPayload `json:"timings"`
	Error   struct {
		Message string `json:"message"`
	} `json:"error"`
}

func buildLemonadeUsage(u *lemonadeUsagePayload, t *lemonadeTimingsPayload) *Usage {
	if u == nil && t == nil {
		return nil
	}
	out := &Usage{}
	if u != nil {
		out.PromptTokens = u.PromptTokens
		out.CompletionTokens = u.CompletionTokens
		out.TotalTokens = u.TotalTokens
		if u.PromptTokensDetails != nil {
			out.CachedTokens = u.PromptTokensDetails.CachedTokens
		}
	}
	if t != nil {
		out.Timings = TurnTimings{
			CacheN:             t.CacheN,
			PromptN:            t.PromptN,
			PromptMS:           t.PromptMS,
			PromptPerSecond:    t.PromptPerSecond,
			PredictedN:         t.PredictedN,
			PredictedMS:        t.PredictedMS,
			PredictedPerSecond: t.PredictedPerSecond,
		}
	}
	return out
}

func mergeLemonadeUsage(existing, next *Usage) *Usage {
	if next == nil {
		return existing
	}
	if existing == nil {
		return next
	}
	merged := *existing
	if next.PromptTokens != 0 {
		merged.PromptTokens = next.PromptTokens
	}
	if next.CompletionTokens != 0 {
		merged.CompletionTokens = next.CompletionTokens
	}
	if next.TotalTokens != 0 {
		merged.TotalTokens = next.TotalTokens
	}
	if next.CachedTokens != 0 {
		merged.CachedTokens = next.CachedTokens
	}
	if next.Timings != (TurnTimings{}) {
		merged.Timings = next.Timings
	}
	return &merged
}

// StreamChat sends the conversation and streams StreamEvent values, draining
// until the terminal [DONE] frame so Lemonade's trailing usage and timings chunk
// (emitted after finish_reason with empty choices) is never lost.
func (l *LemonadeProvider) StreamChat(ctx context.Context, messages []Message, defs []tools.Definition) (<-chan StreamEvent, error) {
	caps := l.Capabilities()
	if !caps.Streaming {
		return nil, unsupportedCapability(l.displayName(), "streaming", "providers.<id>.capabilities.streaming")
	}
	if !caps.ToolCalling && len(defs) > 0 {
		return nil, unsupportedCapability(l.displayName(), "tool calling", "providers.<id>.capabilities.tool_calling")
	}
	body, err := l.buildPayload(true, messages, defs)
	if err != nil {
		return nil, err
	}

	lease, err := l.acquireInferenceTurn(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := l.doWithGate(ctx, http.MethodPost, l.ChatPath(), "text/event-stream", body)
	if err != nil {
		lease.Release()
		l.gate.release()
		return nil, err
	}

	events := make(chan StreamEvent, 32)

	go func() {
		defer l.gate.release()
		defer lease.Release()
		defer resp.Body.Close()
		defer close(events)

		send := func(event StreamEvent) bool {
			select {
			case events <- event:
				return true
			case <-ctx.Done():
				return false
			}
		}

		pending := map[int]*ocToolCallBuf{}
		order := []int{}
		var usage *Usage
		var lastFinish FinishReason

		finishTurn := func(reason FinishReason) {
			if len(order) > 0 {
				calls, err := finalizeOCToolCalls(pending, order)
				if err != nil {
					send(StreamEvent{Err: &protocolError{msg: "decode tool call arguments: " + err.Error()}})
					return
				}
				if !send(StreamEvent{ToolCalls: calls}) {
					return
				}
			}
			if waitMS := lease.WaitMS(); waitMS > 0 {
				if usage == nil {
					usage = &Usage{}
				} else {
					uCopy := *usage
					usage = &uCopy
				}
				usage.Timings.SlotWaitMS = waitMS
			}
			send(StreamEvent{Done: true, StopReason: reason, Usage: usage})
		}

		streamErr := sseReader(ctx, resp.Body, func(data string) error {
			if data == "[DONE]" {
				return errSSEDone
			}

			var chunk lemonadeStreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return &protocolError{msg: fmt.Sprintf("decode stream chunk: %v", err)}
			}
			if chunk.Error.Message != "" {
				return errors.New(chunk.Error.Message)
			}

			if nextUsage := buildLemonadeUsage(chunk.Usage, chunk.Timings); nextUsage != nil {
				usage = mergeLemonadeUsage(usage, nextUsage)
			}

			if len(chunk.Choices) == 0 {
				return nil
			}
			choice := chunk.Choices[0]

			if reasoning := choice.Delta.ReasoningContent; reasoning != "" {
				if !send(StreamEvent{Thinking: reasoning}) {
					return errSendStopped
				}
			} else if reasoning := choice.Delta.Reasoning; reasoning != "" {
				if !send(StreamEvent{Thinking: reasoning}) {
					return errSendStopped
				}
			}

			if choice.Delta.Content != "" {
				if !send(StreamEvent{Text: choice.Delta.Content}) {
					return errSendStopped
				}
			}

			for _, tc := range choice.Delta.ToolCalls {
				buf, ok := pending[tc.Index]
				if !ok {
					buf = &ocToolCallBuf{}
					pending[tc.Index] = buf
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					buf.ID += tc.ID
				}
				if tc.Function.Name != "" {
					buf.Name += tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					buf.ArgsBuf += tc.Function.Arguments
				}
			}

			// Record finish_reason but keep reading so trailing empty-choices
			// usage/timings chunks arrive before [DONE].
			if choice.FinishReason != "" {
				lastFinish = finishReasonFor(choice.FinishReason)
			}
			return nil
		})

		switch {
		case errors.Is(streamErr, errSSEDone):
			if lastFinish == FinishNone {
				lastFinish = FinishStop
			}
			finishTurn(lastFinish)
			return
		case errors.Is(streamErr, errSendStopped):
			return
		case streamErr != nil:
			if ctx.Err() != nil {
				return
			}
			send(StreamEvent{Err: streamErr})
			return
		default:
			send(StreamEvent{Err: &protocolError{msg: "stream ended without a terminal marker ([DONE]); response is incomplete"}})
		}
	}()

	return events, nil
}

// Complete performs one non-streaming completion against Lemonade.
func (l *LemonadeProvider) Complete(ctx context.Context, messages []Message, defs []tools.Definition) (Response, error) {
	if caps := l.Capabilities(); !caps.ToolCalling && len(defs) > 0 {
		return Response{}, unsupportedCapability(l.displayName(), "tool calling", "providers.<id>.capabilities.tool_calling")
	}
	body, err := l.buildPayload(false, messages, defs)
	if err != nil {
		return Response{}, err
	}

	lease, err := l.acquireInferenceTurn(ctx)
	if err != nil {
		return Response{}, err
	}
	resp, err := l.doWithGate(ctx, http.MethodPost, l.ChatPath(), "application/json", body)
	if err != nil {
		lease.Release()
		l.gate.release()
		return Response{}, err
	}
	defer l.gate.release()
	defer lease.Release()
	defer resp.Body.Close()

	var out struct {
		Choices []struct {
			Message struct {
				Content   string       `json:"content"`
				ToolCalls []ocToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage   *lemonadeUsagePayload   `json:"usage"`
		Timings *lemonadeTimingsPayload `json:"timings"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return Response{}, &protocolError{msg: fmt.Sprintf("decode response body: %v", err)}
	}
	if out.Error.Message != "" {
		return Response{}, errors.New(out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return Response{}, &protocolError{msg: "response contained no choices"}
	}

	response := Response{Content: out.Choices[0].Message.Content}
	for _, tc := range out.Choices[0].Message.ToolCalls {
		args := map[string]any{}
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return Response{}, &protocolError{msg: "decode tool call arguments: " + err.Error()}
			}
		}
		response.ToolCalls = append(response.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: args})
	}
	if u := buildLemonadeUsage(out.Usage, out.Timings); u != nil {
		response.Usage = *u
	}
	if waitMS := lease.WaitMS(); waitMS > 0 {
		response.Usage.Timings.SlotWaitMS = waitMS
	}
	response.StopReason = finishReasonFor(out.Choices[0].FinishReason)
	return response, nil
}

// ListModels enumerates downloaded models exposed by Lemonade's /models endpoint
// and extracts reported context windows so /model picker and ContextBudget can
// display accurate token limits.
func (l *LemonadeProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	resp, err := l.do(ctx, http.MethodGet, "/models?show_all=true", "application/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	l.gate.release()

	var out struct {
		Data []struct {
			ID                  string   `json:"id"`
			Checkpoint          string   `json:"checkpoint"`
			Recipe              string   `json:"recipe"`
			Downloaded          *bool    `json:"downloaded"`
			Labels              []string `json:"labels"`
			ContextLength       int64    `json:"context_length"`
			MaxContextWindow    int64    `json:"max_context_window"`
			MaxOutputTokens     int      `json:"max_output_tokens"`
			MaxCompletionTokens int      `json:"max_completion_tokens"`
		} `json:"data"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return nil, &protocolError{msg: fmt.Sprintf("decode model list: %v", err)}
	}
	if out.Error.Message != "" {
		return nil, errors.New(out.Error.Message)
	}

	models := make([]ModelInfo, 0, len(out.Data))
	now := l.now()
	for _, m := range out.Data {
		if m.ID == "" {
			continue
		}
		if m.Downloaded != nil && !*m.Downloaded {
			continue
		}
		ctxWin := m.ContextLength
		if ctxWin <= 0 {
			ctxWin = m.MaxContextWindow
		}
		maxOut := m.MaxOutputTokens
		if maxOut <= 0 {
			maxOut = m.MaxCompletionTokens
		}
		if ctxWin > 0 {
			l.ctxCache.set(m.ID, lemonadeContextEntry{
				contextWindow:   int(ctxWin),
				maxOutputTokens: maxOut,
				fetchedAt:       now,
				failed:          false,
			})
		}
		models = append(models, ModelInfo{
			Name:          m.ID,
			ID:            m.ID,
			ContextWindow: ctxWin,
		})
	}
	return models, nil
}
