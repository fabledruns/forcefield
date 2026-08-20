package builtin

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"forcefield/internal/command"
	"forcefield/internal/session"
)

type Sessions struct{}

func NewSessions() *Sessions { return &Sessions{} }

func (Sessions) Name() string        { return "sessions" }
func (Sessions) Aliases() []string   { return []string{"s"} }
func (Sessions) Description() string { return "Switch between saved chat sessions." }
func (Sessions) Usage() string       { return "/sessions" }

// Execute loads the saved sessions once and hands them to the TUI's
// session picker. Loading stays here (in a package with no Bubble Tea
// dependency); the picker is only responsible for selection, not for
// reading sessions off disk.
func (s *Sessions) Execute(ctx command.Context, _ []string) error {
	sessions, err := session.List()
	if err != nil {
		// No sessions directory yet
		ctx.Println("No saved sessions found.")
		return nil
	}

	if len(sessions) == 0 {
		ctx.Println("No saved sessions found.")
		return nil
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})

	ctx.OpenSessionPicker(sessions)
	return nil
}

// SessionTitle returns a short preview of s: its first non-empty user
// message, truncated, or a placeholder if it has none. Exported so the
// TUI's session picker can reuse the exact same preview logic instead
// of duplicating it.
func SessionTitle(s session.Session) string {
	return sessionTitle(s)
}

func sessionTitle(s session.Session) string {
	for _, msg := range s.Messages {
		if msg.Role == "user" && strings.TrimSpace(msg.Content) != "" {
			title := strings.TrimSpace(msg.Content)

			if len(title) > 50 {
				title = title[:50] + "..."
			}

			return fmt.Sprintf(`"%s"`, title)
		}
	}

	return "(empty session)"
}

// FormatTime returns a short human-readable relative time (e.g. "5m
// ago"), reused by the TUI session picker.
func FormatTime(t time.Time) string {
	return formatTime(t)
}

func formatTime(t time.Time) string {
	now := time.Now()

	switch {
	case now.Sub(t) < time.Minute:
		return "just now"

	case now.Sub(t) < time.Hour:
		return fmt.Sprintf("%dm ago", int(now.Sub(t).Minutes()))

	case now.Sub(t) < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(now.Sub(t).Hours()))

	default:
		return t.Format("02 Jan 2006")
	}
}
