package command

import (
	"fmt"
	"strings"
)

// Dispatch parses line and, if it's a slash command, runs it against ctx
// using reg to resolve the command name.
//
// The first return value reports whether line was a slash command at
// all. When false, the caller should treat line as an ordinary chat
// message instead, Dispatch never returns an error for non-command
// input. When true, a non-nil error means the command was unrecognized
// (with name suggestions included in the message) or that the resolved
// command itself failed.
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

// joinOr joins items with commas and a trailing "or", e.g.
// ["/exit", "/help"] -> "/exit or /help".
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
