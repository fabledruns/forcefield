package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"forcefield/internal/tools"
)

// OpenAIResponses talks to any server implementing the OpenAI Responses
// API: POST {baseURL}/responses for turns (SSE) and GET {baseURL}/models
// for discovery. It is a generic protocol adapter, not tied to any single
// service: OpenCode Zen and OpenCode Go serve part of their catalogs over
// this protocol, and any future Responses-compatible endpoint works by
// setting `type: openai-responses` with its own base URL.
//
// Protocol notes the implementation depends on:
//   - Requests are stateless: store:false is always sent and the full
//     conversation is included as input items on every turn. No
//     previous_response_id chaining, so no server-side state accumulates.
//   - Function tools use {"type":"function",...} definitions; results come
//     back as function_call items whose call_id is echoed in
//     function_call_output items. The API's call_id is preserved verbatim
//     as the internal ToolCall ID so multi-turn replay links correctly.
//   - Reasoning arrives as reasoning summary deltas when requested via
//     reasoning:{effort,summary:auto}; raw reasoning text deltas are also
//     surfaced as Thinking when present.
//
// Like the Chat Completions adapter, this code assumes nothing beyond the
// documented protocol: missing usage, truncated streams, split argument
// deltas, and error bodies in several shapes all normalize here.
type OpenAIResponses struct {
	spec   Spec
	client *http.Client
	retry  retryPolicy
	gate   *requestGate

	// authHintEnv optionally names the environment variable users should
	// set when authentication fails; it appears verbatim in error hints.
	authHintEnv string

	reasoning ReasoningConfig
}

// NewOpenAIResponses builds a provider for the given resolved spec.
// BaseURL is the API root (e.g. https://opencode.ai/zen/v1); the
// /responses path is appended here.
func NewOpenAIResponses(spec Spec) *OpenAIResponses {
	if spec.BaseURL != "" {
		spec.BaseURL = strings.TrimRight(spec.BaseURL, "/")
	}
	return &OpenAIResponses{
		spec:   spec,
		client: newDefaultClient(),
		retry:  defaultRetryPolicy,
		gate:   newRequestGate(),
	}
}

// displayName is the service name used in error messages.
func (o *OpenAIResponses) displayName() string {
	if o.spec.Label != "" {
		return o.spec.Label
	}
	return o.spec.ID
}

// Capabilities reports what this transport supports.
func (o *OpenAIResponses) Capabilities() Capabilities {
	return Capabilities{
		Streaming:         true,
		ToolCalling:       true,
		Reasoning:         true,
		ParallelToolCalls: true,
	}
}

// SetReasoning stores the abstract reasoning config for the next request.
func (o *OpenAIResponses) SetReasoning(cfg ReasoningConfig) {
	o.reasoning = ReasoningConfig{Effort: cfg.Effort}
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
		o.reasoning.Thinking = &tc
	}
}

// GetReasoning reports the last reasoning config set.
func (o *OpenAIResponses) GetReasoning() ReasoningConfig {
	out := ReasoningConfig{Effort: o.reasoning.Effort}
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
		out.Thinking = &tc
	}
	return out
}

// statusHint turns specific HTTP statuses into a concrete next step.
func (o *OpenAIResponses) statusHint(status int, _ string) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		if o.spec.APIKey == "" {
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

func (o *OpenAIResponses) wrapTransport(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || Classify(err) == ErrKindTimeout {
		return fmt.Errorf("request to %s at %s timed out waiting for response headers: %w", o.displayName(), o.spec.BaseURL, err)
	}
	return fmt.Errorf("could not reach %s at %s: %w", o.displayName(), o.spec.BaseURL, err)
}

// responseInputItem is one stateless input item. Text messages use the
// role/content shape; prior model tool calls replay as function_call
// items and their results as function_call_output items, linked by the
// API-issued call_id preserved in the internal ToolCall ID.
type responseInputItem struct {
	Type    string `json:"type,omitempty"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	CallID  string `json:"call_id,omitempty"`
	Name    string `json:"name,omitempty"`
	Args    string `json:"arguments,omitempty"`
	Output  string `json:"output,omitempty"`
}

type responseFunctionTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type responseReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type responseRequest struct {
	Model     string                 `json:"model"`
	Input     []responseInputItem    `json:"input"`
	Tools     []responseFunctionTool `json:"tools,omitempty"`
	Stream    bool                   `json:"stream"`
	Store     bool                   `json:"store"`
	Reasoning *responseReasoning     `json:"reasoning,omitempty"`
}

// responseStreamEvent is the relevant subset of a Responses SSE data
// payload. The protocol multiplexes many event types over one stream; only
// text deltas, reasoning deltas, function-call deltas, terminal response
// objects, and errors affect the turn.
type responseStreamEvent struct {
	Type string `json:"type"`

	// Delta events carry one of these depending on type.
	Delta  string              `json:"delta"`
	ItemID string              `json:"item_id"`
	Item   *responseOutputItem `json:"item,omitempty"`

	// Terminal response envelope (response.completed/failed/incomplete).
	Response *responseObject `json:"response,omitempty"`

	// Error payloads (type "error" or embedded error object).
	Error   *responseErrorBody `json:"error,omitempty"`
	Message string             `json:"message,omitempty"`
}

type responseOutputItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responseObject struct {
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details,omitempty"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage,omitempty"`
	Error *responseErrorBody `json:"error,omitempty"`
}

type responseErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// responseToolCallBuf accumulates one streamed function call until its
// argument fragments are complete. Keyed by item id (falling back to the
// output index) so parallel calls assemble independently.
type responseToolCallBuf struct {
	CallID  string
	Name    string
	ArgsBuf string
	emitted bool
}

func (o *OpenAIResponses) newRequest(ctx context.Context, method, url, accept string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("build request to %s: %w", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	// Identify the client plainly; some gateways ask integrations not to
	// hide behind generic library user agents. An explicit config header
	// still wins when set.
	req.Header.Set("User-Agent", "Forcefield")
	if o.spec.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.spec.APIKey)
	}
	for name, value := range o.spec.Headers {
		req.Header.Set(name, value)
	}
	return req, nil
}

func (o *OpenAIResponses) redact(err error) error {
	return Redacted(err, o.spec.APIKey)
}

// do performs the HTTP round trip up to (and including) the status check,
// with retry policy applied to transient rate limits. The caller owns the
// returned 200 response body.
func (o *OpenAIResponses) do(ctx context.Context, method, path, accept string, body []byte) (*http.Response, error) {
	if err := o.gate.acquire(); err != nil {
		return nil, err
	}
	buildRequest := func() (*http.Request, error) {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		return o.newRequest(ctx, method, o.spec.BaseURL+path, accept, reader)
	}

	resp, err := doWithRetry(ctx, o.client, o.retry, o.displayName(), o.spec.Model, buildRequest, o.wrapTransport)
	if err != nil {
		o.gate.release()
		return nil, o.redact(annotateStatusHint(err, o.statusHint))
	}
	return resp, nil
}

// buildPayload marshals a stateless Responses request: full history every
// turn, function tools, and reasoning effort when configured.
func (o *OpenAIResponses) buildPayload(messages []Message, defs []tools.Definition) ([]byte, error) {
	req := responseRequest{
		Model:  o.spec.Model,
		Input:  toResponseInput(messages),
		Tools:  toResponseTools(defs),
		Stream: true,
		Store:  false,
	}
	if strings.TrimSpace(o.reasoning.Effort) != "" {
		req.Reasoning = &responseReasoning{Effort: strings.TrimSpace(o.reasoning.Effort), Summary: "auto"}
	}
	return json.Marshal(req)
}

// StreamChat sends the conversation and streams the response as
// StreamEvent values. The channel closes when the turn ends, the context
// is canceled, or an error occurs.
func (o *OpenAIResponses) StreamChat(ctx context.Context, messages []Message, defs []tools.Definition) (<-chan StreamEvent, error) {
	body, err := o.buildPayload(messages, defs)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	resp, err := o.do(ctx, http.MethodPost, "/responses", "text/event-stream", body)
	if err != nil {
		return nil, err
	}

	events := make(chan StreamEvent)

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

		pending := map[string]*responseToolCallBuf{}
		order := []string{}
		var usage *Usage
		toolCallsSeen := false

		bufFor := func(key string) *responseToolCallBuf {
			buf, ok := pending[key]
			if !ok {
				buf = &responseToolCallBuf{}
				pending[key] = buf
				order = append(order, key)
			}
			return buf
		}

		// finishTurn flushes buffered tool calls (never silently dropped)
		// and completes the stream with the given stop reason.
		finishTurn := func(reason FinishReason) {
			var calls []ToolCall
			for _, key := range order {
				buf := pending[key]
				if buf.emitted || buf.Name == "" {
					continue
				}
				args, err := decodeResponseArgs(buf.ArgsBuf)
				if err != nil {
					send(StreamEvent{Err: &protocolError{msg: "decode tool call arguments: " + err.Error()}})
					return
				}
				buf.emitted = true
				calls = append(calls, ToolCall{ID: buf.CallID, Name: buf.Name, Arguments: args})
			}
			if len(calls) > 0 {
				toolCallsSeen = true
				if !send(StreamEvent{ToolCalls: calls}) {
					return
				}
			}
			if reason == FinishNone {
				if toolCallsSeen {
					reason = FinishToolCalls
				} else {
					reason = FinishStop
				}
			}
			send(StreamEvent{Done: true, StopReason: reason, Usage: usage})
		}

		streamErr := sseReader(ctx, resp.Body, func(data string) error {
			if data == "[DONE]" {
				return errSSEDone
			}

			var ev responseStreamEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				return &protocolError{msg: fmt.Sprintf("decode stream chunk: %v", err)}
			}

			// Protocol-level errors arrive as events, not HTTP statuses.
			if ev.Type == "error" {
				msg := ev.Message
				if ev.Error != nil && ev.Error.Message != "" {
					msg = ev.Error.Message
				}
				if msg == "" {
					msg = "provider reported a stream error"
				}
				return errors.New(msg)
			}

			switch ev.Type {
			case "response.output_text.delta":
				if ev.Delta != "" {
					if !send(StreamEvent{Text: ev.Delta}) {
						return errSendStopped
					}
				}
			case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
				if ev.Delta != "" {
					if !send(StreamEvent{Thinking: ev.Delta}) {
						return errSendStopped
					}
				}
			case "response.output_item.added":
				if ev.Item != nil && ev.Item.Type == "function_call" {
					key := ev.Item.ID
					if key == "" {
						key = ev.ItemID
					}
					if key == "" {
						return &protocolError{msg: "function_call item without id"}
					}
					buf := bufFor(key)
					if ev.Item.CallID != "" {
						buf.CallID = ev.Item.CallID
					}
					if ev.Item.Name != "" {
						buf.Name = ev.Item.Name
					}
				}
			case "response.function_call_arguments.delta":
				key := ev.ItemID
				if key == "" {
					return &protocolError{msg: "function_call_arguments.delta without item_id"}
				}
				bufFor(key).ArgsBuf += ev.Delta
			case "response.function_call_arguments.done":
				// Authoritative completion for one call; emit it now so a
				// later failure cannot strand an assembled call.
				key := ev.ItemID
				if key == "" {
					return &protocolError{msg: "function_call_arguments.done without item_id"}
				}
				buf := bufFor(key)
				if buf.emitted || buf.Name == "" {
					return nil
				}
				args, err := decodeResponseArgs(buf.ArgsBuf)
				if err != nil {
					return &protocolError{msg: "decode tool call arguments: " + err.Error()}
				}
				buf.emitted = true
				toolCallsSeen = true
				if !send(StreamEvent{ToolCalls: []ToolCall{{ID: buf.CallID, Name: buf.Name, Arguments: args}}}) {
					return errSendStopped
				}
			case "response.output_item.done":
				if ev.Item != nil && ev.Item.Type == "function_call" {
					key := ev.Item.ID
					if key == "" {
						key = ev.ItemID
					}
					if key == "" {
						return &protocolError{msg: "function_call item without id"}
					}
					buf := bufFor(key)
					if buf.emitted {
						return nil
					}
					if ev.Item.CallID != "" {
						buf.CallID = ev.Item.CallID
					}
					if ev.Item.Name != "" {
						buf.Name = ev.Item.Name
					}
					if buf.Name == "" {
						return &protocolError{msg: "function_call item without name"}
					}
					// The done item carries complete arguments when the
					// server sends them; fall back to streamed fragments.
					argSrc := ev.Item.Arguments
					if argSrc == "" {
						argSrc = buf.ArgsBuf
					}
					args, err := decodeResponseArgs(argSrc)
					if err != nil {
						return &protocolError{msg: "decode tool call arguments: " + err.Error()}
					}
					buf.emitted = true
					toolCallsSeen = true
					if !send(StreamEvent{ToolCalls: []ToolCall{{ID: buf.CallID, Name: buf.Name, Arguments: args}}}) {
						return errSendStopped
					}
				}
			case "response.completed":
				if ev.Response != nil && ev.Response.Usage != nil {
					u := ev.Response.Usage
					usage = &Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: u.TotalTokens}
				}
				finishTurn(FinishNone)
				return errSSEDone
			case "response.incomplete":
				reason := FinishStop
				if ev.Response != nil {
					if ev.Response.Usage != nil {
						u := ev.Response.Usage
						usage = &Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: u.TotalTokens}
					}
					if ev.Response.IncompleteDetails != nil && ev.Response.IncompleteDetails.Reason == "max_output_tokens" {
						reason = FinishLength
					}
				}
				finishTurn(reason)
				return errSSEDone
			case "response.failed":
				msg := "provider reported a failed response"
				if ev.Response != nil && ev.Response.Error != nil && ev.Response.Error.Message != "" {
					msg = ev.Response.Error.Message
				} else if ev.Error != nil && ev.Error.Message != "" {
					msg = ev.Error.Message
				} else if ev.Message != "" {
					msg = ev.Message
				}
				return errors.New(msg)
			default:
				// Lifecycle noise (created, in_progress, output_item deltas
				// for text/reasoning items, content_part events): ignored.
				return nil
			}
			return nil
		})

		switch {
		case errors.Is(streamErr, errSSEDone):
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
			// Stream ended without a terminal event (EOF): flush what we
			// have rather than dropping a complete turn.
			finishTurn(FinishNone)
		}
	}()

	return events, nil
}

// ListModels enumerates the models the server exposes via GET /models,
// which Responses-compatible gateways (including OpenCode) serve in the
// standard OpenAI list shape.
func (o *OpenAIResponses) ListModels(ctx context.Context) ([]ModelInfo, error) {
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

// decodeResponseArgs parses one accumulated function arguments string.
// Empty means "no arguments" (nil map, matching Chat Completions).
func decodeResponseArgs(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, err
	}
	return args, nil
}

// toResponseInput converts Forcefield history into stateless Responses
// input items. Assistant tool calls replay as function_call items using
// the API-issued call_id carried in the internal ToolCall ID, so the
// server can link each function_call_output to its call.
func toResponseInput(messages []Message) []responseInputItem {
	out := make([]responseInputItem, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case SystemRole:
			if msg.Content != "" {
				out = append(out, responseInputItem{Role: "system", Content: msg.Content})
			}
		case UserRole:
			if msg.Content != "" {
				out = append(out, responseInputItem{Role: "user", Content: msg.Content})
			}
		case AssistantRole:
			if msg.Content != "" {
				out = append(out, responseInputItem{Role: "assistant", Content: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				args, err := json.Marshal(tc.Arguments)
				if err != nil {
					args = []byte("{}")
				}
				out = append(out, responseInputItem{
					Type:   "function_call",
					CallID: tc.ID,
					Name:   tc.Name,
					Args:   string(args),
				})
			}
		case ToolRole:
			out = append(out, responseInputItem{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: msg.Content,
			})
		}
	}
	return out
}

func toResponseTools(defs []tools.Definition) []responseFunctionTool {
	out := make([]responseFunctionTool, 0, len(defs))
	for _, def := range defs {
		out = append(out, responseFunctionTool{
			Type:        "function",
			Name:        def.Name,
			Description: def.Description,
			Parameters:  def.InputSchema,
		})
	}
	return out
}
