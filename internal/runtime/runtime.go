// Package runtime wires together config, skills, the agent, and a model
// provider to execute a single task. This is the "harness" itself: it
// contains no business logic of its own beyond ordering these steps.
package runtime

import (
	"context"
	"fmt"
	"time"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/skills"
	"forcefield/internal/tools"
	"forcefield/internal/tools/builtin"
)

// Runtime is the main execution point for Forcefield.
type Runtime struct {
	cfg      *config.Config
	provider providers.ModelProvider
	agent    *agent.Agent
	manager  *tools.Manager
	skills   *skills.Store
}

func New() (*Runtime, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	forcefieldHome, err := config.Dir()
	if err != nil {
		return nil, fmt.Errorf("resolve forcefield home: %w", err)
	}

	// Build the skill store once for the lifetime of this Runtime. 
	skillStore, err := skills.New(forcefieldHome)
	if err != nil {
		return nil, fmt.Errorf("load skill store: %w", err)
	}

	a := agent.New(
		cfg.Agent.Name,
		cfg.Agent.SystemPrompt,
		skills.FormatCatalog(skillStore.Catalog()),
	)

	provider, err := newProvider(cfg)
	if err != nil {
		return nil, err
	}

	manager, err := builtin.NewManager()
	if err != nil {
		return nil, fmt.Errorf("create tool manager: %w", err)
	}
	if err := manager.Register(newLoadSkillTool(skillStore)); err != nil {
		return nil, fmt.Errorf("register load_skill tool: %w", err)
	}

	return &Runtime{
		cfg:      cfg,
		provider: provider,
		agent:    a,
		manager:  manager,
		skills:   skillStore,
	}, nil
}

func (r *Runtime) CurrentModel() string    { return r.cfg.Model.Name }     // CurrentModel returns the name of the model currently in use.
func (r *Runtime) CurrentProvider() string { return r.cfg.Model.Provider } // CurrentProvider returns the name of the provider currently in use.

// SetModel switches the active model, keeping the current provider and
// endpoint, and takes effect starting with the next request.
func (r *Runtime) SetModel(name string) error {
	if name == "" {
		return fmt.Errorf("model name cannot be empty")
	}
	cfg := *r.cfg
	cfg.Model.Name = name
	provider, err := newProvider(&cfg)
	if err != nil {
		return err
	}
	r.cfg = &cfg
	r.provider = provider
	return nil
}

// SetProvider switches the active provider, keeping the current model
// name, and takes effect starting with the next request.
func (r *Runtime) SetProvider(name string) error {
	if name == "" {
		return fmt.Errorf("provider name cannot be empty")
	}
	cfg := *r.cfg
	cfg.Model.Provider = name
	provider, err := newProvider(&cfg)
	if err != nil {
		return err
	}
	r.cfg = &cfg
	r.provider = provider
	return nil
}

func (r *Runtime) buildMessages(history []providers.Message) []providers.Message {
	messages := []providers.Message{
		{
			Role:    providers.SystemRole,
			Content: r.agent.BuildSystemPrompt(),
		},
	}

	messages = append(messages, history...)

	return messages
}

// StreamChat runs the agent loop and emits its structured events as they
// happen. Model text, tool calls, tool results, and the final response all
// flow through this single execution path.
func (r *Runtime) StreamChat(ctx context.Context, messages []providers.Message) (<-chan Event, error) {
	if ctx == nil {
		return nil, fmt.Errorf("stream context cannot be nil")
	}

	events := make(chan Event)
	go func() {
		defer close(events)
		r.run(ctx, r.buildMessages(messages), func(event Event) bool {
			select {
			case events <- event:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()

	return events, nil
}

// Stream is kept as a compatibility alias for StreamChat.
func (r *Runtime) Stream(ctx context.Context, messages []providers.Message) (<-chan Event, error) {
	return r.StreamChat(ctx, messages)
}

// Run executes the same streaming agent loop as StreamChat and returns its
// final response after consuming the emitted events.
func (r *Runtime) Run(messages []providers.Message) (providers.Response, error) {
	return r.RunContext(context.Background(), messages)
}

// RunContext is Run with caller-controlled cancellation and deadlines.
func (r *Runtime) RunContext(ctx context.Context, messages []providers.Message) (providers.Response, error) {
	events, err := r.StreamChat(ctx, messages)
	if err != nil {
		return providers.Response{}, err
	}

	for event := range events {
		switch event.Type {
		case EventDone:
			if event.Response == nil {
				return providers.Response{}, fmt.Errorf("runtime completed without a response")
			}
			return *event.Response, nil
		case EventError:
			return providers.Response{}, event.Err
		}
	}

	if err := ctx.Err(); err != nil {
		return providers.Response{}, err
	}
	return providers.Response{}, fmt.Errorf("runtime stopped without a completion event")
}

// run is the only agent loop. It turns provider chunks into runtime events,
// executes requested tools, appends their results to the conversation, and
// repeats until a model turn does not request a tool.
func (r *Runtime) run(ctx context.Context, messages []providers.Message, emit func(Event) bool) {
	for {
		response, err := r.runModelTurn(ctx, messages, emit)
		if err != nil {
			emit(Event{Type: EventError, Err: err})
			return
		}

		if len(response.ToolCalls) == 0 {
			emit(Event{Type: EventDone, Response: &response})
			return
		}

		messages = append(messages, providers.Message{
			Role:      providers.AssistantRole,
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		})

		for _, tc := range response.ToolCalls {
			call := tc
			if !emit(Event{Type: EventToolStart, ToolCall: &call}) {
				return
			}

			started := time.Now()
			result, err := r.manager.Execute(
				ctx,
				tc.Name,
				tc.Arguments,
			)
			if err != nil {
				emit(Event{Type: EventToolFinish, ToolResult: &ToolResult{
					ToolCallID: tc.ID,
					Name:       tc.Name,
					Arguments:  tc.Arguments,
					Success:    false,
					Duration:   time.Since(started),
					Err:        err,
				}})
				emit(Event{Type: EventError, Err: fmt.Errorf("execute tool %q: %w", tc.Name, err)})
				return
			}

			if !emit(Event{Type: EventToolFinish, ToolResult: &ToolResult{
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Arguments:  tc.Arguments,
				Content:    result.Content,
				IsError:    result.IsError,
				Success:    !result.IsError,
				Duration:   time.Since(started),
			}}) {
				return
			}

			messages = append(messages, providers.Message{
				Role:       providers.ToolRole,
				Name:       tc.Name,
				Content:    result.Content,
				ToolCallID: tc.ID,
			})
		}
	}
}

// runModelTurn streams one provider response and returns the assembled model
// response. It deliberately knows nothing about tool execution.
func (r *Runtime) runModelTurn(ctx context.Context, messages []providers.Message, emit func(Event) bool) (providers.Response, error) {
	if !emit(Event{Type: EventThinking}) {
		return providers.Response{}, context.Canceled
	}

	stream, err := r.provider.StreamChat(ctx, messages, r.manager.Definitions())
	if err != nil {
		return providers.Response{}, fmt.Errorf("model call failed: %w", err)
	}

	var response providers.Response
	for event := range stream {
		if event.Err != nil {
			return providers.Response{}, fmt.Errorf("model stream failed: %w", event.Err)
		}

		if event.Thinking != "" {
			if !emit(Event{Type: EventThinking, Thinking: event.Thinking}) {
				return providers.Response{}, context.Canceled
			}
		}

		if event.Text != "" {
			response.Content += event.Text
			if !emit(Event{Type: EventText, Text: event.Text}) {
				return providers.Response{}, context.Canceled
			}
		}

		response.ToolCalls = append(response.ToolCalls, event.ToolCalls...)
	}

	if err := ctx.Err(); err != nil {
		return providers.Response{}, err
	}
	return response, nil
}

func Run(messages []providers.Message) (providers.Response, error) {
	rt, err := New()
	if err != nil {
		return providers.Response{}, fmt.Errorf("create runtime: %w", err)
	}

	return rt.Run(messages)
}

// newProvider selects and constructs a ModelProvider based on
// cfg.Model.Provider. Only "ollama" is supported in this prototype;
// anything else fails fast with a clear message.
func newProvider(cfg *config.Config) (providers.ModelProvider, error) {
	switch cfg.Model.Provider {
	case "ollama":
		return providers.NewOllamaProvider(cfg.Model.Endpoint, cfg.Model.Name), nil
	default:
		return nil, fmt.Errorf(
			"unsupported model provider %q (only \"ollama\" is supported in this prototype)",
			cfg.Model.Provider,
		)
	}
}
