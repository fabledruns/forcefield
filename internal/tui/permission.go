package tui

import (
	"encoding/json"
	"strings"

	"forcefield/internal/permissions"
)

// permissionPrompt tracks a single pending "ask" permission decision the
// user needs to answer before the scheduler can proceed. Only one is ever
// pending at a time: the scheduler goroutine blocks on Ask() until the
// respond channel receives an answer, so a second request can't arrive
// while this one is still open.
//
// Unlike picker/selectPicker, a pending prompt does NOT take over the
// screen: the transcript and header stay visible, the prompt appears as
// a normal activity line in the transcript, and the footer's input box
// is temporarily replaced by the y/n/a/d question. See renderFooter and
// View in model.go.
type permissionPrompt struct {
	request permissions.Request
	respond chan<- permissions.Prompt
}

// permissionRequestMsg is sent into the bubbletea program by the runtime's
// tuiAsker (running on the scheduler's own goroutine) to hand off a
// permission decision to the UI thread.
type permissionRequestMsg struct {
	request permissions.Request
	respond chan<- permissions.Prompt
}

// handlePermissionKey answers the pending permission prompt, if any. It
// returns handled=false when there's no prompt open or the key isn't one
// of the recognized answers, so callers can fall back to normal key
// handling.
func (m model) handlePermissionKey(key string) (model, bool) {
	if m.permissionPrompt == nil {
		return m, false
	}

	var answer permissions.Prompt
	switch key {
	case "y":
		answer = permissions.PromptAllowOnce
	case "n", "esc":
		answer = permissions.PromptDenyOnce
	case "a":
		answer = permissions.PromptAlwaysAllow
	case "d":
		answer = permissions.PromptAlwaysDeny
	default:
		return m, false
	}

	m.permissionPrompt.respond <- answer
	m.appendActivity(formatPermissionAnswer(m.permissionPrompt.request, answer))
	m.permissionPrompt = nil
	return m, true
}

// summary renders the one-line transcript entry announcing the request,
// e.g. `shell wants permission ({"command":"rm -rf build"})`.
func (p *permissionPrompt) summary() string {
	return "permission requested: " + formatToolCallSummary(p.request.Tool, p.request.Arguments)
}

// footerPrompt renders the question shown in place of the input box
// while this prompt is open.
func (p *permissionPrompt) footerPrompt() string {
	return permissionQuestionStyle.Render(formatToolCallSummary(p.request.Tool, p.request.Arguments)) +
		"  " + permissionHelpStyle.Render("Allow? (y) yes  (n) no  (a) always allow  (d) always deny  (esc) no")
}

func formatToolCallSummary(tool string, args map[string]any) string {
	raw, err := json.Marshal(args)
	if err != nil || string(raw) == "null" {
		raw = []byte("{}")
	}
	return tool + " " + string(raw)
}

func formatPermissionAnswer(req permissions.Request, answer permissions.Prompt) string {
	var verdict string
	switch answer {
	case permissions.PromptAllowOnce:
		verdict = "allowed (once)"
	case permissions.PromptDenyOnce:
		verdict = "denied (once)"
	case permissions.PromptAlwaysAllow:
		verdict = "always allowed"
	case permissions.PromptAlwaysDeny:
		verdict = "always denied"
	}
	return strings.TrimSpace(req.Tool + ": " + verdict)
}
