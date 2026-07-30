package cmd

import (
	"fmt"
	"strings"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"

	"github.com/spf13/cobra"
)

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

    response, err := runtime.Run([]providers.Message{
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