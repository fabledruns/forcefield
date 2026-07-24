// Package runtime wires together config, skills, the agent, and a model
// provider to execute a single task. This is the "harness" itself: it
// contains no business logic of its own beyond ordering these steps.
package runtime

import (
	"context"
	"fmt"

	"forcefield/internal/agent"
	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/skills"
)

// Runtime is the main execution point for Forcefield.
type Runtime struct {
    cfg 	 *config.Config
    provider providers.ModelProvider
    agent    *agent.Agent
}

// type ChatRequest struct {
//     Messages []Message
//     SystemPrompt string
//     Provider string
//     Model string
//     Tools []Tool
// }
func New() (*Runtime, error) { 
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	forcefieldHome, err := config.Dir()
	if err != nil {
		return nil, fmt.Errorf("resolve forcefield home: %w", err)
	}

	skillsText, err := skills.Load(forcefieldHome)
	if err != nil {
		return nil, fmt.Errorf("load skills: %w", err)
	}

	a := agent.New(cfg.Agent.Name, cfg.Agent.SystemPrompt, skillsText)

	provider, err := newProvider(cfg)
	if err != nil {
		return nil, err
	}

	return &Runtime{
		cfg:      cfg,
		provider: provider,
		agent:    a,
	}, nil
}

func (r *Runtime) Run(task string) (string, error) {
    systemPrompt := r.agent.BuildSystemPrompt()

    response, err := r.provider.Chat(context.Background(), systemPrompt, task)
    if err != nil {
        return "", fmt.Errorf("model call failed: %w", err)
    }

    return response, nil
}

func Run(task string) (string, error) {
    rt, err := New()
    if err != nil {
        return "", fmt.Errorf("create runtime: %w", err)
    }

    return rt.Run(task)
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
