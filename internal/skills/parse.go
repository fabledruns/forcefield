package skills

import (
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type parsedSkill struct {
	id          string
	name        string
	description string
	body        string
}

func parse(filename, raw string) parsedSkill {
	base := strings.TrimSuffix(filename, filepath.Ext(filename))

	fields, body, ok := splitFrontmatter(raw)
	if !ok {
		body = raw
	}

	id := fields["id"]
	if id == "" {
		id = normalizeID(base)
	}

	name := fields["name"]
	if name == "" {
		if h := firstHeading(body); h != "" {
			name = h
		} else {
			name = base
		}
	}

	return parsedSkill{
		id:          id,
		name:        name,
		description: fields["description"],
		body:        body,
	}
}

func splitFrontmatter(raw string) (fields map[string]string, body string, ok bool) {
	lines := splitLines(raw)
	if len(lines) == 0 || !isDelimiter(lines[0]) {
		return nil, "", false
	}

	closing := -1
	for i := 1; i < len(lines); i++ {
		if isDelimiter(lines[i]) {
			closing = i
			break
		}
	}
	if closing == -1 {
		return nil, "", false
	}

	yamlBlock := raw[lines[1].start:lines[closing].start]

	var data map[string]any
	if err := yaml.Unmarshal([]byte(yamlBlock), &data); err != nil {
		return nil, "", false
	}

	fields = make(map[string]string, 3)
	for _, key := range []string{"id", "name", "description"} {
		v, present := data[key]
		if !present {
			continue
		}
		if s, isString := v.(string); isString {
			fields[key] = strings.TrimSpace(s)
		}
	}

	return fields, raw[lines[closing].end:], true
}

func isDelimiter(l line) bool {
	return strings.TrimRight(l.text, "\r") == "---"
}

type line struct {
	text       string
	start, end int
}

// splitLines preserves byte offsets so slices of the original string can be
// returned without reconstructing line endings.
func splitLines(s string) []line {
	var lines []line
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, line{text: s[start:i], start: start, end: i + 1})
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, line{text: s[start:], start: start, end: len(s)})
	}
	return lines
}

var h1Heading = regexp.MustCompile(`^#[ \t]+(.+?)[ \t]*$`)

func firstHeading(s string) string {
	for _, l := range splitLines(s) {
		if m := h1Heading.FindStringSubmatch(strings.TrimRight(l.text, "\r")); m != nil {
			return m[1]
		}
	}
	return ""
}

var nonIDChars = regexp.MustCompile(`[^a-z0-9]+`)

func normalizeID(s string) string {
	return strings.Trim(nonIDChars.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// NormalizeID is the exported form of normalizeID for use by callers
// that need to resolve user-supplied identifiers (e.g. /skills show).
func NormalizeID(s string) string { return normalizeID(s) }
