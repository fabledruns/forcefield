package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"forcefield/internal/tui"
)

var rootCmd = &cobra.Command{
    Use:   "ff",
    Short: "A local-first agent harness.",
    Long: `Forcefield is a local-first agent harness and runtime.

It lets you chat with local models, execute tools, and build
AI-powered workflows without requiring cloud services.`,

    // Running `ff` with no subcommand drops straight into the interactive
    // chat session, the same one `ff chat` starts explicitly.
    RunE: func(cmd *cobra.Command, args []string) error {
        return tui.Start()
    },
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	
}
