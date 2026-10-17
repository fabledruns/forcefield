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

// OpenAICompatible is the generic Chat Completions adapter for all compatible
// services (config differs; protocol shared). Normalizes vendor quirks.
// See docs/Providers.md.
type OpenAICompatible struct {
	spec   Spec
	client *http.Client
	retry  retryPolicy
	gate   *requestGate

	// extraBody entries are merged into every request body. Presets use
	// this for service-specific fields (e.g. NVIDIA's
	// chat_template_kwargs) without branching the shared code paths.
	extraBody map[string]any

	// authHintEnv optionally names the environment variable users should
	// set when authentication fails; it appears verbatim in error hints.
	authHintEnv string

	// noKeyHint optionally replaces the default "set an API key" hint for
	// services that work without one (e.g. LM Studio), where a 401 means
	// something else went wrong.
	noKeyHint string

	// hintFor optionally replaces the generic status hints with ones
	// specific to a protocol-identical deployment-scoped service (Azure).
	// Nil keeps the built-in hints; nothing else observes the difference.
	hintFor func(status int, body string) string

	reasoning ReasoningConfig
}

// NewOpenAICompatible builds a provider for the given resolved spec.
func NewOpenAICompatible(spec Spec) *OpenAICompatible {
	if spec.BaseURL != "" {
		spec.BaseURL = strings.TrimRight(spec.BaseURL, "/")
	}
	p := &OpenAICompatible{
		spec:   spec,
		client: newDefaultClient(),
		retry:  defaultRetryPolicy,
		gate:   newRequestGate(),
	}
	// For NVIDIA models that support thinking, enable reasoning streaming by
	// default to preserve existing behavior for those models (e.g., z-ai/glm-5.2).
	// For models where thinking is not a separate capability (DeepSeek, Muse),
	// do not send the field.
	if isNvidiaSpec(spec) && ModelReasoningCapabilities(spec.ID, spec.Model).SupportsThinking() {
		p.extraBody = map[string]any{
			"chat_template_kwargs": map[string]any{
				"enable_thinking": true,
				"clear_thinking":  false,
			},
		}
	}
	// Timeout precedence: explicit timeout_seconds wins; otherwise Kimi K3
	// on NIM keeps its longer header bound for slow reasoning TTFB.
	// Kimi K3 on NIM can be very slow to start streaming (TTFB) due to
	// reasoning; give it a longer header timeout so a valid slow response
	// is not timed out and mis-reported as unreachable.
	if spec.TimeoutSeconds > 0 {
		p.client = clientForTimeout(spec.TimeoutSeconds)
	} else if isNvidiaSpec(spec) && strings.Contains(strings.ToLower(spec.Model), "kimi") {
		p.client = &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: defaultNvidiaTimeout,
			},
			CheckRedirect: safeRedirectCheck,
		}
	}
	return p
}

// displayName is the service name used in error messages.
func (o *OpenAICompatible) displayName() string {
	if o.spec.Label != "" {
		return o.spec.Label
	}
	return o.spec.ID
}

// Capabilities reports what this transport supports.
func (o *OpenAICompatible) Capabilities() Capabilities {
	base := Capabilities{
		Streaming:       true,
		ToolCalling:     true,
		Reasoning:       true,
		MaxOutputTokens: 4096,
	}
	return o.spec.CapabilitiesOverride.Apply(base)
}

// ChatPath returns the configured chat-completions path or the default.
func (o *OpenAICompatible) ChatPath() string {
	return o.spec.EffectiveChatPath()
}

// applyAuth stamps the configured authentication onto req. Modes:
// "" (auto: bearer when a key resolves), "bearer", "none", "header".
// It fails closed on a key that cannot be sent safely instead of
// forwarding a header-injection vector. Errors never include the key.
func (o *OpenAICompatible) applyAuth(req *http.Request) error {
	mode := o.spec.NormalizedAuthMode()
	key := o.spec.APIKey
	switch mode {
	case "none":
		return nil
	case "header":
		if key == "" {
			return nil
		}
		if !IsValidAPIKey(key) {
			return invalidAPIKeyError(o.spec.ID, "")
		}
		name := o.spec.AuthHeader
		if name == "" {
			return nil
		}
		value := key
		if o.spec.AuthScheme != "" {
			value = o.spec.AuthScheme + " " + key
		}
		req.Header.Set(name, value)
		return nil
	default: // "", "bearer"
		if key == "" {
			return nil
		}
		if !IsValidAPIKey(key) {
			return invalidAPIKeyError(o.spec.ID, "")
		}
		scheme := "Bearer"
		if o.spec.AuthScheme != "" {
			scheme = o.spec.AuthScheme
		}
		req.Header.Set("Authorization", scheme+" "+key)
		return nil
	}
}

// SetReasoning stores the abstract reasoning config for the next request.
func (o *OpenAICompatible) SetReasoning(cfg ReasoningConfig) {
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
	o.reasoning = deep
}

// GetReasoning reports the last reasoning config set.
func (o *OpenAICompatible) GetReasoning() ReasoningConfig {
	deep := ReasoningConfig{Effort: o.reasoning.Effort}
	if o.reasoning.Thinking != nil {
		tc := ThinkingConfig{Level: o.reasoning.Thinking.Level}
		if o.reasoning.Thinking.Enabled != nil {
			v := *o.reasoning.Thinking.Enabled
			tc.Enabled = &v
		}
		if o.reasoning.Thinking.Budget != nil {
			v := *o.reasoning.Thinking.Budget
			tc.Budget = &v
		}
		deep.Thinking = &tc
	}
	return deep
}

// statusHint turns specific HTTP statuses into a concrete next step. An
// adapter-provided hintFor takes precedence when set (Azure).
func (o *OpenAICompatible) statusHint(status int, body string) string {
	if o.hintFor != nil {
		return o.hintFor(status, body)
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		if o.spec.APIKey == "" {
			if o.noKeyHint != "" {
				return o.noKeyHint
			}
			env := o.authHintEnv
			if env == "" {
				env = "the provider's API key environment variable"
			} else {
				env = fmt.Sprintf("the %s environment variable", env)
			}
			return fmt.Sprintf("no API key is configured - set %s (or api_key_env in config.yaml) and restart Forcefield", env)
		}
		if o.authHintEnv != "" {
			return fmt.Sprintf("check that %s is valid and that this account can access this model", o.authHintEnv)
		}
		return "check that the configured API key is valid and authorized for this model"
	case http.StatusNotFound:
		return fmt.Sprintf("model %q was not found on %s - pick another model with /model or set model.name in config.yaml",
			o.spec.Model, o.displayName())
	default:
		return ""
	}
}

func (o *OpenAICompatible) wrapTransport(err error) error {
	// Distinguish a header timeout (slow TTFB) from a true unreachable
	// peer so a valid slow provider response is not mis-reported as
	// "could not reach" and incorrectly classified as ErrKindConnection.
	if errors.Is(err, context.DeadlineExceeded) || Classify(err) == ErrKindTimeout {
		return fmt.Errorf("request to %s at %s timed out waiting for response headers: %w", o.displayName(), o.spec.BaseURL, err)
	}
	return fmt.Errorf("could not reach %s at %s: %w", o.displayName(), o.spec.BaseURL, err)
}

// ocMessage mirrors one message of the OpenAI chat completions protocol.
type ocMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`

	ToolCalls []ocToolCall `json:"tool_calls,omitempty"`

	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

type ocTool struct {
	Type     string         `json:"type"`
	Function ocFunctionSpec `json:"function"`
}

type ocFunctionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type ocToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type,omitempty"`
	Function ocFunctionCallData `json:"function"`
}

type ocFunctionCallData struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ocChatRequest struct {
	Model           string      `json:"model"`
	Messages        []ocMessage `json:"messages"`
	Tools           []ocTool    `json:"tools,omitempty"`
	Stream          bool        `json:"stream"`
	ReasoningEffort string      `json:"reasoning_effort,omitempty"`
}

// ocToolCallDelta is one incremental piece of a streamed tool call. Servers
// send the name once and stream arguments as string fragments keyed by
// index; fragments must be concatenated before decoding.
type ocToolCallDelta struct {
	Index    int                `json:"index"`
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function ocFunctionCallData `json:"function"`
}

// ocStreamChunk is the relevant subset of a streamed /chat/completions
// chunk. Reasoning-capable models stream chain of thought separately from
// the answer, in delta.reasoning_content (the spelling NVIDIA documents)
// or delta.reasoning (used by other gateways); both surface as Thinking
// events.
type ocStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string            `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			Reasoning        string            `json:"reasoning"`
			ToolCalls        []ocToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`

	Usage *ocUsage `json:"usage"`

	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

type ocUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u *ocUsage) normalized() *Usage {
	if u == nil {
		return nil
	}
	return &Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

// finishReasonFor maps a protocol finish_reason onto its normalized form.
func finishReasonFor(raw string) FinishReason {
	switch raw {
	case "":
		return FinishNone
	case "tool_calls", "function_call":
		return FinishToolCalls
	case "length":
		return FinishLength
	default:
		return FinishStop
	}
}

// ocToolCallBuf accumulates one streamed tool call until its argument
// fragments are complete.
type ocToolCallBuf struct {
	ID      string
	Name    string
	ArgsBuf string
}

// buildPayload marshals the request and merges any preset-provided extra
// body fields on top.
func (o *OpenAICompatible) buildPayload(stream bool, messages []Message, defs []tools.Definition) ([]byte, error) {
	req := ocChatRequest{
		Model:           o.spec.Model,
		Messages:        toOCMessages(messages),
		Tools:           toOCTools(defs),
		Stream:          stream,
		ReasoningEffort: o.reasoning.Effort,
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	needsMerge := len(o.extraBody) > 0 || (o.reasoning.Thinking != nil && o.reasoning.Thinking.Enabled != nil && isNvidiaSpec(o.spec))
	if !needsMerge {
		return payload, nil
	}

	var merged map[string]any
	if err := json.Unmarshal(payload, &merged); err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	for k, v := range o.extraBody {
		merged[k] = v
	}
	if o.reasoning.Thinking != nil && o.reasoning.Thinking.Enabled != nil && isNvidiaSpec(o.spec) {
		merged["chat_template_kwargs"] = map[string]any{
			"enable_thinking": *o.reasoning.Thinking.Enabled,
			"clear_thinking":  false,
		}
	}
	payload, err = json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return payload, nil
}

func isNvidiaSpec(spec Spec) bool {
	if strings.EqualFold(spec.ID, "nvidia") {
		return true
	}
	if strings.Contains(strings.ToLower(spec.Label), "nvidia") {
		return true
	}
	return false
}

func (o *OpenAICompatible) newRequest(ctx context.Context, method, url, accept string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("build request to %s: %w", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if err := o.applyAuth(req); err != nil {
		return nil, err
	}
	for name, value := range o.spec.Headers {
		// Values were rejected at validation when they contain control
		// characters; skip any that slipped through a programmatic Spec
		// instead of forwarding a header-injection vector.
		if !IsValidHeaderValue(value) {
			continue
		}
		req.Header.Set(name, value)
	}
	return req, nil
}

func (o *OpenAICompatible) redact(err error) error {
	return Redacted(err, o.spec.APIKey)
}

// do performs the HTTP round trip up to (and including) the status check,
// with retry policy applied to transient rate limits. The caller owns the
// returned 200 response body.
func (o *OpenAICompatible) do(ctx context.Context, method, path, accept string, body []byte) (*http.Response, error) {
	if err := o.gate.acquire(); err != nil {
		return nil, err
	}
	buildRequest := func() (*http.Request, error) {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		u := o.spec.BaseURL + path
		// Versioned data-plane APIs (Azure) pin the api-version on
		// every request; the charset is validated at spec time so no
		// extra query parameters can be smuggled in.
		if v := o.spec.APIVersion; v != "" {
			u += "?api-version=" + url.QueryEscape(v)
		}
		return o.newRequest(ctx, method, u, accept, reader)
	}

	resp, err := doWithRetry(ctx, o.client, o.retry, o.displayName(), o.spec.Model, buildRequest, o.wrapTransport)
	if err != nil {
		o.gate.release()
		return nil, o.redact(annotateStatusHint(err, o.statusHint))
	}
	return resp, nil
}

// StreamChat sends the conversation and streams the response as
// StreamEvent values. The channel closes when the turn ends, the context
// is canceled, or an error occurs.
func (o *OpenAICompatible) StreamChat(ctx context.Context, messages []Message, defs []tools.Definition) (<-chan StreamEvent, error) {
	if caps := o.Capabilities(); !caps.Streaming {
		return nil, unsupportedCapability(o.displayName(), "streaming", "providers.<id>.capabilities.streaming")
	}
	if caps := o.Capabilities(); !caps.ToolCalling && len(defs) > 0 {
		return nil, unsupportedCapability(o.displayName(), "tool calling", "providers.<id>.capabilities.tool_calling")
	}
	body, err := o.buildPayload(true, messages, defs)
	if err != nil {
		return nil, err
	}

	resp, err := o.do(ctx, http.MethodPost, o.ChatPath(), "text/event-stream", body)
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
		defer o.gate.release()

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

		// finishTurn flushes any buffered tool calls and completes the
		// stream. Every exit path funnels through here so partially
		// received tool calls are never silently dropped just because the
		// server omitted a "tool_calls" finish_reason - some models end
		// tool-call turns with "stop" or a bare [DONE], which would
		// otherwise look like an empty response.
		finishTurn := func(reason FinishReason) {
			if len(order) > 0 {
				calls, err := finalizeOCToolCalls(pending, order)
				if err != nil {
					err = &protocolError{msg: "decode tool call arguments: " + err.Error()}
					send(StreamEvent{Err: err})
					return
				}
				if !send(StreamEvent{ToolCalls: calls}) {
					return
				}
			}
			send(StreamEvent{Done: true, StopReason: reason, Usage: usage})
		}

		streamErr := sseReader(ctx, resp.Body, func(data string) error {
			if data == "[DONE]" {
				return errSSEDone
			}

			var chunk ocStreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return &protocolError{msg: fmt.Sprintf("decode stream chunk: %v", err)}
			}

			if chunk.Error.Message != "" {
				return errors.New(chunk.Error.Message)
			}

			if u := chunk.Usage.normalized(); u != nil {
				usage = u
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

			if choice.FinishReason != "" {
				// Any finish reason ends the turn; finishTurn decides
				// whether buffered tool calls need to be delivered first.
				lastFinish = finishReasonFor(choice.FinishReason)
				return errSSEDone
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
			// Stream ended without [DONE] or a finish reason (EOF): the
			// turn is truncated, not complete. Report it so the runtime
			// never commits partial content as a finished turn.
			send(StreamEvent{Err: &protocolError{msg: "stream ended without a terminal marker ([DONE] or finish_reason); response is incomplete"}})
		}
	}()

	return events, nil
}

// errSSEDone and errSendStopped are internal control-flow signals for the
// sseReader callback; they never escape the adapter.
var (
	errSSEDone     = errors.New("sse: done")
	errSendStopped = errors.New("sse: receiver stopped")
)

// protocolError marks a malformed response: bodies or stream chunks that
// violate the expected wire format.
type protocolError struct{ msg string }

func (e *protocolError) Error() string { return e.msg }

// capabilityError marks a request for a feature the provider or model has
// explicitly disabled via capabilities configuration.
type capabilityError struct{ msg string }

func (e *capabilityError) Error() string { return e.msg }

// unsupportedCapability builds an ErrKindUnsupportedCapability error naming
// the capability and the config key that controls it.
func unsupportedCapability(provider, capability, configKey string) error {
	return &capabilityError{msg: fmt.Sprintf("%s does not support %s (set %s in config.yaml to change this)", provider, capability, configKey)}
}

// Complete performs one non-streaming completion and returns the full
// response. It exists for callers that want a single synchronous answer;
// the agent loop itself always streams.
func (o *OpenAICompatible) Complete(ctx context.Context, messages []Message, defs []tools.Definition) (Response, error) {
	if caps := o.Capabilities(); !caps.ToolCalling && len(defs) > 0 {
		return Response{}, unsupportedCapability(o.displayName(), "tool calling", "providers.<id>.capabilities.tool_calling")
	}
	body, err := o.buildPayload(false, messages, defs)
	if err != nil {
		return Response{}, err
	}

	resp, err := o.do(ctx, http.MethodPost, o.ChatPath(), "application/json", body)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	o.gate.release() // single-flight covers the exchange only; the body is read here

	var out struct {
		Choices []struct {
			Message struct {
				Content   string       `json:"content"`
				ToolCalls []ocToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *ocUsage `json:"usage"`
		Error struct {
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
	if u := out.Usage.normalized(); u != nil {
		response.Usage = *u
	}
	response.StopReason = finishReasonFor(out.Choices[0].FinishReason)
	return response, nil
}

// maxResponseBytes bounds how much of a non-streaming response body is
// decoded; legitimate completions are far smaller than this.
const maxResponseBytes = 64 << 20

// ListModels enumerates the models the server exposes, when it implements
// the standard GET /models endpoint.
func (o *OpenAICompatible) ListModels(ctx context.Context) ([]ModelInfo, error) {
	resp, err := o.do(ctx, http.MethodGet, "/models", "application/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	o.gate.release()

	var out struct {
		Data []struct {
			ID string `json:"id"`
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
	for _, m := range out.Data {
		if m.ID == "" {
			continue
		}
		models = append(models, ModelInfo{Name: m.ID, ID: m.ID})
	}
	return models, nil
}

func finalizeOCToolCalls(pending map[int]*ocToolCallBuf, order []int) ([]ToolCall, error) {
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

// Helper conversions

func toOCMessages(messages []Message) []ocMessage {
	ocMessages := make([]ocMessage, 0, len(messages))
	for _, msg := range messages {
		var toolCalls []ocToolCall
		if len(msg.ToolCalls) > 0 {
			toolCalls = make([]ocToolCall, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				args, err := json.Marshal(tc.Arguments)
				if err != nil {
					args = []byte("{}")
				}
				toolCalls = append(toolCalls, ocToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: ocFunctionCallData{
						Name:      tc.Name,
						Arguments: string(args),
					},
				})
			}
		}

		ocMessages = append(ocMessages, ocMessage{
			Role:       string(msg.Role),
			Content:    msg.Content,
			ToolCalls:  toolCalls,
			ToolCallID: msg.ToolCallID,
			Name:       msg.Name,
		})
	}
	return ocMessages
}

func toOCTools(defs []tools.Definition) []ocTool {
	ocTools := make([]ocTool, 0, len(defs))
	for _, def := range defs {
		ocTools = append(ocTools, ocTool{
			Type: "function",
			Function: ocFunctionSpec{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  def.InputSchema,
			},
		})
	}
	return ocTools
}
