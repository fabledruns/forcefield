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

func (Sessions) Name() string 		 { return "sessions" }
func (Sessions) Aliases() []string 	 { return []string{"s"} }
func (Sessions) Description() string { return "List saved chat sessions." }
func (Sessions) Usage() string 		 { return "/sessions" }

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

	var b strings.Builder

	b.WriteString("Sessions:\n\n")

	for i, sess := range sessions {
		title := sessionTitle(sess)

		fmt.Fprintf(
			&b,
			"  %d. %s\n",
			i+1,
			sess.ID,
		)

		fmt.Fprintf(
			&b,
			"     %s\n",
			title,
		)

		fmt.Fprintf(
			&b,
			"     %d messages • updated %s\n\n",
			len(sess.Messages),
			formatTime(sess.UpdatedAt),
		)
	}

	ctx.Println("%s", strings.TrimRight(b.String(), "\n"))
	return nil
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