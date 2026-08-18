package tui

import "strings"

// Icon is a named glyph from a closed set. All icons share one visual
// language: geometric and typographic symbols that render in a typical
// developer terminal. They are not emoji and not Nerd Font private-use
// characters.
type Icon string

func (i Icon) String() string { return string(i) }

const (
	IconPrompt    Icon = "›"
	IconCollapsed Icon = "▸"
	IconExpanded  Icon = "▾"

	IconSuccess   Icon = "✓"
	IconFailure   Icon = "✗"
	IconWarning   Icon = "!"
	IconCancel    Icon = "⊘"
	IconRunning   Icon = "●"
	IconIdle      Icon = "○"
	IconThink     Icon = "◌"

	IconPipe      Icon = "│"
	IconSep       Icon = "·"
	IconEllipsis  Icon = "…"

	IconShell     Icon = "⌁"
	IconFile      Icon = "□"
	IconGit       Icon = "◇"
	IconSearch    Icon = "⌕"
	IconSettings  Icon = "⚙"
	IconMemory    Icon = "▤"
	IconModel     Icon = "◈"
	IconSession   Icon = "◉"
	IconSkill     Icon = "✦"
	IconTool      Icon = "◆"
)

// iconForTool returns the kind icon for a tool name. Unknown tools get
// the generic execution mark rather than a one-off glyph.
func iconForTool(name string) Icon {
	switch name {
	case "shell", "pwd":
		return IconShell
	case "read_file", "write_file", "list_files":
		return IconFile
	case "load_skill":
		return IconSkill
	case "add_project_memory":
		return IconMemory
	}

	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "git"):
		return IconGit
	case strings.Contains(lower, "search"),
		strings.Contains(lower, "grep"),
		strings.Contains(lower, "find"):
		return IconSearch
	case strings.Contains(lower, "memory"):
		return IconMemory
	default:
		return IconTool
	}
}

// activityLabelForTool is the high-level thinking step shown while a
// tool of this name is running. It is a status phrase, not a log line.
func activityLabelForTool(name string) string {
	switch name {
	case "read_file", "list_files", "pwd":
		return "Inspecting files..."
	case "write_file":
		return "Writing files..."
	case "shell":
		return "Running command..."
	case "load_skill":
		return "Loading skill..."
	case "add_project_memory":
		return "Updating memory..."
	default:
		lower := strings.ToLower(name)
		switch {
		case strings.Contains(lower, "git"):
			return "Checking git..."
		case strings.Contains(lower, "search"),
			strings.Contains(lower, "grep"),
			strings.Contains(lower, "find"):
			return "Searching..."
		default:
			return "Working..."
		}
	}
}
