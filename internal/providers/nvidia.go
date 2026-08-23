package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"forcefield/internal/tools"
	"net/http"
	"strings"
)

// defaultNvidiaTimeout bounds how long a single request to NVIDIA NIM may
// take before it's aborted.
const defaultNvidiaTimeout = 0

// NvidiaProvider talks to OpenAI-compatible Chat Completions APIs. It
// powers NVIDIA NIM (https://integrate.api.nvidia.com/v1) and, via
// NewLMStudioProvider, LM Studio's local server, which speak the same
// wire protocol; only error wording and authentication differ.
type NvidiaProvider struct {
	Endpoint string
	Model    string
	APIKey   string
	client   *http.Client
	retry    retryPolicy
	gate     *requestGate

	// label is the human-facing service name used in errors ("NVIDIA NIM"
	// by default). Empty means the default.
	label string
}

// NewNvidiaProvider builds an NvidiaProvider pointed at the given endpoint
// (e.g. "https://integrate.api.nvidia.com/v1"), model name, and API key.
// If client is nil, a default client with a bounded timeout is used.
func NewNvidiaProvider(endpoint, model, apiKey string, client *http.Client) *NvidiaProvider {
	if client == nil {
		client = &http.Client{Timeout: defaultNvidiaTimeout}
	}
	return &NvidiaProvider{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Model:    model,
		APIKey:   apiKey,
		client:   client,
		retry:    defaultRetryPolicy,
		gate:     newRequestGate(),
	}
}

// NewLMStudioProvider builds a provider for LM Studio's local server,
// which exposes the same OpenAI-compatible chat completions API as NVIDIA
// NIM but is unauthenticated. Errors are worded for LM Studio so a user
// running locally is never told to go debug "NVIDIA NIM".
func NewLMStudioProvider(endpoint, model string) *NvidiaProvider {
	p := NewNvidiaProvider(endpoint, model, "", nil)
	p.label = "LM Studio"
	return p
}

// displayName returns the human-facing service name for error messages.
func (n *NvidiaProvider) displayName() string {
	if n.label != "" {
		return n.label
	}
	return "NVIDIA NIM"
}

// providerName returns the lowercase name doWithRetry uses in status
// errors ("nvidia nim returned status 404 …").
func (n *NvidiaProvider) providerName() string {
	return strings.ToLower(n.displayName())
}

// wrapTransport rewords connection-level failures so they say which
// service to check and how.
func (n *NvidiaProvider) wrapTransport(err error) error {
	hint := ""
	switch n.displayName() {
	case "LM Studio":
		hint = " (is the LM Studio local server running with the server enabled?)"
	default:
		hint = ""
	}
	return fmt.Errorf("could not reach %s at %s%s: %w", n.displayName(), n.Endpoint, hint, err)
}

// statusHint turns specific HTTP statuses into a concrete next step.
func (n *NvidiaProvider) statusHint(status int, _ string) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		if n.displayName() == "LM Studio" {
			return "LM Studio does not need an API key - make sure this endpoint points at an actual LM Studio server"
		}
		if n.APIKey == "" {
			return "no API key is configured - set the NVIDIA_API_KEY environment variable and restart Forcefield"
		}
		return "check that NVIDIA_API_KEY is valid and that this account can access this model"
	case http.StatusNotFound:
		return fmt.Sprintf("model %q was not found on %s - pick another model with /model or set model.name in config.yaml", n.Model, n.displayName())
	default:
		return ""
	}
}

// nvidiaMessage mirrors the shape the OpenAI-compatible chat API expects
// per message.
type nvidiaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`

	ToolCalls []nvidiaToolCall `json:"tool_calls,omitempty"`

	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

type nvidiaTool struct {
	Type     string         `json:"type"`
	Function nvidiaFunction `json:"function"`
}

type nvidiaFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type nvidiaToolCall struct {
	ID       string             `json:"id"`
	Function nvidiaFunctionCall `json:"function"`
}

type nvidiaFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type nvidiaChatRequest struct {
	Model              string                    `json:"model"`
	Messages           []nvidiaMessage           `json:"messages"`
	Tools              []nvidiaTool              `json:"tools,omitempty"`
	Stream             bool                      `json:"stream"`
	ChatTemplateKwargs *nvidiaChatTemplateKwargs `json:"chat_template_kwargs,omitempty"`
}

// nvidiaChatTemplateKwargs requests reasoning output from NIM-hosted
// models that gate it behind this non-standard field instead of streaming
// it unconditionally (GLM-5/5.1/5.2, several Qwen3 and MiniMax builds…).
// Models that don't recognize the field simply ignore it, so it's safe to
// send on every NVIDIA request rather than needing a per-model allowlist.
// clear_thinking:false preserves reasoning across turns for agentic/tool
// workflows, matching NVIDIA's own documented usage.
type nvidiaChatTemplateKwargs struct {
	EnableThinking bool `json:"enable_thinking"`
	ClearThinking  bool `json:"clear_thinking"`
}

type nvidiaToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// nvidiaStreamChunk is the relevant subset of a streamed
// /chat/completions response chunk (Server-Sent Events, OpenAI shape).
//
// Reasoning-capable models (DeepSeek-R1, Qwen3, Nemotron…) stream their
// chain of thought separately from the answer, in delta.reasoning_content
// (the field NIM documents) or delta.reasoning (the spelling some other
// OpenAI-compatible gateways use). Both are surfaced as Thinking events;
// models that reason inline or not at all simply never set them.
type nvidiaStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content"`
			Reasoning        string                `json:"reasoning"`
			ToolCalls        []nvidiaToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`

	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

type nvidiaToolCallBuf struct {
	ID      string
	Name    string
	ArgsBuf string
}

// StreamChat sends the conversation to NVIDIA NIM and returns a channel
// that emits StreamEvent objects as the model generates its reply. The
// channel is closed when the model is done, the context is cancelled, or
// an error occurs.
func (n *NvidiaProvider) StreamChat(ctx context.Context, messages []Message, tools []tools.Definition) (<-chan StreamEvent, error) {
	reqBody := nvidiaChatRequest{
		Model:    n.Model,
		Messages: toNvidiaMessages(messages),
		Tools:    toNvidiaTools(tools),
		Stream:   true,
		// Ask NIM to actually stream reasoning. Models that stream it
		// unconditionally (or don't support reasoning at all) are
		// unaffected; models that gate it behind this field (GLM-5.x and
		// others) only emit reasoning_content deltas when it's set.
		ChatTemplateKwargs: &nvidiaChatTemplateKwargs{
			EnableThinking: true,
			ClearThinking:  false,
		},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encode nvidia request: %w", err)
	}

	if err := n.gate.acquire(); err != nil {
		return nil, err
	}

	buildRequest := func() (*http.Request, error) {
		url := n.Endpoint + "/chat/completions"
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("build request to %s: %w", url, err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		if n.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+n.APIKey)
		}
		return httpReq, nil
	}

	resp, err := doWithRetry(ctx, n.client, n.retry, n.providerName(), n.Model, buildRequest, n.wrapTransport)
	if err != nil {
		n.gate.release()
		return nil, annotateStatusHint(err, n.statusHint)
	}

	events := make(chan StreamEvent)

	go func() {
		defer resp.Body.Close()
		defer close(events)
		defer n.gate.release()

		send := func(event StreamEvent) bool {
			select {
			case events <- event:
				return true
			case <-ctx.Done():
				return false
			}
		}

		pending := map[int]*nvidiaToolCallBuf{}
		order := []int{}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		// finishTurn flushes any buffered tool calls and completes the
		// stream. Every stream exit path funnels through here so partially
		// received tool calls are never silently dropped just because the
		// server omitted a "tool_calls" finish_reason - some NIM models end
		// tool-call turns with "stop" or a bare [DONE], which previously
		// left the runtime with an empty response that looked like a hang.
		finishTurn := func() {
			if len(order) > 0 {
				calls, err := finalizeNvidiaToolCalls(pending, order)
				if err != nil {
					send(StreamEvent{Err: fmt.Errorf("decode nvidia tool call arguments: %w", err)})
					return
				}
				if !send(StreamEvent{ToolCalls: calls}) {
					return
				}
			}
			send(StreamEvent{Done: true})
		}

		for scanner.Scan() {
			if ctx.Err() != nil {
				return
			}

			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "data:") {
				continue
			}

			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				finishTurn()
				return
			}

			var chunk nvidiaStreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				send(StreamEvent{Err: fmt.Errorf("decode nvidia stream chunk: %w", err)})
				return
			}

			if chunk.Error.Message != "" {
				send(StreamEvent{Err: errors.New(chunk.Error.Message)})
				return
			}

			if len(chunk.Choices) == 0 {
				continue
			}

			choice := chunk.Choices[0]

			if reasoning := choice.Delta.ReasoningContent; reasoning != "" {
				if !send(StreamEvent{Thinking: reasoning}) {
					return
				}
			} else if reasoning := choice.Delta.Reasoning; reasoning != "" {
				if !send(StreamEvent{Thinking: reasoning}) {
					return
				}
			}

			if choice.Delta.Content != "" {
				if !send(StreamEvent{Text: choice.Delta.Content}) {
					return
				}
			}

			for _, tc := range choice.Delta.ToolCalls {
				buf, ok := pending[tc.Index]
				if !ok {
					buf = &nvidiaToolCallBuf{}
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
				finishTurn()
				return
			}
		}

		if err := scanner.Err(); err != nil {
			send(StreamEvent{Err: err})
			return
		}
		// Stream ended without [DONE] or a finish reason (EOF): still a
		// complete turn as far as the runtime is concerned.
		finishTurn()
	}()

	return events, nil
}

func finalizeNvidiaToolCalls(pending map[int]*nvidiaToolCallBuf, order []int) ([]ToolCall, error) {
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
func toNvidiaMessages(messages []Message) []nvidiaMessage {
	nvidiaMessages := make([]nvidiaMessage, 0, len(messages))
	for _, msg := range messages {
		var toolCalls []nvidiaToolCall
		if len(msg.ToolCalls) > 0 {
			toolCalls = make([]nvidiaToolCall, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				args, err := json.Marshal(tc.Arguments)
				if err != nil {
					args = []byte("{}")
				}
				toolCalls = append(toolCalls, nvidiaToolCall{
					ID: tc.ID,
					Function: nvidiaFunctionCall{
						Name:      tc.Name,
						Arguments: string(args),
					},
				})
			}
		}

		nvidiaMessages = append(nvidiaMessages, nvidiaMessage{
			Role:       string(msg.Role),
			Content:    msg.Content,
			ToolCalls:  toolCalls,
			ToolCallID: msg.ToolCallID,
			Name:       msg.Name,
		})
	}
	return nvidiaMessages
}

func toNvidiaTools(tools []tools.Definition) []nvidiaTool {
	nvidiaTools := make([]nvidiaTool, 0, len(tools))
	for _, tool := range tools {
		nvidiaTools = append(nvidiaTools, nvidiaTool{
			Type: "function",
			Function: nvidiaFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}
	return nvidiaTools
}
