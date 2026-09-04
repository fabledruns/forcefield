package providers

import (
	"fmt"
	"strconv"
	"strings"
)

// ThinkingKind describes how a model exposes thinking/reasoning control.
type ThinkingKind string

const (
	ThinkingKindBool   ThinkingKind = "bool"
	ThinkingKindBudget ThinkingKind = "budget"
	ThinkingKindEnum   ThinkingKind = "enum"
)

// EffortCapability describes the reasoning effort levels a model supports.
type EffortCapability struct {
	Levels  []string
	Default string
}

// ThinkingCapability describes how a model exposes thinking control.
type ThinkingCapability struct {
	Kind ThinkingKind
	// Levels holds enumerated modes when Kind == ThinkingKindEnum.
	Levels []string
	// MinBudget/MaxBudget bounds a numeric budget when Kind == ThinkingKindBudget.
	MinBudget      int
	MaxBudget      int
	DefaultBudget  int
	DefaultEnabled bool
}

// ReasoningCapabilities is the centralized description of what a
// provider/model supports. The runtime and TUI read these instead of
// branching on provider/model names.
type ReasoningCapabilities struct {
	Effort   *EffortCapability
	Thinking *ThinkingCapability
}

// SupportsEffort reports whether effort is configurable for this model.
func (r ReasoningCapabilities) SupportsEffort() bool { return r.Effort != nil }

// SupportsThinking reports whether thinking is configurable for this model.
func (r ReasoningCapabilities) SupportsThinking() bool { return r.Thinking != nil }

// ReasoningConfig is the user-selected values, validated against a
// ReasoningCapabilities.
type ReasoningConfig struct {
	Effort   string
	Thinking *ThinkingConfig
}

// ThinkingConfig holds the selected thinking setting for the active model.
type ThinkingConfig struct {
	Enabled *bool
	Budget  *int
	Level   string
}

// ModelReasoningCapabilities returns the reasoning capabilities for a
// provider/model pair. It is the single capability source for the whole
// codebase; UI code must never hardcode model names.
func ModelReasoningCapabilities(providerID, modelID string) ReasoningCapabilities {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	modelID = strings.TrimSpace(modelID)
	modelLower := strings.ToLower(modelID)

	switch providerID {
	case "ollama":
		// Ollama's /api/chat supports a boolean "think" field for
		// reasoning-capable models. Conservative: every Ollama model is
		// listed as supporting the toggle; the server ignores it for
		// non-reasoning models.
		return ReasoningCapabilities{
			Thinking: &ThinkingCapability{Kind: ThinkingKindBool, DefaultEnabled: false},
		}
	case "anthropic":
		// Known Claude models expose extended thinking with a numeric budget.
		if anthropicThinkingModels[modelLower] {
			return ReasoningCapabilities{
				Thinking: &ThinkingCapability{
					Kind:           ThinkingKindBudget,
					MinBudget:      1024,
					MaxBudget:      32000,
					DefaultBudget:  4096,
					DefaultEnabled: false,
				},
			}
		}
		return ReasoningCapabilities{}
	case "gemini":
		if geminiThinkingModels[modelLower] {
			return ReasoningCapabilities{
				Thinking: &ThinkingCapability{
					Kind:           ThinkingKindBudget,
					MinBudget:      0,
					MaxBudget:      32768,
					DefaultBudget:  1024,
					DefaultEnabled: false,
				},
			}
		}
		return ReasoningCapabilities{}
	case "nvidia":
		// NVIDIA model-specific capabilities must be explicit and verifiable.
		// Do not use provider-wide heuristics. Unknown models remain unsupported.
		if caps, ok := nvidiaModelCapabilities[normalizeModelID(modelLower)]; ok {
			// Return deep copy to prevent caller mutation.
			return cloneReasoningCapabilities(caps)
		}
		// Fallback: also check without normalizing (exact key) for backward compat
		if caps, ok := nvidiaModelCapabilities[modelLower]; ok {
			return cloneReasoningCapabilities(caps)
		}
		return ReasoningCapabilities{}
	case "openai":
		if openAIEffortModels[modelLower] {
			return ReasoningCapabilities{
				Effort: &EffortCapability{
					Levels:  []string{"low", "medium", "high"},
					Default: "medium",
				},
			}
		}
		return ReasoningCapabilities{}
	case "lmstudio":
		return ReasoningCapabilities{}
	case "opencode-zen", "opencode-go":
		// Resolve the model's wire protocol first: reasoning controls
		// differ per transport, and one gateway hosts all three.
		table := opencodeTableForProvider(providerID)
		if table == nil {
			return ReasoningCapabilities{}
		}
		protocol, err := opencodeProtocolForModel(table, modelID)
		if err != nil {
			return ReasoningCapabilities{}
		}
		switch protocol {
		case opencodeResponses:
			// Reasoning effort is the Responses-native control.
			return ReasoningCapabilities{
				Effort: &EffortCapability{
					Levels:  []string{"low", "medium", "high"},
					Default: "medium",
				},
			}
		case opencodeMessages:
			// Only verified Claude models get budget thinking; other
			// Anthropic-protocol models stay conservative.
			if anthropicThinkingModels[modelLower] {
				return ReasoningCapabilities{
					Thinking: &ThinkingCapability{
						Kind:           ThinkingKindBudget,
						MinBudget:      1024,
						MaxBudget:      32000,
						DefaultBudget:  4096,
						DefaultEnabled: false,
					},
				}
			}
			return ReasoningCapabilities{}
		default:
			return ReasoningCapabilities{}
		}
	default:
		// Generic openai-compatible custom endpoints: handle test models
		// and known generic effort models. For tests we need a provider
		// "test" that supports both capabilities.
		if providerID == "test" {
			// test model "test-model" supports effort and thinking bool for testing generic mapping.
			// But model-specific: treat any test model as supporting both for flexibility.
			// Specific test models:
			if strings.Contains(modelLower, "effort") {
				return ReasoningCapabilities{
					Effort: &EffortCapability{Levels: []string{"low", "medium", "high", "xhigh"}, Default: "medium"},
				}
			}
			if strings.Contains(modelLower, "thinking") {
				return ReasoningCapabilities{
					Thinking: &ThinkingCapability{Kind: ThinkingKindBool},
				}
			}
			// default test supports both with budget
			if modelLower == "test-model" {
				return ReasoningCapabilities{
					Effort:   &EffortCapability{Levels: []string{"low", "medium", "high", "xhigh"}, Default: "medium"},
					Thinking: &ThinkingCapability{Kind: ThinkingKindBool},
				}
			}
		}
		if genericEffortModels[modelLower] {
			return ReasoningCapabilities{
				Effort: &EffortCapability{Levels: []string{"low", "medium", "high", "xhigh"}, Default: "medium"},
			}
		}
		if genericThinkingBoolModels[modelLower] {
			return ReasoningCapabilities{
				Thinking: &ThinkingCapability{Kind: ThinkingKindBool},
			}
		}
		return ReasoningCapabilities{}
	}
}

// ReasoningCapabilitiesForSpec resolves capabilities for a provider Spec.
// It is a convenience wrapper around ModelReasoningCapabilities.
func ReasoningCapabilitiesForSpec(spec Spec) ReasoningCapabilities {
	return ModelReasoningCapabilities(spec.ID, spec.Model)
}

// ValidateEffort reports whether level is allowed by this capability.
func (r ReasoningCapabilities) ValidateEffort(level string) error {
	if r.Effort == nil {
		return fmt.Errorf("Current model does not support configurable effort.")
	}
	if len(r.Effort.Levels) == 0 {
		return fmt.Errorf("Current model does not support configurable effort.")
	}
	norm := strings.ToLower(strings.TrimSpace(level))
	for _, allowed := range r.Effort.Levels {
		if strings.ToLower(allowed) == norm {
			return nil
		}
	}
	return fmt.Errorf("Invalid effort level %q. Supported levels: %s.", level, strings.Join(r.Effort.Levels, ", "))
}

// ValidateThinking reports whether cfg is allowed by this capability.
func (r ReasoningCapabilities) ValidateThinking(cfg ThinkingConfig) error {
	if r.Thinking == nil {
		return fmt.Errorf("Current model does not support thinking controls.")
	}
	switch r.Thinking.Kind {
	case ThinkingKindBool:
		if cfg.Enabled == nil && cfg.Budget == nil && cfg.Level == "" {
			return fmt.Errorf("thinking requires \"on\" or \"off\".")
		}
		if cfg.Budget != nil {
			return fmt.Errorf("Current model does not support thinking budget. Supported: on, off.")
		}
		if cfg.Level != "" {
			return fmt.Errorf("Invalid thinking value %q. Supported: on, off.", cfg.Level)
		}
		if cfg.Enabled == nil {
			return fmt.Errorf("thinking requires \"on\" or \"off\".")
		}
		return nil
	case ThinkingKindBudget:
		// Allow Enabled false to disable, or Budget within range to enable.
		if cfg.Enabled != nil && !*cfg.Enabled {
			return nil
		}
		if cfg.Budget != nil {
			if *cfg.Budget == 0 && r.Thinking.MinBudget == 0 {
				// 0 disables thinking for providers like Gemini where 0 is allowed.
				return nil
			}
			if *cfg.Budget < r.Thinking.MinBudget || *cfg.Budget > r.Thinking.MaxBudget {
				return fmt.Errorf("Invalid thinking budget %d. Supported range: %d-%d.", *cfg.Budget, r.Thinking.MinBudget, r.Thinking.MaxBudget)
			}
			return nil
		}
		if cfg.Enabled != nil && *cfg.Enabled {
			return nil
		}
		if cfg.Level != "" {
			return fmt.Errorf("Current model does not support thinking level %q. Supported budget range: %d-%d.", cfg.Level, r.Thinking.MinBudget, r.Thinking.MaxBudget)
		}
		return fmt.Errorf("thinking requires a budget value (e.g. /thinking %d) or \"on\"/\"off\".", r.Thinking.DefaultBudget)
	case ThinkingKindEnum:
		if cfg.Level == "" {
			return fmt.Errorf("thinking requires one of: %s.", strings.Join(r.Thinking.Levels, ", "))
		}
		norm := strings.ToLower(strings.TrimSpace(cfg.Level))
		for _, allowed := range r.Thinking.Levels {
			if strings.ToLower(allowed) == norm {
				return nil
			}
		}
		return fmt.Errorf("Invalid thinking level %q. Supported levels: %s.", cfg.Level, strings.Join(r.Thinking.Levels, ", "))
	default:
		return fmt.Errorf("Current model does not support thinking controls.")
	}
}

// ReasoningAware is implemented by providers that can be configured with
// abstract reasoning settings.
type ReasoningAware interface {
	SetReasoning(cfg ReasoningConfig)
	GetReasoning() ReasoningConfig
}

// Helper maps for static capability definitions. Keys are lowercased model IDs.

var anthropicThinkingModels = map[string]bool{
	"claude-sonnet-4-5": true,
	"claude-haiku-4-5":  true,
	// Newer Claude models served direct and via OpenCode Zen, all on the
	// Anthropic Messages protocol with budget thinking. Fable and
	// third-party models on the same protocol stay conservative.
	"claude-opus-4-5":   true,
	"claude-opus-4-6":   true,
	"claude-opus-4-7":   true,
	"claude-opus-4-8":   true,
	"claude-sonnet-4-6": true,
	"claude-sonnet-5":   true,
	"claude-opus-5":     true,
}

var geminiThinkingModels = map[string]bool{
	"gemini-2.5-flash": true,
	"gemini-2.5-pro":   true,
}

// nvidiaModelCapabilities is the explicit per-model contract for NVIDIA.
// Keys are normalized lowercased model IDs (without provider prefix).
// This must reflect the actual NVIDIA API reference, not model-card wording.
//   - reasoning_effort vs chat_template_kwargs.reasoning_strength are distinct;
//     do not alias xhigh ↔ max without documentation.
//   - none is an effort level, not a thinking toggle.
var nvidiaModelCapabilities = map[string]ReasoningCapabilities{
	// Existing verified NVIDIA reasoning models: effort low/medium/high/xhigh + thinking bool via chat_template_kwargs.enable_thinking
	"z-ai/glm-5.2": {
		Effort:   &EffortCapability{Levels: []string{"low", "medium", "high", "xhigh"}, Default: "medium"},
		Thinking: &ThinkingCapability{Kind: ThinkingKindBool},
	},
	"nvidia/nemotron-3-ultra-550b-a55b": {
		Effort:   &EffortCapability{Levels: []string{"low", "medium", "high", "xhigh"}, Default: "medium"},
		Thinking: &ThinkingCapability{Kind: ThinkingKindBool},
	},
	"deepseek-ai/deepseek-v4-pro": {
		Effort:   &EffortCapability{Levels: []string{"low", "medium", "high", "xhigh"}, Default: "medium"},
		Thinking: &ThinkingCapability{Kind: ThinkingKindBool},
	},
	// New verified models:
	// DeepSeek V4 Flash 0731: reasoning_effort none/high/max (API top-level, internally translated to chat_template_kwargs)
	// No independent thinking toggle; none is the effort level that disables reasoning.
	"deepseek-ai/deepseek-v4-flash-0731": {
		Effort: &EffortCapability{Levels: []string{"none", "high", "max"}, Default: "high"},
		// Thinking intentionally nil – /thinking should report unsupported, /effort none is valid
	},
	// Muse Glimmer 30B: NVIDIA API reference reasoning_effort none/minimal/low/medium/high/max
	// Model card wording xhigh is NOT exposed as API value; do not alias max → xhigh.
	// If reasoning_strength is the model-specific control, it would be separate; for now only reasoning_effort is verified.
	"meta/muse-glimmer-30b": {
		Effort: &EffortCapability{Levels: []string{"none", "minimal", "low", "medium", "high", "max"}, Default: "medium"},
	},
	// Also support prefixed form nvidia/meta/muse-glimmer-30b via normalization (see normalizeModelID)
	"nvidia/meta/muse-glimmer-30b": {
		Effort: &EffortCapability{Levels: []string{"none", "minimal", "low", "medium", "high", "max"}, Default: "medium"},
	},
	"nvidia/deepseek-ai/deepseek-v4-flash-0731": {
		Effort: &EffortCapability{Levels: []string{"none", "high", "max"}, Default: "high"},
	},
}

// nvidiaEffortModels kept for backward compat with existing tests that check false entries; use new map for capabilities.
var nvidiaEffortModels = map[string]bool{
	"z-ai/glm-5.2":                      true,
	"nvidia/nemotron-3-ultra-550b-a55b": true,
	"deepseek-ai/deepseek-v4-pro":       true,
	"thinkingmachines/inkling":          false, // explicitly unsupported for testing
	"minimaxai/minimax-m3":              false,
}

// normalizeModelID strips optional provider prefix and lowercases for lookup.
func normalizeModelID(mid string) string {
	m := strings.ToLower(strings.TrimSpace(mid))
	if strings.HasPrefix(m, "nvidia/") {
		m = strings.TrimPrefix(m, "nvidia/")
	}
	return m
}

func cloneReasoningCapabilities(c ReasoningCapabilities) ReasoningCapabilities {
	out := ReasoningCapabilities{}
	if c.Effort != nil {
		levels := make([]string, len(c.Effort.Levels))
		copy(levels, c.Effort.Levels)
		out.Effort = &EffortCapability{Levels: levels, Default: c.Effort.Default}
	}
	if c.Thinking != nil {
		levels := make([]string, len(c.Thinking.Levels))
		copy(levels, c.Thinking.Levels)
		out.Thinking = &ThinkingCapability{
			Kind:           c.Thinking.Kind,
			Levels:         levels,
			MinBudget:      c.Thinking.MinBudget,
			MaxBudget:      c.Thinking.MaxBudget,
			DefaultBudget:  c.Thinking.DefaultBudget,
			DefaultEnabled: c.Thinking.DefaultEnabled,
		}
	}
	return out
}

var openAIEffortModels = map[string]bool{
	"gpt-5":       true,
	"o1":          true,
	"o1-preview":  true,
	"o3":          true,
	"gpt-4o":      false,
	"gpt-4o-mini": false,
}

var genericEffortModels = map[string]bool{
	"test-effort-model": true,
}

var genericThinkingBoolModels = map[string]bool{
	"test-thinking-model": true,
}

// ParseThinkingBudget parses a string argument as a thinking budget.
func ParseThinkingBudget(arg string) (int, error) {
	val, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil {
		return 0, fmt.Errorf("Invalid thinking budget %q. Expected a number.", arg)
	}
	return val, nil
}

// CanonicalEffort returns the canonical casing for an effort level.
func (r ReasoningCapabilities) CanonicalEffort(level string) string {
	norm := strings.ToLower(strings.TrimSpace(level))
	for _, allowed := range r.Effort.Levels {
		if strings.ToLower(allowed) == norm {
			return allowed
		}
	}
	return level
}

// CanonicalThinkingLevel returns canonical casing for a thinking enum level.
func (r ReasoningCapabilities) CanonicalThinkingLevel(level string) string {
	if r.Thinking == nil {
		return level
	}
	norm := strings.ToLower(strings.TrimSpace(level))
	for _, allowed := range r.Thinking.Levels {
		if strings.ToLower(allowed) == norm {
			return allowed
		}
	}
	return level
}
