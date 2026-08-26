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
	"strings"

	"forcefield/internal/tools"
)

// GeminiProvider talks to Google's native Generative Language API. A
// dedicated adapter is warranted because the protocol differs from
// OpenAI's: contents carry typed parts, roles are user/model, system
// prompts are a top-level instruction, tools use functionCall /
// functionResponse parts, and authentication rides a Google-specific
// header.
type GeminiProvider struct {
	spec   Spec
	client *http.Client
	retry  retryPolicy
	gate   *requestGate

	authHintEnv string
}

// NewGeminiProvider builds a provider from a resolved spec. BaseURL is
// the API root (default https://generativelanguage.googleapis.com); the
// versioned path and model method are appended here.
func NewGeminiProvider(spec Spec) *GeminiProvider {
	spec.BaseURL = strings.TrimRight(spec.BaseURL, "/")
	return &GeminiProvider{
		spec:        spec,
		client:      &http.Client{},
		retry:       defaultRetryPolicy,
		gate:        newRequestGate(),
		authHintEnv: "GEMINI_API_KEY",
	}
}

// displayName is the service name used in error messages.
func (g *GeminiProvider) displayName() string {
	if g.spec.Label != "" {
		return g.spec.Label
	}
	return "Google Gemini"
}

// Capabilities reports what this adapter supports.
func (g *GeminiProvider) Capabilities() Capabilities {
	return Capabilities{
		Streaming:         true,
		ToolCalling:       true,
		Reasoning:         true,
		ParallelToolCalls: true,
	}
}

// statusHint turns specific HTTP statuses into a concrete next step.
func (g *GeminiProvider) statusHint(status int, _ string) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		if g.spec.APIKey == "" {
			return fmt.Sprintf("no API key is configured - set the %s environment variable (or api_key_env in config.yaml; GOOGLE_API_KEY works too) and restart Forcefield", g.authHintEnv)
		}
		return fmt.Sprintf("check that the %s value is valid and authorized for this model", g.authHintEnv)
	case http.StatusNotFound:
		return fmt.Sprintf("model %q was not found - pick another model with /model or see https://ai.google.dev/gemini-api/docs/models for available names", g.spec.Model)
	case http.StatusBadRequest:
		return "the request was rejected as malformed - if this persists the model may not support one of the requested features (e.g. function calling)"
	default:
		return ""
	}
}

func (g *GeminiProvider) wrapTransport(err error) error {
	return fmt.Errorf("could not reach %s at %s: %w", g.displayName(), g.spec.BaseURL, err)
}

// geminiPart is one typed piece of a content entry.
type geminiPart struct {
	Text string `json:"text,omitempty"`

	FunctionCall *geminiFunctionCall `json:"functionCall,omitempty"`

	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"` // "user" or "model"
	Parts []geminiPart `json:"parts"`
}

type geminiFunctionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// geminiFinishReasonFor maps a finishReason onto its normalized form.
// Tool-call turns are detected by their functionCall parts, so SAFETY or
// other unusual reasons still end the turn as FinishStop.
func geminiFinishReasonFor(raw string) FinishReason {
	switch raw {
	case "":
		return FinishNone
	case "MAX_TOKENS":
		return FinishLength
	default:
		return FinishStop
	}
}

// buildGeminiContents converts Forcefield history into Generative
// Language form: system messages become the systemInstruction, assistant
// tool calls become model-turn functionCall parts, and tool results
// become user-turn functionResponse parts.
func buildGeminiContents(messages []Message) (system string, out []geminiContent) {
	for _, msg := range messages {
		switch msg.Role {
		case SystemRole:
			if msg.Content != "" {
				system = strings.TrimSpace(system + "\n\n" + msg.Content)
			}
		case AssistantRole:
			parts := make([]geminiPart, 0, len(msg.ToolCalls)+1)
			if msg.Content != "" {
				parts = append(parts, geminiPart{Text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				args := tc.Arguments
				if args == nil {
					args = map[string]any{}
				}
				parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{Name: tc.Name, Args: args}})
			}
			if len(parts) > 0 {
				out = append(out, geminiContent{Role: "model", Parts: parts})
			}
		case ToolRole:
			out = append(out, geminiContent{
				Role: "user",
				Parts: []geminiPart{{FunctionResponse: &geminiFunctionResponse{
					Name:     msg.Name,
					Response: map[string]any{"result": msg.Content},
				}}},
			})
		case UserRole:
			if msg.Content != "" {
				out = append(out, geminiContent{Role: "user", Parts: []geminiPart{{Text: msg.Content}}})
			}
		}
	}
	return strings.TrimSpace(system), out
}

func toGeminiTools(defs []tools.Definition) []map[string]any {
	if len(defs) == 0 {
		return nil
	}
	decls := make([]geminiFunctionDeclaration, 0, len(defs))
	for _, def := range defs {
		schema := def.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		decls = append(decls, geminiFunctionDeclaration{
			Name:        def.Name,
			Description: def.Description,
			Parameters:  schema,
		})
	}
	return []map[string]any{{"functionDeclarations": decls}}
}

func (g *GeminiProvider) newRequest(ctx context.Context, method, target, accept string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("build request to %s: %w", target, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	// The key rides a header rather than the query string so it can never
	// leak into logs that record URLs.
	if g.spec.APIKey != "" {
		req.Header.Set("x-goog-api-key", g.spec.APIKey)
	}
	for name, value := range g.spec.Headers {
		req.Header.Set(name, value)
	}
	return req, nil
}

// do performs the HTTP round trip up to the status check, applying retry
// policy to transient rate limits. The caller owns the returned body.
func (g *GeminiProvider) do(ctx context.Context, method, pathAndQuery, accept string, body []byte) (*http.Response, error) {
	if err := g.gate.acquire(); err != nil {
		return nil, err
	}
	buildRequest := func() (*http.Request, error) {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		return g.newRequest(ctx, method, g.spec.BaseURL+pathAndQuery, accept, reader)
	}

	resp, err := doWithRetry(ctx, g.client, g.retry, g.displayName(), g.spec.Model, buildRequest, g.wrapTransport)
	if err != nil {
		g.gate.release()
		return nil, Redacted(annotateStatusHint(err, g.statusHint), g.spec.APIKey)
	}
	return resp, nil
}

func (g *GeminiProvider) streamPath() string {
	return "/v1beta/models/" + url.PathEscape(g.spec.Model) + ":streamGenerateContent?alt=sse"
}

func (g *GeminiProvider) generatePath() string {
	return "/v1beta/models/" + url.PathEscape(g.spec.Model) + ":generateContent"
}

// buildPayload marshals the request body for either streaming or
// non-streaming calls.
func (g *GeminiProvider) buildPayload(stream bool, messages []Message, defs []tools.Definition) ([]byte, error) {
	system, contents := buildGeminiContents(messages)

	req := map[string]any{
		"contents": contents,
	}
	if system != "" {
		req["systemInstruction"] = geminiContent{Parts: []geminiPart{{Text: system}}}
	}
	if decls := toGeminiTools(defs); decls != nil {
		req["tools"] = decls
	}
	if stream {
		// Keep responses to one candidate; agent loops consume exactly one.
		req["generationConfig"] = map[string]any{"candidateCount": 1}
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return payload, nil
}

// StreamChat sends the conversation and streams the response as
// StreamEvent values.
func (g *GeminiProvider) StreamChat(ctx context.Context, messages []Message, defs []tools.Definition) (<-chan StreamEvent, error) {
	body, err := g.buildPayload(true, messages, defs)
	if err != nil {
		return nil, err
	}

	resp, err := g.do(ctx, http.MethodPost, g.streamPath(), "text/event-stream", body)
	if err != nil {
		return nil, err
	}

	events := make(chan StreamEvent)

	go func() {
		defer resp.Body.Close()
		defer close(events)
		defer g.gate.release()

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
							ID:        fmt.Sprintf("call-%d", len(pendingCalls)+1),
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
			if stop == FinishNone {
				stop = FinishStop
			}
			finishTurn()
		}
	}()

	return events, nil
}

// Complete performs one non-streaming completion.
func (g *GeminiProvider) Complete(ctx context.Context, messages []Message, defs []tools.Definition) (Response, error) {
	body, err := g.buildPayload(false, messages, defs)
	if err != nil {
		return Response{}, err
	}

	resp, err := g.do(ctx, http.MethodPost, g.generatePath(), "application/json", body)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	g.gate.release() // single-flight covers the exchange only; the body is read here

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
				ID:        fmt.Sprintf("call-%d", len(calls)+1),
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

// ListModels enumerates models visible to the API key.
func (g *GeminiProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	resp, err := g.do(ctx, http.MethodGet, "/v1beta/models?pageSize=200", "application/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	g.gate.release()

	var out struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return nil, &protocolError{msg: fmt.Sprintf("decode model list: %v", err)}
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, errors.New(out.Error.Message)
	}

	models := make([]ModelInfo, 0, len(out.Models))
	for _, m := range out.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		if id == "" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = id
		}
		models = append(models, ModelInfo{Name: name, ID: id})
	}
	return models, nil
}
