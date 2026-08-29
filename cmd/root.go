package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"forcefield/internal/session"
	"forcefield/internal/tui"
)

var resumeID string

// Version is the binary version, set at build time via ldflags:
// go build -ldflags "-X forcefield/cmd.Version=v1.2.3"
var Version = "dev"

// rootTuiStarter is a package var so tests can inject a fake.
var rootTuiStarter = tui.Start
var loadSession = session.Load
var newSession = session.New

var rootCmd = &cobra.Command{
	Use:   "ff",
	Short: "A local-first agent harness.",
	Long: `Forcefield is a local-first agent harness and runtime.

It lets you chat with local models, execute tools, and build
AI-powered workflows without requiring cloud services.`,
	Version: Version,

	// Running `ff` with no subcommand drops straight into the interactive
	// chat session, the same one `ff chat` starts explicitly.
	RunE: func(cmd *cobra.Command, args []string) error {
		var sess *session.Session

		if resumeID != "" {
			loaded, err := loadSession(resumeID)
			if err != nil {
				return err
			}
			sess = loaded
		} else {
			sess = newSession()
		}

		return rootTuiStarter(sess)
	},
}

func Execute() {
	// Ensure the version displayed by --version reflects any ldflags
	// injection via main.Version -> cmd.Version.
	rootCmd.Version = Version
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	// Sync Version field in case ldflags sets it after var initialization.
	rootCmd.Version = Version
	rootCmd.Flags().StringVar(
		&resumeID,
		"resume",
		"",
		"resume an existing session",
	)
}
