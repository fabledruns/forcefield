package mcp

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Sanitization and bounds enforcement for untrusted MCP data: tool names,
// descriptions, input schemas, and qualified tool names. Everything here
// is pure and side-effect free. The governing rules:
//
//   - names are sanitized to a restricted charset; over-long names are
//     rejected, never silently cut (cutting could create ambiguity)
//   - descriptions are truncated as plain text on a rune boundary;
//     schemas are never truncated (truncating JSON could forge a
//     permissive schema), so oversized schemas skip the tool instead
//   - validation errors name fields and reasons with bounded excerpts;
//     they never carry values, payloads, or environment content

// segmentPattern matches one sanitized name segment: letters, digits,
// underscore, hyphen. Dots and all other runes are mapped to underscore
// by SanitizeSegment before this pattern is checked.
var segmentPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// qualifiedPattern matches a namespaced tool name of the form
// mcp__server__tool. It is the same pattern configuration validation
// accepts for tools: overrides, so both layers agree on the shape.
var qualifiedPattern = regexp.MustCompile(`^mcp__[A-Za-z0-9_-]+__[A-Za-z0-9_-]+$`)

// SanitizeSegment maps one server-advertised name segment onto the
// restricted charset: every rune outside [A-Za-z0-9_-] becomes "_", and
// the result must be non-empty within MaxRemoteToolNameLen. Runs of
// underscores are preserved as-is so sanitization stays a pure function
// callers can reason about; collision handling (including across servers
// and against native names) belongs to later registration layers, which
// fail closed on any collision.
func SanitizeSegment(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("mcp: empty name: %w", ErrInvalidTool)
	}
	if len([]rune(s)) > MaxRemoteToolNameLen {
		return "", fmt.Errorf("mcp: name %q exceeds %d characters: %w",
			quoteBounded(s), MaxRemoteToolNameLen, ErrTooLarge)
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if !segmentPattern.MatchString(out) {
		return "", fmt.Errorf("mcp: name %q sanitizes to nothing usable: %w", quoteBounded(s), ErrInvalidTool)
	}
	return out, nil
}

// SanitizeRemoteName sanitizes one server-advertised tool name. Dots are
// the common real-world case (e.g. "fs.read_file") and map to underscore.
func SanitizeRemoteName(name string) (string, error) {
	return SanitizeSegment(name)
}

// QualifiedName builds the namespaced tool name mcp__server__tool. The
// server key must already be valid (see ValidateServerKey); the remote
// name is sanitized. Results longer than MaxQualifiedToolNameLen are
// rejected, never truncated into ambiguity.
func QualifiedName(serverKey, remoteName string) (string, error) {
	if err := ValidateServerKey(serverKey); err != nil {
		return "", err
	}
	sanitized, err := SanitizeRemoteName(remoteName)
	if err != nil {
		return "", err
	}
	out := "mcp__" + serverKey + "__" + sanitized
	if len(out) > MaxQualifiedToolNameLen {
		return "", fmt.Errorf("mcp: qualified tool name %q exceeds %d characters: %w",
			quoteBounded(out), MaxQualifiedToolNameLen, ErrTooLarge)
	}
	return out, nil
}

// IsQualifiedToolName reports whether name has the namespaced MCP tool
// shape within the length bound. Configuration validation uses it to
// accept tools: mcp__* overrides with the same limits mechanism as native
// tools; existence of the tool is checked later at discovery time.
func IsQualifiedToolName(name string) bool {
	if len(name) > MaxQualifiedToolNameLen {
		return false
	}
	return qualifiedPattern.MatchString(name)
}

// TruncateDescription caps one tool description to MaxDescriptionBytes on
// a rune boundary, appending TruncationMarker when anything was cut. It
// returns the (possibly unchanged) text and whether truncation happened.
// Descriptions are plain text: this function never parses or emits JSON.
func TruncateDescription(s string) (string, bool) {
	if len(s) <= MaxDescriptionBytes {
		return s, false
	}
	kept := validUTF8Prefix(s, MaxDescriptionBytes)
	return kept + TruncationMarker, true
}

// RemoteTool is one server-advertised tool definition after parsing.
// InputSchema is nil when the server sent none; callers treat nil like
// an empty object schema, matching ValidateArgs permissiveness.
type RemoteTool struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// ParseToolEntry parses one raw tools/list entry into a RemoteTool without
// applying size caps: it establishes shape (name string, description
// string-or-absent, inputSchema object-or-absent). Bounds run in
// ValidateRemoteTool so envelope parsing and bound enforcement stay in
// distinct, testable steps.
func ParseToolEntry(raw json.RawMessage) (RemoteTool, error) {
	if len(raw) == 0 {
		return RemoteTool{}, fmt.Errorf("mcp: empty tool entry: %w", ErrInvalidTool)
	}
	var entry struct {
		Name        json.RawMessage `json:"name"`
		Description json.RawMessage `json:"description,omitempty"`
		InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return RemoteTool{}, fmt.Errorf("mcp: malformed tool entry (%s): %w", quoteBounded(err.Error()), ErrInvalidTool)
	}
	var name string
	if err := json.Unmarshal(entry.Name, &name); err != nil || name == "" {
		return RemoteTool{}, fmt.Errorf("mcp: tool entry without a usable name: %w", ErrInvalidTool)
	}
	tool := RemoteTool{Name: name}
	if len(entry.Description) > 0 && string(entry.Description) != "null" {
		var desc string
		if err := json.Unmarshal(entry.Description, &desc); err != nil {
			return RemoteTool{}, fmt.Errorf("mcp: tool %q has a non-string description: %w", quoteBounded(name), ErrInvalidTool)
		}
		tool.Description = desc
	}
	if len(entry.InputSchema) > 0 && string(entry.InputSchema) != "null" {
		var schema map[string]any
		if err := json.Unmarshal(entry.InputSchema, &schema); err != nil {
			return RemoteTool{}, fmt.Errorf("mcp: tool %q has a non-object inputSchema: %w", quoteBounded(name), ErrInvalidTool)
		}
		tool.InputSchema = schema
	}
	return tool, nil
}

// ValidateRemoteTool enforces size and shape bounds on one parsed tool
// entry. Oversized or over-complex definitions fail here so the tool is
// skipped; schemas are never truncated. Name length is checked
// pre-sanitization and the sanitized form must match the segment pattern.
func ValidateRemoteTool(tool RemoteTool) error {
	if len([]rune(tool.Name)) > MaxRemoteToolNameLen {
		return fmt.Errorf("mcp: tool name %q exceeds %d characters: %w",
			quoteBounded(tool.Name), MaxRemoteToolNameLen, ErrTooLarge)
	}
	if _, err := SanitizeRemoteName(tool.Name); err != nil {
		return err
	}
	if tool.InputSchema != nil {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return fmt.Errorf("mcp: tool %q schema does not re-encode: %w", quoteBounded(tool.Name), ErrInvalidTool)
		}
		if len(raw) > MaxSchemaBytes {
			return fmt.Errorf("mcp: tool %q schema of %d bytes exceeds %d byte limit: %w",
				quoteBounded(tool.Name), len(raw), MaxSchemaBytes, ErrTooLarge)
		}
		if err := checkSchemaShape(tool.InputSchema, 1, tool.Name); err != nil {
			return err
		}
	}
	return nil
}

// checkSchemaShape walks one decoded inputSchema enforcing depth,
// property-count, enum-size, and string-leaf bounds. Unknown keywords are
// ignored, matching ValidateArgs granularity: the schema constrains
// arguments, it grants no capabilities. depth tracks object/array nesting
// starting at 1 for the top-level schema.
func checkSchemaShape(v any, depth int, toolName string) error {
	if depth > MaxSchemaDepth {
		return fmt.Errorf("mcp: tool %q schema exceeds nesting depth %d: %w",
			quoteBounded(toolName), MaxSchemaDepth, ErrTooLarge)
	}
	switch t := v.(type) {
	case map[string]any:
		if propsRaw, ok := t["properties"]; ok && propsRaw != nil {
			props, ok := propsRaw.(map[string]any)
			if !ok {
				return fmt.Errorf("mcp: tool %q schema properties must be an object: %w",
					quoteBounded(toolName), ErrInvalidTool)
			}
			if len(props) > MaxSchemaProperties {
				return fmt.Errorf("mcp: tool %q schema has %d properties (max %d): %w",
					quoteBounded(toolName), len(props), MaxSchemaProperties, ErrTooLarge)
			}
			for key, sub := range props {
				if len(key) > MaxSchemaStringBytes {
					return fmt.Errorf("mcp: tool %q schema property name exceeds %d bytes: %w",
						quoteBounded(toolName), MaxSchemaStringBytes, ErrTooLarge)
				}
				if err := checkSchemaShape(sub, depth+1, toolName); err != nil {
					return err
				}
			}
		}
		if enumRaw, ok := t["enum"]; ok && enumRaw != nil {
			items, ok := enumRaw.([]any)
			if !ok {
				return fmt.Errorf("mcp: tool %q schema enum must be an array: %w",
					quoteBounded(toolName), ErrInvalidTool)
			}
			if len(items) > MaxSchemaEnumEntries {
				return fmt.Errorf("mcp: tool %q schema enum has %d entries (max %d): %w",
					quoteBounded(toolName), len(items), MaxSchemaEnumEntries, ErrTooLarge)
			}
			for _, item := range items {
				if s, ok := item.(string); ok && len(s) > MaxSchemaStringBytes {
					return fmt.Errorf("mcp: tool %q schema enum entry exceeds %d bytes: %w",
						quoteBounded(toolName), MaxSchemaStringBytes, ErrTooLarge)
				}
			}
		}
		for _, key := range []string{"items", "additionalProperties"} {
			if sub, ok := t[key]; ok && sub != nil {
				if subMap, ok := sub.(map[string]any); ok {
					if err := checkSchemaShape(subMap, depth+1, toolName); err != nil {
						return err
					}
				}
			}
		}
	case []any:
		for _, item := range t {
			if err := checkSchemaShape(item, depth+1, toolName); err != nil {
				return err
			}
		}
	case string:
		if len(t) > MaxSchemaStringBytes {
			return fmt.Errorf("mcp: tool %q schema string exceeds %d bytes: %w",
				quoteBounded(toolName), MaxSchemaStringBytes, ErrTooLarge)
		}
	}
	return nil
}

// secretMarkers flags configuration keys whose values deserve warning and
// redaction-registration treatment at runtime. Matching is a case-
// insensitive substring test by design: it favors catching KEY/TOKEN/
// SECRET variants over precision, and it names keys only, never values.
var secretMarkers = []string{"KEY", "TOKEN", "SECRET"}

// SecretLookingKey reports whether an environment key looks secret-bearing.
// The runtime layer warns on such keys and registers inline values with
// the centralized redaction registry; this helper only classifies.
func SecretLookingKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, m := range secretMarkers {
		if strings.Contains(upper, m) {
			return true
		}
	}
	return false
}

// BoundedWarning truncates a per-tool skip reason for status snapshots so
// one hostile definition cannot flood diagnostics. Reasons name the tool
// and the violated bound; they never carry schemas or payloads.
func BoundedWarning(reason string) string {
	return BoundedDetail(reason)
}
