package command

import "strings"

// ParsedInput is a slash-command line split into its command name and
// arguments, e.g. "/model qwen3:8b" -> {Name: "model", Args: ["qwen3:8b"]}.
type ParsedInput struct {
	Name string
	Args []string
}

// Parse reports whether line is a slash command and splits its name and
// arguments when it is one.
func Parse(line string) (ParsedInput, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return ParsedInput{}, false
	}

	fields := strings.Fields(line[1:])
	if len(fields) == 0 {
		return ParsedInput{}, false
	}

	return ParsedInput{
		Name: strings.ToLower(fields[0]),
		Args: fields[1:],
	}, true
}
