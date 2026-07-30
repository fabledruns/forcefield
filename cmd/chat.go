package cmd

import (
	"github.com/spf13/cobra"

	"forcefield/internal/session"
	"forcefield/internal/tui"
)

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Start an interactive chat session",
	Long: `Chat opens an interactive terminal UI for talking to your configured
agent. It sends each message through the same runtime.Run call that
"ff run" uses — one request per message, no added memory — wrapped in a
scrollable, persistent session instead of one command per question.`,
	Args: cobra.NoArgs,

	RunE: func(cmd *cobra.Command, args []string) error {
		return tui.Start(session.New())
	},
}

func init() {
	rootCmd.AddCommand(chatCmd)
}
