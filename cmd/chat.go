package cmd

import (
	"github.com/spf13/cobra"

	"forcefield/internal/session"
	"forcefield/internal/tui"
)

// chatStarter and newChatSession are package vars so tests can inject
// fakes without redesigning the command architecture.
var chatStarter = tui.Start
var newChatSession = session.New

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Start an interactive chat session",
	Long: `Chat opens an interactive terminal UI for talking to your configured
agent. It sends each message through the same runtime.Run call that
"ff run" uses — one request per message, no added memory — wrapped in a
scrollable, persistent session instead of one command per question.`,
	Args: cobra.NoArgs,

	RunE: func(cmd *cobra.Command, args []string) error {
		var sess *session.Session

		// Mirror the root command's --resume path: without this, the
		// flag parses but is silently ignored and the user loses the
		// session they asked to resume.
		if resumeID != "" {
			loaded, err := loadSession(resumeID)
			if err != nil {
				return err
			}
			sess = loaded
		} else {
			sess = newChatSession()
		}
		if agentFlag != "" {
			sess.Agent = agentFlag
			_ = sess.Save()
		}
		return chatStarter(sess)
	},
}

func init() {
	// Register --resume on the shared resumeID var so `ff chat --resume
	// <id>` enters the same resume path as `ff --resume <id>`. The root
	// flag is command-local (not persistent), so without this the flag
	// would be rejected here; binding the same var keeps one source of
	// truth for which session to resume.
	chatCmd.Flags().StringVar(
		&resumeID,
		"resume",
		"",
		"resume an existing session",
	)
	rootCmd.AddCommand(chatCmd)
}
