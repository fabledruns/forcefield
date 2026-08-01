package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"forcefield/internal/tools"
	"io"
	"net/http"
	"strings"
	"time"
)

// defaultNvidiaTimeout bounds how long a single request to NVIDIA NIM may
// take before it's aborted.
const defaultNvidiaTimeout = 60 * time.Second

// NvidiaProvider talks to NVIDIA NIM's OpenAI-compatible Chat Completions
// API at https://integrate.api.nvidia.com/v1.
type NvidiaProvider struct {
	Endpoint string
	Model    string
	APIKey   string
	client   *http.Client
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

// nvidiaToolCall is a complete (non-streamed) tool call, used when sending
// prior assistant messages back to the API.
type nvidiaToolCall struct {
	ID       string             `json:"id"`
	Function nvidiaFunctionCall `json:"function"`
}

type nvidiaFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// nvidiaChatRequest is the request body for POST /chat/completions.
type nvidiaChatRequest struct {
	Model    string          `json:"model"`
	Messages []nvidiaMessage `json:"messages"`
	Tools    []nvidiaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
}

// nvidiaToolCallDelta is a partial tool call as it appears in a streamed
// delta. Index identifies which tool call this fragment belongs to;
// ID/Name/Arguments may each be empty and are appended incrementally.
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
type nvidiaStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string                `json:"content"`
			ToolCalls []nvidiaToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`

	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// nvidiaToolCallBuf accumulates a streamed tool call's fragments (ID, name,
// and raw argument text) until finish_reason confirms it is complete.
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
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encode nvidia request: %w", err)
	}

	url := n.Endpoint + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request to %s: %w", url, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if n.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer " + n.APIKey)
	}

	resp, err := n.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("could not reach NVIDIA NIM at %s: %w", n.Endpoint, err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		return nil, fmt.Errorf(
			"nvidia nim returned status %d for model %q: %s",
			resp.StatusCode,
			n.Model,
			string(body),
		)
	}

	events := make(chan StreamEvent)

	go func() {
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

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		// pending buffers partial tool calls by their stream index until
		// their arguments are complete.
		pending := map[int]*nvidiaToolCallBuf{}
		order := []int{}

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
				send(StreamEvent{Done: true})
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

			switch choice.FinishReason {
			case "":
				// turn not finished yet, keep reading
			case "tool_calls":
				calls, err := finalizeNvidiaToolCalls(pending, order)
				if err != nil {
					send(StreamEvent{Err: fmt.Errorf("decode nvidia tool call arguments: %w", err)})
					return
				}
				if len(calls) > 0 {
					if !send(StreamEvent{ToolCalls: calls}) {
						return
					}
				}
				send(StreamEvent{Done: true})
				return
			case "stop", "length", "content_filter":
				send(StreamEvent{Done: true})
				return
			default:
				// Unknown finish reason: treat the turn as complete rather
				// than looping forever, but don't fail the whole stream.
				send(StreamEvent{Done: true})
				return
			}
		}

		if err := scanner.Err(); err != nil {
			send(StreamEvent{Err: err})
		}
	}()

	return events, nil
}

// finalizeNvidiaToolCalls parses the buffered argument strings into
// ToolCall.Arguments now that streaming is complete, in the order the
// tool calls first appeared.
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