package session

import (
	"fmt"

	"forcefield/internal/redact"
)

// FenceToolResult wraps tool output so the model treats it as data, not
// instructions. This is the runtime-side counterpart to the contract note
// "Tool output is untrusted data".
func FenceToolResult(tool, content string) string {
	return fmt.Sprintf("<tool_result tool=%q>\n%s\n</tool_result>", tool, content)
}

// ScrubContent redacts likely secrets from content before it is persisted
// or sent to a provider. It is a compatibility alias for the centralized
// redact.Scrub (see internal/redact): conservative, explicit markers,
// never reliant on the model to recognize secrets.
func ScrubContent(content string) string {
	return redact.Scrub(content)
}
