package command

import "strings"

// ParsedInput is a slash-command line split into its command name and
// arguments, e.g. "/model qwen3:8b" -> {Name: "model", Args: ["qwen3:8b"]}.
type ParsedInput struct {
	Name string
	Args []string
}

// Parse reports whether line is a slash command and, if so, splits it
// into a lowercased command name and its arguments. It does no lookup or
// execution, parsing is intentionally kept separate from dispatch so
// each can be reasoned about, and tested, on its own.
//
// Parse only allocates the slices backing ParsedInput.Args; there is no
// regexp and no intermediate string copies beyond what strings.Fields
// already needs, so it stays cheap enough to run on every keystroke's
// worth of submitted input.
func Parse(line string) (ParsedInput, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return ParsedInput{}, false
	}

	fields := strings.Fields(line[1:])
	if len(fields) == 0 {
		// A bare "/" (or "/   ") isnt a command worth dispatching.
		return ParsedInput{}, false
	}

	return ParsedInput{
		Name: strings.ToLower(fields[0]),
		Args: fields[1:],
	}, true
}
