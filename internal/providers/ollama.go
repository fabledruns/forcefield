package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"forcefield/internal/tools"
	"io"
	"net/http"
	"strings"
)

// OllamaProvider talks to a local Ollama server's /api/chat endpoint.
type OllamaProvider struct {
	Endpoint string
	Model    string
	client   *http.Client
	retry    retryPolicy
	gate     *requestGate
}

// Capabilities reports what this adapter supports.
func (o *OllamaProvider) Capabilities() Capabilities {
	return Capabilities{
		Streaming:   true,
		ToolCalling: true,
		Reasoning:   true,
	}
}

// ListModels enumerates the models installed on the Ollama server.
func (o *OllamaProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if err := o.gate.acquire(); err != nil {
		return nil, err
	}
	defer o.gate.release()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(o.Endpoint, "/")+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("build request to %s: %w", o.Endpoint, err)
	}

	resp, err := doWithRetry(ctx, o.client, o.retry, "ollama", "", func() (*http.Request, error) { return req, nil }, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var body struct {
		Models []struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Details struct {
				Family            string `json:"family"`
				ParameterSize     string `json:"parameter_size"`
				QuantizationLevel string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&body); err != nil {
		return nil, &protocolError{msg: fmt.Sprintf("decode model list: %v", err)}
	}

	models := make([]ModelInfo, 0, len(body.Models))
	for _, m := range body.Models {
		id := m.Name
		if id == "" {
			id = m.Model
		}
		if id == "" {
			continue
		}
		description := ""
		if m.Details.ParameterSize != "" {
			description = m.Details.ParameterSize
			if m.Details.QuantizationLevel != "" {
				description += " · " + m.Details.QuantizationLevel
			}
		}
		models = append(models, ModelInfo{Name: id, ID: id, Description: description})
	}
	return models, nil
}

// NewOllamaProvider builds an OllamaProvider pointed at the given endpoint
// (e.g. "http://localhost:11434") and model name (e.g. "llama3").
func NewOllamaProvider(endpoint, model string) *OllamaProvider {
	return &OllamaProvider{
		Endpoint: endpoint,
		Model:    model,
		client:   &http.Client{},
		retry:    defaultRetryPolicy,
		gate:     newRequestGate(),
	}
}

// ollamaMessage mirrors the shape Ollama's chat API expects per message.
type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`

	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`

	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

type ollamaTool struct {
	Type     string         `json:"type"`
	Function ollamaFunction `json:"function"`
}

type ollamaFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ollamaToolCall struct {
	ID       string             `json:"id"`
	Function ollamaFunctionCall `json:"function"`
}

type ollamaFunctionCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
}

type ollamaStreamResponse struct {
	Message struct {
		Content   string           `json:"content"`
		Thinking  string           `json:"thinking"`
		ToolCalls []ollamaToolCall `json:"tool_calls"`
	} `json:"message"`

	Done  bool   `json:"done"`
	Error string `json:"error"`
}

// statusHint turns specific Ollama HTTP statuses into a concrete next
// step. The most common one by far is 404: the configured model was never
// pulled (or is misspelled), which otherwise reads as a bare "not found".
func (o *OllamaProvider) statusHint(status int, _ string) string {
	if status == http.StatusNotFound {
		return fmt.Sprintf(
			"model %q may not be installed - run `ollama pull %s` (see `ollama list` for what you have)",
			o.Model, o.Model,
		)
	}
	return ""
}

// StreamChat sends messages to Ollama and streams the response.
func (o *OllamaProvider) StreamChat(ctx context.Context, messages []Message, tools []tools.Definition) (<-chan StreamEvent, error) {
	ollamaMessages := toOllamaMessages(messages)
	ollamaTools := toOllamaTools(tools)

	reqBody := ollamaChatRequest{
		Model:    o.Model,
		Messages: ollamaMessages,
		Tools:    ollamaTools,
		Stream:   true,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encode ollama request: %w", err)
	}

	if err := o.gate.acquire(); err != nil {
		return nil, err
	}

	buildRequest := func() (*http.Request, error) {
		url := o.Endpoint + "/api/chat"
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("build request to %s: %w", url, err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		return httpReq, nil
	}

	wrapTransport := func(err error) error {
		return fmt.Errorf(
			"could not reach Ollama at %s (is `ollama serve` running?): %w",
			o.Endpoint, err,
		)
	}

	resp, err := doWithRetry(ctx, o.client, o.retry, "ollama", o.Model, buildRequest, wrapTransport)
	if err != nil {
		o.gate.release()
		return nil, annotateStatusHint(err, o.statusHint)
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

		decoder := json.NewDecoder(resp.Body)

		for {
			var chunk ollamaStreamResponse

			err := decoder.Decode(&chunk)
			if err == io.EOF {
				send(StreamEvent{
					Err: io.ErrUnexpectedEOF,
				})
				return
			}

			if err != nil {
				send(StreamEvent{
					Err: err,
				})
				return
			}

			if chunk.Error != "" {
				send(StreamEvent{
					Err: errors.New(chunk.Error),
				})

				return
			}

			if chunk.Message.Thinking != "" {
				if !send(StreamEvent{Thinking: chunk.Message.Thinking}) {
					return
				}
			}

			if chunk.Message.Content != "" {
				if !send(StreamEvent{Text: chunk.Message.Content}) {
					return
				}
			}

			if len(chunk.Message.ToolCalls) > 0 {
				if !send(StreamEvent{ToolCalls: toToolCalls(chunk.Message.ToolCalls)}) {
					return
				}
			}

			if chunk.Done {
				send(StreamEvent{Done: true})

				return
			}
		}
	}()

	return events, nil
}

// Helper conversions
func toOllamaMessages(messages []Message) []ollamaMessage {
	ollamaMessages := make([]ollamaMessage, 0, len(messages))
	for _, msg := range messages {
		var toolCalls []ollamaToolCall
		if len(msg.ToolCalls) > 0 {
			toolCalls = make([]ollamaToolCall, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				toolCalls = append(toolCalls, ollamaToolCall{
					ID: tc.ID,
					Function: ollamaFunctionCall{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				})
			}
		}

		ollamaMessages = append(ollamaMessages, ollamaMessage{
			Role:       string(msg.Role),
			Content:    msg.Content,
			ToolCalls:  toolCalls,
			ToolCallID: msg.ToolCallID,
			Name:       msg.Name,
		})
	}
	return ollamaMessages
}

func toToolCalls(calls []ollamaToolCall) []ToolCall {
	toolCalls := make([]ToolCall, 0, len(calls))
	for _, tc := range calls {
		toolCalls = append(toolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return toolCalls
}

func toOllamaTools(tools []tools.Definition) []ollamaTool {
	ollamaTools := make([]ollamaTool, 0, len(tools))
	for _, tool := range tools {
		ollamaTools = append(ollamaTools, ollamaTool{
			Type: "function",
			Function: ollamaFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}
	return ollamaTools
}
