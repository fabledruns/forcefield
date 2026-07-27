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
)

// OllamaProvider talks to a local Ollama server's /api/chat endpoint.
type OllamaProvider struct {
	Endpoint string
	Model    string
	client   *http.Client
}

// NewOllamaProvider builds an OllamaProvider pointed at the given endpoint
// (e.g. "http://localhost:11434") and model name (e.g. "llama3").
func NewOllamaProvider(endpoint, model string) *OllamaProvider {
	return &OllamaProvider{
		Endpoint: endpoint,
		Model:    model,
		client:   &http.Client{},
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
	ID 	     string         	`json:"id"`
	Function ollamaFunctionCall `json:"function"`
}

type ollamaFunctionCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ollamaChatRequest is the request body for POST /api/chat.
type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
}

// ollamaChatResponse is the relevant subset of Ollama's non-streaming
// /api/chat response body.
type ollamaChatResponse struct {
	Message struct {
		Content   string           `json:"content"`
		Thinking  string           `json:"thinking"`
		ToolCalls []ollamaToolCall `json:"tool_calls"`
	} `json:"message"`

	Done  bool   `json:"done"`
	Error string `json:"error"`
}

// ollamaStreamResponse is the relevant subset of Ollama's streaming
// /api/chat response body. Each chunk is a JSON object with this shape.
type ollamaStreamResponse struct {
	Message struct {
		Content   string           `json:"content"`
		Thinking  string           `json:"thinking"`
		ToolCalls []ollamaToolCall `json:"tool_calls"`
	} `json:"message"`

	Done  bool   `json:"done"`
	Error string `json:"error"`
}

// Chat sends the system and user prompts to Ollama and returns the
// model's reply text.
//
// IMPORTANT: Only used for non-streaming chat. For streaming, use StreamChat instead.
func (o *OllamaProvider) Chat(ctx context.Context, messages []Message, tools []tools.Definition) (Response, error) {
	ollamaMessages := toOllamaMessages(messages)
	ollamaTools := toOllamaTools(tools)

	reqBody := ollamaChatRequest{
		Model:    o.Model,
		Messages: ollamaMessages,
		Tools:    ollamaTools,
		Stream:   false,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return Response{}, fmt.Errorf("encode ollama request: %w", err)
	}

	url := o.Endpoint + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("build request to %s: %w", url, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf(
			"could not reach Ollama at %s (is `ollama serve` running?): %w",
			o.Endpoint, err,
		)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Response{}, fmt.Errorf(
			"ollama returned status %d for model %q: %s",
			resp.StatusCode,
			o.Model,
			string(body),
		)
	}

	var chatResp ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return Response{}, fmt.Errorf("decode ollama response: %w", err)
	}

	if chatResp.Error != "" {
		return Response{}, errors.New(chatResp.Error)
	}

	toolCalls := make([]ToolCall, 0, len(chatResp.Message.ToolCalls))
	for _, tc := range chatResp.Message.ToolCalls {
		toolCalls = append(toolCalls, ToolCall{
			ID: 	   tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	return Response{
		Content:   chatResp.Message.Content,
		ToolCalls: toolCalls,
	}, nil
}

// StreamChat sends the system and user prompts to Ollama and returns a channel
// that emits StreamEvent objects as the model generates its reply. The channel
// is closed when the model is done or if an error occurs.
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

	url := o.Endpoint + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request to %s: %w", url, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf(
			"could not reach Ollama at %s (is `ollama serve` running?): %w",
			o.Endpoint, err,
		)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		return nil, fmt.Errorf(
			"ollama returned status %d for model %q: %s",
			resp.StatusCode,
			o.Model,
			string(body),
		)
	}

	events := make(chan StreamEvent)

	go func() {
		defer resp.Body.Close()
		defer close(events)

		decoder := json.NewDecoder(resp.Body)

		for {
			var chunk ollamaStreamResponse

			err := decoder.Decode(&chunk)
			if err == io.EOF {
				events <- StreamEvent{
					Err: io.ErrUnexpectedEOF,
				}
				return
			}

			if err != nil {
				events <- StreamEvent{
					Err: err,
				}
				return
			}

			if chunk.Error != "" {
				events <- StreamEvent{
					Err: errors.New(chunk.Error),
				}

				return
			}

			if chunk.Message.Thinking != "" {
				events <- StreamEvent{
					Thinking: chunk.Message.Thinking,
				}
			}

			if chunk.Message.Content != "" {
				events <- StreamEvent{
					Text: chunk.Message.Content,
				}
			}

			if chunk.Done {
				events <- StreamEvent{
					Done: true,
				}

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