package command

import (
	"fmt"
	"strings"
)

// Dispatch parses and runs a slash command. It reports false for chat input.
func Dispatch(ctx Context, reg *Registry, line string) (isCommand bool, err error) {
	parsed, ok := Parse(line)
	if !ok {
		return false, nil
	}

	cmd, ok := reg.Lookup(parsed.Name)
	if !ok {
		return true, unknownCommandError(reg, parsed.Name)
	}

	if err := cmd.Execute(ctx, parsed.Args); err != nil {
		return true, fmt.Errorf("/%s: %w", parsed.Name, err)
	}
	return true, nil
}

func unknownCommandError(reg *Registry, name string) error {
	suggestions := reg.Suggest(name, 3)
	if len(suggestions) == 0 {
		return fmt.Errorf("unknown command /%s (try /help)", name)
	}

	formatted := make([]string, len(suggestions))
	for i, s := range suggestions {
		formatted[i] = "/" + s
	}
	return fmt.Errorf("unknown command /%s — did you mean %s?", name, joinOr(formatted))
}

func joinOr(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
	}
}
