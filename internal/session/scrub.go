package session

import (
	"fmt"
	"strings"

	"forcefield/internal/redact"
)

// FenceToolResult wraps tool output so the model treats it as data, not
// instructions. This is the runtime-side counterpart to the contract note
// "Tool output is untrusted data". Content is escaped so a model-controlled
// closing tag cannot break out of the fence: any literal </tool_result> in
// content is rewritten to a visually identical but inert form before
// wrapping, keeping the envelope exactly one block.
func FenceToolResult(tool, content string) string {
	escaped := strings.ReplaceAll(content, "</tool_result>", "<\\/tool_result>")
	return fmt.Sprintf("<tool_result tool=%q>\n%s\n</tool_result>", tool, escaped)
}

// ScrubContent redacts likely secrets from content before it is persisted
// or sent to a provider. It is a compatibility alias for the centralized
// redact.Scrub (see internal/redact): conservative, explicit markers,
// never reliant on the model to recognize secrets.
func ScrubContent(content string) string {
	return redact.Scrub(content)
}
