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
// is temporarily replaced by a selectable permission UI. See renderFooter
// and View in model.go.
type permissionPrompt struct {
	request  permissions.Request
	respond  chan<- permissions.Prompt
	selected int // index into permissionChoices, navigated with ↑/↓
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

	// Navigation
	switch key {
	case "up", "k":
		if m.permissionPrompt.selected > 0 {
			m.permissionPrompt.selected--
		} else {
			m.permissionPrompt.selected = len(permissionChoices) - 1
		}
		return m, true
	case "down", "j":
		if m.permissionPrompt.selected < len(permissionChoices)-1 {
			m.permissionPrompt.selected++
		} else {
			m.permissionPrompt.selected = 0
		}
		return m, true
	case "enter":
		choice := permissionChoices[m.permissionPrompt.selected]
		answer := choice.prompt
		m.permissionPrompt.respond <- answer
		m.appendActivity(formatPermissionAnswer(m.permissionPrompt.request, answer))
		m.permissionPrompt = nil
		m.hoverID = ""
		return m, true
	}

	var answer permissions.Prompt
	var matched bool
	switch key {
	case "y":
		answer = permissions.PromptAllowOnce
		matched = true
	case "n", "esc":
		answer = permissions.PromptDenyOnce
		matched = true
	case "a":
		answer = permissions.PromptAlwaysAllow
		matched = true
	case "d":
		answer = permissions.PromptAlwaysDeny
		matched = true
	default:
		return m, false
	}
	if !matched {
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

// permOption is one selectable answer in the permission UI.
type permOption struct {
	key    string // legacy key for hover/click mapping
	label  string
	prompt permissions.Prompt
}

// permissionChoices is the ordered list of permission actions presented as
// a vertical selectable list. The order matches the spec example.
var permissionChoices = []permOption{
	{"y", "Allow once", permissions.PromptAllowOnce},
	{"a", "Always allow", permissions.PromptAlwaysAllow},
	{"n", "Deny", permissions.PromptDenyOnce},
	{"d", "Always deny", permissions.PromptAlwaysDeny},
}

// permissionOptions retains the legacy name for tests that import it.
func permissionOptions() []permOption {
	return permissionChoices
}

// permOptionGap was used for the old horizontal layout; kept for
// compatibility but no longer used in the vertical UI.
const permOptionGap = "   "

// footerPrompt renders the permission UI shown in place of the input box
// while this prompt is open. It shows the tool name and a clean,
// readable block for its arguments (never raw JSON as primary), plus
// execution details from the executor's Enforcement report and a
// vertical selectable list. The list is navigated with ↑/↓ and confirmed
// with Enter, with mouse click support and Esc as quick deny.
func (p *permissionPrompt) footerPrompt(hoveredKey string) string {
	var b strings.Builder
	b.WriteString(permissionQuestionStyle.Render("Permission required"))
	b.WriteString("\n")
	b.WriteString(p.formatToolBlock())
	if p.request.Execution != nil {
		b.WriteString("\n")
		b.WriteString(permissionHelpStyle.Render(strings.Join(p.request.Execution.SummaryLines(), "\n")))
	} else if risk := riskNote(p.request.Tool); risk != "" {
		b.WriteString("\n")
		b.WriteString(permissionHelpStyle.Render(risk))
	}
	b.WriteString("\n\n")
	b.WriteString(p.renderOptions(hoveredKey))
	b.WriteString("\n")
	b.WriteString(permissionHelpStyle.Render("↑/↓ navigate · enter confirm · esc deny"))
	return b.String()
}

// renderOptions draws the vertical selectable list, highlighting the
// currently selected option and the hovered option.
func (p *permissionPrompt) renderOptions(hoveredKey string) string {
	var b strings.Builder
	for i, o := range permissionChoices {
		prefix := "  "
		style := permissionHelpStyle
		if i == p.selected {
			prefix = "› "
			style = pickerSelectedStyle
		}
		if o.key == hoveredKey {
			style = permOptionHoverStyle
		}
		// Underline hovered even when selected for extra feedback.
		label := prefix + o.label
		b.WriteString(style.Render(label))
		if i < len(permissionChoices)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderPermissionOptions retains compatibility for older callers.
func renderPermissionOptions(hoveredKey string) string {
	// Old horizontal rendering is replaced by vertical; delegate to the
	// prompt's method with a temporary prompt for tests that call it
	// directly.
	tmp := &permissionPrompt{selected: 0}
	return tmp.renderOptions(hoveredKey)
}

// formatToolBlock renders the tool name and its arguments as a clean,
// multi-line block. It avoids dumping raw JSON as the primary view; each
// argument is shown as `key: value` on its own line, sorted for
// determinism. Long string values are truncated.
func (p *permissionPrompt) formatToolBlock() string {
	var b strings.Builder
	b.WriteString(permissionHelpStyle.Render("Tool: " + p.request.Tool))
	args := p.request.Arguments
	if len(args) == 0 {
		return b.String()
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	// Deterministic order
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, k := range keys {
		v := args[k]
		var val string
		switch s := v.(type) {
		case string:
			// Truncate very long strings (e.g. write_file content)
			if len(s) > 300 {
				s = s[:300] + "…"
			}
			// Quote the string for clarity, but keep it readable
			if strings.Contains(s, "\n") {
				// Multi-line: show first line only in the block, full is in details on expand
				first := strings.SplitN(s, "\n", 2)[0]
				if len(first) > 80 {
					first = first[:80] + "…"
				}
				val = fmt.Sprintf("%q…", first)
			} else {
				val = fmt.Sprintf("%q", s)
			}
		default:
			raw, err := json.Marshal(v)
			if err != nil || string(raw) == "null" {
				raw = []byte("{}")
			}
			val = string(raw)
			if len(val) > 200 {
				val = val[:200] + "…"
			}
		}
		b.WriteString("\n")
		b.WriteString(permissionHelpStyle.Render(fmt.Sprintf("  %s: %s", k, val)))
	}
	return b.String()
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

// permissionOptionRects returns the hit rectangles for the vertical
// permission options. Geometry is shared with rendering so clicks land
// exactly where labels are drawn. The options are the 4 rows directly
// above the hint line inside the prompt box, which itself sits above
// the outer help line and border.
func (m model) permissionOptionRects() []indexedRect {
	// Vertical list: 4 options + hint line + border + outer help line.
	// First option is 7 rows from bottom, last is 4 from bottom.
	const firstRowFromBottom = 7
	rects := make([]indexedRect, 0, len(permissionChoices))
	baseY := m.height - firstRowFromBottom
	x := inputBorderStyle.GetPaddingLeft()
	for i, o := range permissionChoices {
		w := lipgloss.Width("  " + o.label)
		// Selected prefix "› " is same width as "  "
		rects = append(rects, indexedRect{index: i, key: o.key, Rect: Rect{X: x, Y: baseY + i, W: w, H: 1}})
	}
	return rects
}

// permOptionsRowFromBottom is kept for compatibility; new layout uses
// firstRowFromBottom = 7.
const permOptionsRowFromBottom = 3

// indexedRect pairs a hit rectangle with its payload for ordered lookups.
type indexedRect struct {
	index int
	key   string
	Rect
}
