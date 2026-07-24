package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
    Use:   "ff",
    Short: "A local-first agent harness.",
    Long: `Forcefield is a local-first agent harness and runtime.

It lets you chat with local models, execute tools, and build
AI-powered workflows without requiring cloud services.`,
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	
}
