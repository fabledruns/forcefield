package builtin

import (
	"fmt"
	"strconv"
	"strings"

	"forcefield/internal/command"
	"forcefield/internal/providers"
)

// Thinking is the /thinking command. It controls reasoning/thinking mode
// for models that expose it.
type Thinking struct{}

func NewThinking() *Thinking { return &Thinking{} }

func (Thinking) Name() string      { return "thinking" }
func (Thinking) Aliases() []string { return nil }
func (Thinking) Description() string {
	return "Show or set thinking/reasoning mode for the active model."
}
func (Thinking) Usage() string { return "/thinking [on|off|budget|level]" }

func (Thinking) Execute(ctx command.Context, args []string) error {
	caps := ctx.ReasoningCapabilities()
	if !caps.SupportsThinking() {
		ctx.Println("Current model does not support thinking controls.")
		if len(args) > 0 {
			return fmt.Errorf("Current model does not support thinking controls.")
		}
		return nil
	}

	kind := caps.Thinking.Kind

	if len(args) == 0 {
		switch kind {
		case providers.ThinkingKindBool:
			enabled, err := ctx.ToggleThinking()
			if err != nil {
				return err
			}
			if enabled {
				ctx.Println("✓ Thinking: on")
			} else {
				ctx.Println("✓ Thinking: off")
			}
			return nil
		case providers.ThinkingKindBudget:
			tc := ctx.Thinking()
			if tc == nil {
				ctx.Println("Thinking: (not set) (budget range: %d-%d)", caps.Thinking.MinBudget, caps.Thinking.MaxBudget)
				return nil
			}
			if tc.Enabled != nil && !*tc.Enabled {
				ctx.Println("Thinking: off")
				return nil
			}
			if tc.Budget != nil {
				if *tc.Budget == 0 && caps.Thinking.MinBudget == 0 {
					ctx.Println("Thinking: off")
				} else {
					ctx.Println("Thinking: budget %d (range: %d-%d)", *tc.Budget, caps.Thinking.MinBudget, caps.Thinking.MaxBudget)
				}
				return nil
			}
			if tc.Enabled != nil && *tc.Enabled {
				ctx.Println("Thinking: on")
				return nil
			}
			ctx.Println("Thinking: (not set) (budget range: %d-%d)", caps.Thinking.MinBudget, caps.Thinking.MaxBudget)
			return nil
		case providers.ThinkingKindEnum:
			current := ""
			if tc := ctx.Thinking(); tc != nil {
				current = tc.Level
			}
			levels := strings.Join(caps.Thinking.Levels, ", ")
			if current == "" {
				ctx.Println("Thinking: (not set) (available: %s)", levels)
			} else {
				ctx.Println("Thinking: %s (available: %s)", current, levels)
			}
			return nil
		default:
			return fmt.Errorf("Current model does not support thinking controls.")
		}
	}

	if len(args) > 1 {
		return fmt.Errorf("expected at most one argument, got %d", len(args))
	}

	arg := strings.TrimSpace(args[0])

	switch kind {
	case providers.ThinkingKindBool:
		lower := strings.ToLower(arg)
		var enabled bool
		switch lower {
		case "on", "true", "1", "enable", "enabled":
			enabled = true
		case "off", "false", "0", "disable", "disabled":
			enabled = false
		default:
			return fmt.Errorf("Invalid thinking value %q. Supported: on, off.", arg)
		}
		tc := providers.ThinkingConfig{Enabled: &enabled}
		if err := ctx.SetThinking(tc); err != nil {
			return err
		}
		if enabled {
			ctx.Println("✓ Thinking: on")
		} else {
			ctx.Println("✓ Thinking: off")
		}
		return nil

	case providers.ThinkingKindBudget:
		lower := strings.ToLower(arg)
		if lower == "on" || lower == "true" || lower == "enable" || lower == "enabled" {
			enabled := true
			tc := providers.ThinkingConfig{Enabled: &enabled}
			if err := ctx.SetThinking(tc); err != nil {
				return err
			}
			ctx.Println("✓ Thinking: on")
			return nil
		}
		if lower == "off" || lower == "false" || lower == "disable" || lower == "disabled" || lower == "0" {
			enabled := false
			tc := providers.ThinkingConfig{Enabled: &enabled}
			if err := ctx.SetThinking(tc); err != nil {
				return err
			}
			ctx.Println("✓ Thinking: off")
			return nil
		}
		budget, err := strconv.Atoi(arg)
		if err != nil {
			return fmt.Errorf("Invalid thinking value %q. Supported: on, off, or budget %d-%d.", arg, caps.Thinking.MinBudget, caps.Thinking.MaxBudget)
		}
		tc := providers.ThinkingConfig{Budget: &budget}
		if err := ctx.SetThinking(tc); err != nil {
			return err
		}
		ctx.Println("✓ Thinking: budget %d", budget)
		return nil

	case providers.ThinkingKindEnum:
		tc := providers.ThinkingConfig{Level: arg}
		if err := ctx.SetThinking(tc); err != nil {
			return err
		}
		ctx.Println("✓ Thinking: %s", caps.CanonicalThinkingLevel(arg))
		return nil
	default:
		return fmt.Errorf("Current model does not support thinking controls.")
	}
}
