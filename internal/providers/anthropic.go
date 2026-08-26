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

// anthropicVersion pins the Messages API feature set Forcefield speaks.
const anthropicVersion = "2023-06-01"

// anthropicMaxTokens is required by the Messages API. It caps the reply,
// not the context window; models stop earlier when they finish naturally.
const anthropicMaxTokens = 8192

// AnthropicProvider talks to Anthropic's native Messages API. A dedicated
// adapter is warranted because the protocol differs fundamentally from
// OpenAI's: authentication headers, system prompts as a top-level field,
// content blocks instead of role strings, tool calls as tool_use /
// tool_result blocks, and a different SSE event grammar.
type AnthropicProvider struct {
	spec   Spec
	client *http.Client
	retry  retryPolicy
	gate   *requestGate

	authHintEnv string
}

// NewAnthropicProvider builds a provider from a resolved spec. BaseURL is
// the API root (default https://api.anthropic.com); the versioned path is
// appended here.
func NewAnthropicProvider(spec Spec) *AnthropicProvider {
	spec.BaseURL = strings.TrimRight(spec.BaseURL, "/")
	return &AnthropicProvider{
		spec:        spec,
		client:      &http.Client{},
		retry:       defaultRetryPolicy,
		gate:        newRequestGate(),
		authHintEnv: "ANTHROPIC_API_KEY",
	}
}

// displayName is the service name used in error messages.
func (a *AnthropicProvider) displayName() string {
	if a.spec.Label != "" {
		return a.spec.Label
	}
	return "Anthropic"
}

// Capabilities reports what this adapter supports.
func (a *AnthropicProvider) Capabilities() Capabilities {
	return Capabilities{
		Streaming:         true,
		ToolCalling:       true,
		Reasoning:         true,
		ParallelToolCalls: true,
	}
}

// statusHint turns specific HTTP statuses into a concrete next step.
func (a *AnthropicProvider) statusHint(status int, _ string) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		if a.spec.APIKey == "" {
			return fmt.Sprintf("no API key is configured - set the %s environment variable (or api_key_env in config.yaml) and restart Forcefield", a.authHintEnv)
		}
		return fmt.Sprintf("check that the %s value is valid and authorized for this model", a.authHintEnv)
	case http.StatusNotFound:
		return fmt.Sprintf("model %q was not found - pick another model with /model or set model.name in config.yaml", a.spec.Model)
	case http.StatusBadRequest:
		return "the request was rejected as malformed - if this persists the model may not support one of the requested features (e.g. tools)"
	default:
		return ""
	}
}

func (a *AnthropicProvider) wrapTransport(err error) error {
	return fmt.Errorf("could not reach %s at %s: %w", a.displayName(), a.spec.BaseURL, err)
}

// anthropicBlock is one content block inside a request or response.
// Which fields matter depends on Type.
type anthropicBlock struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"`

	// tool_use (request side / response side)
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`

	// tool_result (request side)
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// input_json_delta fragments accumulate into Input via this field.
	PartialJSON string `json:"partial_json,omitempty"`

	// thinking deltas
	Thinking string `json:"thinking,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"` // "user" or "assistant"
	Content []anthropicBlock `json:"content"`
}

type anthropicToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicToolDef `json:"tools,omitempty"`
	Stream    bool               `json:"stream"`
}

type anthropicResponse struct {
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// anthropicStopReasonFor maps the Messages API stop_reason vocabulary onto
// its normalized form.
func anthropicStopReasonFor(raw string) FinishReason {
	switch raw {
	case "":
		return FinishNone
	case "tool_use":
		return FinishToolCalls
	case "max_tokens":
		return FinishLength
	default:
		return FinishStop
	}
}

// buildMessages converts Forcefield history into Messages API form:
// system content moves to the top-level system field, assistant tool
// calls become tool_use blocks, and tool results become user-turn
// tool_result blocks (merged while consecutive, as the API expects).
func buildAnthropicMessages(messages []Message) (system string, out []anthropicMessage) {
	appendText := func(role, text string) {
		if text == "" {
			return
		}
		out = append(out, anthropicMessage{
			Role:    role,
			Content: []anthropicBlock{{Type: "text", Text: text}},
		})
	}

	for _, msg := range messages {
		switch msg.Role {
		case SystemRole:
			if msg.Content != "" {
				system = strings.TrimSpace(system + "\n\n" + msg.Content)
			}
		case AssistantRole:
			blocks := make([]anthropicBlock, 0, len(msg.ToolCalls)+1)
			if msg.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				input := tc.Arguments
				if input == nil {
					input = map[string]any{}
				}
				blocks = append(blocks, anthropicBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
			}
			if len(blocks) > 0 {
				out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
			}
		case ToolRole:
			result := anthropicBlock{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   msg.Content,
			}
			// Merge consecutive tool results into one user message.
			if n := len(out); n > 0 && out[n-1].Role == "user" && len(out[n-1].Content) > 0 && out[n-1].Content[0].Type == "tool_result" {
				out[n-1].Content = append(out[n-1].Content, result)
				continue
			}
			out = append(out, anthropicMessage{Role: "user", Content: []anthropicBlock{result}})
		case UserRole:
			appendText("user", msg.Content)
		}
	}
	return strings.TrimSpace(system), out
}

func toAnthropicTools(defs []tools.Definition) []anthropicToolDef {
	out := make([]anthropicToolDef, 0, len(defs))
	for _, def := range defs {
		schema := def.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, anthropicToolDef{Name: def.Name, Description: def.Description, InputSchema: schema})
	}
	return out
}

func (a *AnthropicProvider) newRequest(ctx context.Context, method, url, accept string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("build request to %s: %w", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", anthropicVersion)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if a.spec.APIKey != "" {
		req.Header.Set("x-api-key", a.spec.APIKey)
	}
	for name, value := range a.spec.Headers {
		req.Header.Set(name, value)
	}
	return req, nil
}

// do performs the HTTP round trip up to the status check, applying retry
// policy to transient rate limits. The caller owns the returned body.
func (a *AnthropicProvider) do(ctx context.Context, method, path, accept string, body []byte) (*http.Response, error) {
	if err := a.gate.acquire(); err != nil {
		return nil, err
	}
	buildRequest := func() (*http.Request, error) {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		return a.newRequest(ctx, method, a.spec.BaseURL+path, accept, reader)
	}

	resp, err := doWithRetry(ctx, a.client, a.retry, a.displayName(), a.spec.Model, buildRequest, a.wrapTransport)
	if err != nil {
		a.gate.release()
		return nil, Redacted(annotateStatusHint(err, a.statusHint), a.spec.APIKey)
	}
	return resp, nil
}

// StreamChat sends the conversation and streams the response as
// StreamEvent values.
func (a *AnthropicProvider) StreamChat(ctx context.Context, messages []Message, defs []tools.Definition) (<-chan StreamEvent, error) {
	system, history := buildAnthropicMessages(messages)

	body, err := json.Marshal(anthropicRequest{
		Model:     a.spec.Model,
		MaxTokens: anthropicMaxTokens,
		System:    system,
		Messages:  history,
		Tools:     toAnthropicTools(defs),
		Stream:    true,
	})
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	resp, err := a.do(ctx, http.MethodPost, "/v1/messages", "text/event-stream", body)
	if err != nil {
		return nil, err
	}

	events := make(chan StreamEvent)

	go func() {
		defer resp.Body.Close()
		defer close(events)
		defer a.gate.release()

		send := func(event StreamEvent) bool {
			select {
			case events <- event:
				return true
			case <-ctx.Done():
				return false
			}
		}

		// Blocks accumulate by index: text streams immediately, while
		// tool_use arguments arrive as JSON fragments that must be joined
		// before decoding.
		texts := map[int]string{}
		thinking := map[int]string{}
		tools := map[int]*anthropicBlock{}
		order := []int{}
		var usage Usage
		var stop FinishReason

		finishTurn := func() {
			if len(order) > 0 {
				calls := make([]ToolCall, 0, len(order))
				for _, idx := range order {
					block := tools[idx]
					calls = append(calls, ToolCall{ID: block.ID, Name: block.Name, Arguments: block.Input})
				}
				if !send(StreamEvent{ToolCalls: calls}) {
					return
				}
			}
			send(StreamEvent{Done: true, StopReason: stop, Usage: &usage})
		}

		streamErr := sseReader(ctx, resp.Body, func(data string) error {
			var event struct {
				Type string `json:"type"`

				Message *struct {
					Usage struct {
						InputTokens int `json:"input_tokens"`
					} `json:"usage"`
				} `json:"message"`

				Index        int             `json:"index"`
				ContentBlock *anthropicBlock `json:"content_block"`
				Delta        *anthropicBlock `json:"delta"`

				Error *struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				return &protocolError{msg: fmt.Sprintf("decode stream event: %v", err)}
			}

			switch event.Type {
			case "", "ping":
				return nil
			case "error":
				if event.Error != nil && event.Error.Message != "" {
					return fmt.Errorf("%s (%s)", event.Error.Message, event.Error.Type)
				}
				return &protocolError{msg: "stream reported an unspecified error"}
			case "message_start":
				if event.Message != nil {
					usage.PromptTokens = event.Message.Usage.InputTokens
				}
			case "content_block_start":
				if event.ContentBlock == nil {
					return &protocolError{msg: "content_block_start without a content block"}
				}
				switch event.ContentBlock.Type {
				case "tool_use":
					tools[event.Index] = event.ContentBlock
					order = append(order, event.Index)
				case "thinking":
					thinking[event.Index] = ""
				default: // "text"
					texts[event.Index] = ""
				}
			case "content_block_delta":
				if event.Delta == nil {
					return &protocolError{msg: "content_block_delta without a delta"}
				}
				switch event.Delta.Type {
				case "text_delta":
					texts[event.Index] += event.Delta.Text
					if !send(StreamEvent{Text: event.Delta.Text}) {
						return errSendStopped
					}
				case "thinking_delta":
					thinking[event.Index] += event.Delta.Thinking
					if !send(StreamEvent{Thinking: event.Delta.Thinking}) {
						return errSendStopped
					}
				case "input_json_delta":
					block, ok := tools[event.Index]
					if !ok {
						return &protocolError{msg: "input_json_delta for an unknown tool_use block"}
					}
					block.PartialJSON += event.Delta.PartialJSON
				}
			case "content_block_stop":
				if args := tools[event.Index]; args != nil && args.PartialJSON != "" {
					trimmed := strings.TrimSpace(args.PartialJSON)
					if trimmed == "" {
						args.Input = map[string]any{}
						break
					}
					var input map[string]any
					if err := json.Unmarshal([]byte(trimmed), &input); err != nil {
						return &protocolError{msg: "decode tool call arguments: " + err.Error()}
					}
					if input == nil {
						input = map[string]any{}
					}
					args.Input = input
				}
			case "message_delta":
				// message_delta carries both the final stop_reason (inside
				// delta) and cumulative output usage.
				var wrapper struct {
					Delta struct {
						StopReason string `json:"stop_reason"`
					} `json:"delta"`
					Usage struct {
						OutputTokens int `json:"output_tokens"`
					} `json:"usage"`
				}
				if err := json.Unmarshal([]byte(data), &wrapper); err == nil {
					stop = anthropicStopReasonFor(wrapper.Delta.StopReason)
					usage.CompletionTokens = wrapper.Usage.OutputTokens
					usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
				}
			case "message_stop":
				return errSSEDone
			}
			return nil
		})

		switch {
		case errors.Is(streamErr, errSSEDone):
			finishTurn()
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
			finishTurn()
		}
	}()

	return events, nil
}

// Complete performs one non-streaming completion.
func (a *AnthropicProvider) Complete(ctx context.Context, messages []Message, defs []tools.Definition) (Response, error) {
	system, history := buildAnthropicMessages(messages)

	body, err := json.Marshal(anthropicRequest{
		Model:     a.spec.Model,
		MaxTokens: anthropicMaxTokens,
		System:    system,
		Messages:  history,
		Tools:     toAnthropicTools(defs),
	})
	if err != nil {
		return Response{}, fmt.Errorf("encode request: %w", err)
	}

	resp, err := a.do(ctx, http.MethodPost, "/v1/messages", "application/json", body)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	a.gate.release() // single-flight covers the exchange only; the body is read here

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Response{}, fmt.Errorf("read response body: %w", err)
	}

	var out anthropicResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return Response{}, &protocolError{msg: fmt.Sprintf("decode response body: %v", err)}
	}
	if out.Error != nil && out.Error.Message != "" {
		return Response{}, fmt.Errorf("%s (%s)", out.Error.Message, out.Error.Type)
	}

	response := Response{}
	var calls []ToolCall
	for _, block := range out.Content {
		switch block.Type {
		case "text":
			response.Content += block.Text
		case "tool_use":
			input := block.Input
			if input == nil {
				input = map[string]any{}
			}
			calls = append(calls, ToolCall{ID: block.ID, Name: block.Name, Arguments: input})
		}
	}
	response.ToolCalls = calls
	response.Usage = Usage{
		PromptTokens:     out.Usage.InputTokens,
		CompletionTokens: out.Usage.OutputTokens,
		TotalTokens:      out.Usage.InputTokens + out.Usage.OutputTokens,
	}
	response.StopReason = anthropicStopReasonFor(out.StopReason)
	return response, nil
}

// ListModels enumerates models visible to the API key (first page).
func (a *AnthropicProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	resp, err := a.do(ctx, http.MethodGet, "/v1/models", "application/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	a.gate.release()

	var out struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
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

	models := make([]ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID == "" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		models = append(models, ModelInfo{Name: name, ID: m.ID})
	}
	return models, nil
}
