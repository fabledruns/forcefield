package cmd

import (
	"fmt"
	"strings"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"

	"github.com/spf13/cobra"
)

// runtimeRun is a package var so tests can inject a fake without
// redesigning the command architecture. Production uses runtime.Run.
var runtimeRun = runtime.Run

// runtimeNew is a package var so tests can inject a fake runtime for
// agent-aware runs.
var runtimeNew = runtime.New

var runCmd = &cobra.Command{
	Use:   "run [task]",
	Short: "Run a one-shot prompt",
	Args:  cobra.MinimumNArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		return runCommand(args)
	},
}

func runCommand(args []string) error {
	task := strings.TrimSpace(strings.Join(args, " "))

	// When --agent is set, we need a runtime instance to switch agents.
	if agentFlag != "" {
		rt, err := runtimeNew()
		if err != nil {
			return err
		}
		if err := rt.SetAgent(agentFlag); err != nil {
			return err
		}
		response, err := rt.Run([]providers.Message{
			{Role: providers.UserRole, Content: task},
		})
		if err != nil {
			return err
		}
		fmt.Println(response.Content)
		return nil
	}

	response, err := runtimeRun([]providers.Message{
		{
			Role:    providers.UserRole,
			Content: task,
		},
	})
	if err != nil {
		return err
	}

	fmt.Println(response.Content)
	return nil
}

func init() {
	rootCmd.AddCommand(runCmd)
}
