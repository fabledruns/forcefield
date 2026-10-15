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
	"strings"
	"sync"

	"forcefield/internal/tools"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// vertexScope is the OAuth scope used for Vertex AI access tokens. It is
// the cloud-wide scope ADC mints by default for user credentials created
// with `gcloud auth application-default login`.
const vertexScope = "https://www.googleapis.com/auth/cloud-platform"

// VertexProvider talks to Google Cloud Vertex AI's Generative AI API
// (GenerateContent / streamGenerateContent) using Application Default
// Credentials. A dedicated adapter is warranted: unlike the Gemini
// developer API (API key against generativelanguage.googleapis.com),
// Vertex AI addresses regional endpoints per project/location and
// authenticates with short-lived OAuth tokens from the ADC chain.
//
// The GenerateContent wire format is identical to Gemini's, so request
// building and stream parsing reuse the shared GenAI helpers in this
// package; only endpoint construction, credentials, and error wording
// are Vertex-specific.
type VertexProvider struct {
	spec   Spec
	client *http.Client
	retry  retryPolicy
	gate   *requestGate

	reasoning ReasoningConfig

	// tokenFunc overrides ADC token resolution. It is nil in production
	// and set by tests to avoid touching real credentials.
	tokenFunc func(ctx context.Context) (string, error)

	mu sync.Mutex
	ts oauth2.TokenSource
}

// NewVertexProvider builds a provider from a resolved spec. The regional
// endpoint comes from config resolution (derived from location unless an
// explicit base_url overrides it); the resource path is appended here.
func NewVertexProvider(spec Spec) *VertexProvider {
	spec.BaseURL = strings.TrimRight(spec.BaseURL, "/")
	client := newDefaultClient()
	if spec.TimeoutSeconds > 0 {
		client = clientForTimeout(spec.TimeoutSeconds)
	}
	return &VertexProvider{
		spec:   spec,
		client: client,
		retry:  defaultRetryPolicy,
		gate:   newRequestGate(),
	}
}

// displayName is the service name used in error messages.
func (v *VertexProvider) displayName() string {
	if v.spec.Label != "" {
		return v.spec.Label
	}
	return "Google Vertex AI"
}

// Capabilities reports what this transport supports.
func (v *VertexProvider) Capabilities() Capabilities {
	base := Capabilities{
		Streaming:         true,
		ToolCalling:       true,
		Reasoning:         true,
		ParallelToolCalls: true,
		MaxOutputTokens:   8192,
	}
	return v.spec.CapabilitiesOverride.Apply(base)
}

// SetReasoning stores the abstract reasoning config for the next request.
func (v *VertexProvider) SetReasoning(cfg ReasoningConfig) {
	deep := ReasoningConfig{Effort: cfg.Effort}
	if cfg.Thinking != nil {
		tc := ThinkingConfig{Level: cfg.Thinking.Level}
		if cfg.Thinking.Enabled != nil {
			enabled := *cfg.Thinking.Enabled
			tc.Enabled = &enabled
		}
		if cfg.Thinking.Budget != nil {
			budget := *cfg.Thinking.Budget
			tc.Budget = &budget
		}
		deep.Thinking = &tc
	}
	v.reasoning = deep
}

// GetReasoning reports the last reasoning config set.
func (v *VertexProvider) GetReasoning() ReasoningConfig {
	deep := ReasoningConfig{Effort: v.reasoning.Effort}
	if v.reasoning.Thinking != nil {
		tc := ThinkingConfig{Level: v.reasoning.Thinking.Level}
		if v.reasoning.Thinking.Enabled != nil {
			enabled := *v.reasoning.Thinking.Enabled
			tc.Enabled = &enabled
		}
		if v.reasoning.Thinking.Budget != nil {
			budget := *v.reasoning.Thinking.Budget
			tc.Budget = &budget
		}
		deep.Thinking = &tc
	}
	return deep
}

// statusHint turns specific HTTP statuses into a concrete next step.
func (v *VertexProvider) statusHint(status int, _ string) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "Vertex AI rejected the credentials - run `gcloud auth application-default login`, set GOOGLE_APPLICATION_CREDENTIALS to a service-account key file, or run on GCP with a service account that has the Vertex AI User role; also verify the project has the Vertex AI API enabled"
	case http.StatusNotFound:
		return fmt.Sprintf("model %q was not found in project %q location %q - verify the model ID and that it is available in that region", v.spec.Model, v.spec.Project, v.spec.Location)
	case http.StatusBadRequest:
		return "the request was rejected as malformed - if this persists the model may not support one of the requested features (e.g. function calling)"
	default:
		return ""
	}
}

func (v *VertexProvider) wrapTransport(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || Classify(err) == ErrKindTimeout {
		return fmt.Errorf("request to %s at %s timed out waiting for response headers: %w", v.displayName(), v.spec.BaseURL, err)
	}
	return fmt.Errorf("could not reach %s at %s: %w", v.displayName(), v.spec.BaseURL, err)
}

// token resolves an OAuth access token via ADC (or the test seam). Tokens
// are short-lived and cached by the oauth2 TokenSource, which refreshes
// them automatically. Errors never include credential material.
func (v *VertexProvider) token(ctx context.Context) (string, error) {
	if v.tokenFunc != nil {
		tok, err := v.tokenFunc(ctx)
		if err != nil {
			return "", v.authError(err)
		}
		if !IsValidAPIKey(tok) {
			return "", v.authError(errors.New("resolved token contains invalid control characters"))
		}
		return tok, nil
	}
	v.mu.Lock()
	ts := v.ts
	v.mu.Unlock()
	if ts == nil {
		creds, err := v.adcCredentials(ctx)
		if err != nil {
			return "", err
		}
		ts = creds.TokenSource
		v.mu.Lock()
		v.ts = ts
		v.mu.Unlock()
	}
	tok, err := ts.Token()
	if err != nil {
		return "", v.authError(err)
	}
	if tok == nil || !IsValidAPIKey(tok.AccessToken) {
		return "", v.authError(errors.New("resolved token is missing or invalid"))
	}
	return tok.AccessToken, nil
}

// adcCredentials resolves credentials following the documented ADC chain:
// GOOGLE_APPLICATION_CREDENTIALS, the gcloud well-known file, then the
// GCE metadata server. A per-provider credentials_file overrides the
// chain for this provider only.
func (v *VertexProvider) adcCredentials(ctx context.Context) (*google.Credentials, error) {
	if file := strings.TrimSpace(v.spec.CredentialsFile); file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, v.authError(fmt.Errorf("read credentials_file %s: %w", file, err))
		}
		creds, err := google.CredentialsFromJSON(ctx, data, vertexScope)
		if err != nil {
			return nil, v.authError(fmt.Errorf("parse credentials_file %s: %w", file, err))
		}
		return creds, nil
	}
	creds, err := google.FindDefaultCredentials(ctx, vertexScope)
	if err != nil {
		return nil, v.authError(err)
	}
	return creds, nil
}

// authError normalizes an ADC/token failure as a non-retryable auth error
// with setup guidance. The wrapped error carries file paths and library
// messages only, never key material.
func (v *VertexProvider) authError(err error) error {
	return &statusError{
		Provider: v.displayName(),
		Model:    v.spec.Model,
		Status:   http.StatusUnauthorized,
		Body:     err.Error(),
		Kind:     ErrKindAuth,
		Hint:     "Vertex AI needs Application Default Credentials - run `gcloud auth application-default login`, set GOOGLE_APPLICATION_CREDENTIALS to a service-account key file, or use providers.<id>.credentials_file for this provider only",
	}
}

// resourcePath builds the versioned model resource path for generate and
// stream calls. Models given as full resource names (tuned endpoints)
// are used verbatim.
func (v *VertexProvider) resourcePath() (string, error) {
	if strings.TrimSpace(v.spec.Project) == "" {
		return "", fmt.Errorf("provider %q has no project - set providers.%s.project (or GOOGLE_CLOUD_PROJECT)", v.spec.ID, v.spec.ID)
	}
	if strings.TrimSpace(v.spec.Location) == "" {
		return "", fmt.Errorf("provider %q has no location - set providers.%s.location (e.g. us-central1)", v.spec.ID, v.spec.ID)
	}
	model := strings.TrimSpace(v.spec.Model)
	if model == "" {
		return "", fmt.Errorf("provider %q has no model - set model.name", v.spec.ID)
	}
	if strings.HasPrefix(model, "projects/") {
		return "/v1/" + strings.Trim(model, "/"), nil
	}
	return "/v1/projects/" + url.PathEscape(v.spec.Project) +
		"/locations/" + url.PathEscape(v.spec.Location) +
		"/publishers/google/models/" + url.PathEscape(model), nil
}

func (v *VertexProvider) streamPath() (string, error) {
	prefix, err := v.resourcePath()
	if err != nil {
		return "", err
	}
	return prefix + ":streamGenerateContent?alt=sse", nil
}

func (v *VertexProvider) generatePath() (string, error) {
	prefix, err := v.resourcePath()
	if err != nil {
		return "", err
	}
	return prefix + ":generateContent", nil
}

func (v *VertexProvider) newRequest(ctx context.Context, method, target, accept string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("build request to %s: %w", target, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	tok, err := v.token(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	for name, value := range v.spec.Headers {
		if !IsValidHeaderValue(value) {
			continue
		}
		req.Header.Set(name, value)
	}
	return req, nil
}

func (v *VertexProvider) redact(err error) error {
	// Tokens are short-lived and never echoed by the API, but redact the
	// configured custom headers' values defensively is out of scope:
	// headers are operator config, never secrets by convention.
	return annotateStatusHint(err, v.statusHint)
}

// do performs the HTTP round trip up to (and including) the status check,
// with retry policy applied to transient rate limits. The caller owns the
// returned 200 response body.
func (v *VertexProvider) do(ctx context.Context, method, pathAndQuery, accept string, body []byte) (*http.Response, error) {
	if err := v.gate.acquire(); err != nil {
		return nil, err
	}
	buildRequest := func() (*http.Request, error) {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		return v.newRequest(ctx, method, v.spec.BaseURL+pathAndQuery, accept, reader)
	}

	resp, err := doWithRetry(ctx, v.client, v.retry, v.displayName(), v.spec.Model, buildRequest, v.wrapTransport)
	if err != nil {
		v.gate.release()
		return nil, v.redact(err)
	}
	return resp, nil
}

// StreamChat sends the conversation and streams the response as
// StreamEvent values.
func (v *VertexProvider) StreamChat(ctx context.Context, messages []Message, defs []tools.Definition) (<-chan StreamEvent, error) {
	body, err := buildGenAIContentPayload(v.reasoning, true, messages, defs)
	if err != nil {
		return nil, err
	}

	path, err := v.streamPath()
	if err != nil {
		return nil, err
	}
	resp, err := v.do(ctx, http.MethodPost, path, "text/event-stream", body)
	if err != nil {
		return nil, err
	}

	// Buffered so the network reader stays ahead of the consumer by a
	// few dozen events instead of rendezvousing per token. Order is
	// unchanged (single producer); cancellation still preempts via ctx.
	events := make(chan StreamEvent, 32)

	go func() {
		defer resp.Body.Close()
		defer close(events)
		defer v.gate.release()

		send := func(event StreamEvent) bool {
			select {
			case events <- event:
				return true
			case <-ctx.Done():
				return false
			}
		}

		// Function calls accumulate across chunks until the stream ends;
		// they are delivered as one batched ToolCalls event like every
		// other adapter.
		var pendingCalls []ToolCall
		var usage Usage
		var stop FinishReason

		finishTurn := func() {
			if len(pendingCalls) > 0 {
				if !send(StreamEvent{ToolCalls: pendingCalls}) {
					return
				}
			}
			send(StreamEvent{Done: true, StopReason: stop, Usage: &usage})
		}

		streamErr := sseReader(ctx, resp.Body, func(data string) error {
			var chunk geminiResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return &protocolError{msg: fmt.Sprintf("decode stream chunk: %v", err)}
			}
			if chunk.Error != nil && chunk.Error.Message != "" {
				status := chunk.Error.Status
				if status != "" {
					return fmt.Errorf("%s (%s)", chunk.Error.Message, status)
				}
				return errors.New(chunk.Error.Message)
			}

			usage.PromptTokens = chunk.UsageMetadata.PromptTokenCount
			usage.CompletionTokens = chunk.UsageMetadata.CandidatesTokenCount
			usage.TotalTokens = chunk.UsageMetadata.TotalTokenCount

			for _, cand := range chunk.Candidates {
				if reason := geminiFinishReasonFor(cand.FinishReason); reason != FinishNone {
					stop = reason
				}
				for _, part := range cand.Content.Parts {
					switch {
					case part.FunctionCall != nil:
						args := part.FunctionCall.Args
						if args == nil {
							args = map[string]any{}
						}
						pendingCalls = append(pendingCalls, ToolCall{
							ID:        nextSyntheticCallID(),
							Name:      part.FunctionCall.Name,
							Arguments: args,
						})
					default: // text part
						if part.Text != "" && !send(StreamEvent{Text: part.Text}) {
							return errSendStopped
						}
					}
				}
			}
			return nil
		})

		switch {
		case errors.Is(streamErr, errSendStopped):
			return
		case streamErr != nil:
			if ctx.Err() != nil {
				return
			}
			send(StreamEvent{Err: streamErr})
			return
		default:
			// Stream ended without a finish reason (EOF): the turn is
			// truncated, not complete. Report it so the runtime never
			// commits partial content as a finished turn.
			if stop == FinishNone {
				send(StreamEvent{Err: &protocolError{msg: "stream ended without a terminal marker (finishReason); response is incomplete"}})
				return
			}
			finishTurn()
		}
	}()

	return events, nil
}

// Complete performs one non-streaming completion.
func (v *VertexProvider) Complete(ctx context.Context, messages []Message, defs []tools.Definition) (Response, error) {
	body, err := buildGenAIContentPayload(v.reasoning, false, messages, defs)
	if err != nil {
		return Response{}, err
	}

	path, err := v.generatePath()
	if err != nil {
		return Response{}, err
	}
	resp, err := v.do(ctx, http.MethodPost, path, "application/json", body)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	v.gate.release() // single-flight covers the exchange only; the body is read here

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Response{}, fmt.Errorf("read response body: %w", err)
	}

	var out geminiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return Response{}, &protocolError{msg: fmt.Sprintf("decode response body: %v", err)}
	}
	if out.Error != nil && out.Error.Message != "" {
		status := out.Error.Status
		if status != "" {
			return Response{}, fmt.Errorf("%s (%s)", out.Error.Message, status)
		}
		return Response{}, errors.New(out.Error.Message)
	}
	if len(out.Candidates) == 0 {
		return Response{}, &protocolError{msg: "response contained no candidates"}
	}

	candidate := out.Candidates[0]
	response := Response{}
	var calls []ToolCall
	for _, part := range candidate.Content.Parts {
		if part.FunctionCall != nil {
			args := part.FunctionCall.Args
			if args == nil {
				args = map[string]any{}
			}
			calls = append(calls, ToolCall{
				ID:        nextSyntheticCallID(),
				Name:      part.FunctionCall.Name,
				Arguments: args,
			})
			continue
		}
		response.Content += part.Text
	}
	response.ToolCalls = calls
	response.Usage = Usage{
		PromptTokens:     out.UsageMetadata.PromptTokenCount,
		CompletionTokens: out.UsageMetadata.CandidatesTokenCount,
		TotalTokens:      out.UsageMetadata.TotalTokenCount,
	}
	response.StopReason = geminiFinishReasonFor(candidate.FinishReason)
	if response.StopReason == FinishNone {
		response.StopReason = FinishStop
	}
	return response, nil
}
