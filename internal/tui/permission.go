package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

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
	m.hoverID = ""
	return m, true
}

// summary renders the one-line transcript entry announcing the request,
// e.g. `permission requested: shell "go test ./..." in C:\repo`.
func (p *permissionPrompt) summary() string {
	return "permission requested: " + p.actionDescription()
}

// permOption is one clickable/pressable answer on the permission footer.
// The same definitions drive both rendering and hit-testing, so the click
// targets are always exactly the labels the user sees.
type permOption struct {
	key   string // the keyboard equivalent; clicking performs this key's action
	label string
}

// permissionOptions lists the answers in display order.
func permissionOptions() []permOption {
	return []permOption{
		{"y", "yes"},
		{"n", "no"},
		{"a", "always allow"},
		{"d", "always deny"},
	}
}

// permOptionGap separates answer labels on the options line.
const permOptionGap = "   "

// footerPrompt renders the question shown in place of the input box while
// this prompt is open. The execution block comes from the executor's own
// Enforcement report, so what it says about isolation is exactly what the
// runtime enforces - never hardcoded claims. The answer labels sit on a
// dedicated final line where each one is an explicit click target
// performing precisely its keyboard equivalent; there is deliberately no
// big default button, keeping confirmation as deliberate as the keys.
func (p *permissionPrompt) footerPrompt(hoveredKey string) string {
	text := permissionQuestionStyle.Render(p.actionDescription())

	if p.request.Execution != nil {
		text += "\n" + permissionHelpStyle.Render(
			strings.Join(p.request.Execution.SummaryLines(), "\n"))
	} else if risk := riskNote(p.request.Tool); risk != "" {
		text += "\n" + permissionHelpStyle.Render(risk)
	}

	return text + "\n" + renderPermissionOptions(hoveredKey) +
		permissionHelpStyle.Render("   (esc) no")
}

// renderPermissionOptions draws the answer labels, emphasizing the one
// under the pointer when mouse support is active.
func renderPermissionOptions(hoveredKey string) string {
	parts := make([]string, 0, len(permissionOptions()))
	for _, o := range permissionOptions() {
		label := "(" + o.key + ") " + o.label
		if o.key == hoveredKey {
			label = permOptionHoverStyle.Render(label)
		} else {
			label = permissionHelpStyle.Render(label)
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, permOptionGap)
}

// actionDescription describes what the tool wants to do in words a human
// can read at a glance, instead of dumping raw JSON arguments. Tools
// without a known argument shape fall back to their full arguments so
// nothing about the request is hidden.
func (p *permissionPrompt) actionDescription() string {
	args := p.request.Arguments
	switch p.request.Tool {
	case "shell":
		cmd, _ := args["command"].(string)
		cwd, _ := args["cwd"].(string)
		if cmd == "" && cwd == "" {
			break
		}
		var b strings.Builder
		b.WriteString("shell ")
		if cmd != "" {
			b.WriteString(quoteCommand(cmd))
		}
		if cwd != "" {
			fmt.Fprintf(&b, " in %s", cwd)
		}
		return b.String()
	case "read_file", "write_file":
		if path, ok := args["path"].(string); ok && path != "" {
			return fmt.Sprintf("%s %s", p.request.Tool, path)
		}
	case "list_files":
		path, _ := args["path"].(string)
		if path == "" {
			return "list_files ."
		}
		return "list_files " + path
	}
	return formatToolCallSummary(p.request.Tool, args)
}

// riskNote returns an honest, one-line description of what approving this
// tool allows when no executor report exists. It deliberately makes no
// isolation claims: Forcefield does not sandbox native execution, and the
// text must not pretend it does.
func riskNote(tool string) string {
	switch tool {
	case "shell":
		return "runs a command on this machine with your user's permissions"
	case "write_file":
		return "creates or overwrites a file on disk"
	default:
		return ""
	}
}

// formatToolCallSummary is the JSON fallback used for tools whose
// arguments have no friendlier rendering.
func formatToolCallSummary(tool string, args map[string]any) string {
	raw, err := json.Marshal(args)
	if err != nil || string(raw) == "null" {
		raw = []byte("{}")
	}
	return tool + " " + string(raw)
}

// formatPermissionAnswer renders the transcript activity line recording
// how a request was answered.
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

// permHoverKey returns the answer key whose label is under the pointer,
// derived from the current hover region id.
func (m model) permHoverKey() string {
	const prefix = "perm:"
	if m.mouseEnabled && strings.HasPrefix(m.hoverID, prefix) {
		return strings.TrimPrefix(m.hoverID, prefix)
	}
	return ""
}

// permissionOptionRects returns the hit rectangles of the answer labels.
// Geometry lives here rather than in View so hit-testing and rendering
// share one source of truth: the labels are drawn on the last content row
// of the prompt box. The box has top/bottom borders only (no side
// columns) plus one cell of left padding, so a label's screen column is
// exactly its index within that row.
func (m model) permissionOptionRects() []indexedRect {
	opts := permissionOptions()
	y := m.height - permOptionsRowFromBottom

	x := inputBorderStyle.GetPaddingLeft()
	rects := make([]indexedRect, 0, len(opts))
	for i, o := range opts {
		w := lipgloss.Width("(" + o.key + ") " + o.label)
		rects = append(rects, indexedRect{index: i, key: o.key, Rect: Rect{X: x, Y: y, W: w, H: 1}})
		x += w + lipgloss.Width(permOptionGap)
	}
	return rects
}

// permOptionsRowFromBottom locates the options row relative to the screen
// bottom: one row for the help line below the box, one for the box's
// bottom border, then the last content row of the box.
const permOptionsRowFromBottom = 3

// indexedRect pairs a hit rectangle with its payload for ordered lookups.
type indexedRect struct {
	index int
	key   string
	Rect
}
