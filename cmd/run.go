package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"forcefield/internal/providers"
	"forcefield/internal/runtime"

	"github.com/spf13/cobra"
)

// runtimeRun is a package var so tests can inject a fake without
// redesigning the command architecture. Production creates a runtime and
// runs with the caller's context so SIGINT/SIGTERM cancels the operation
// instead of leaving tools or provider streams running.
var runtimeRun = func(ctx context.Context, msgs []providers.Message) (providers.Response, error) {
	rt, err := runtime.New()
	if err != nil {
		return providers.Response{}, err
	}
	return rt.RunContext(ctx, msgs)
}

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

	// Cancel the run on SIGINT/SIGTERM so Ctrl+C stops the provider
	// request, tool execution, and shell processes through the same
	// context chain the TUI uses, instead of killing the process
	// mid-write with a half-persisted session.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// When --agent is set, we need a runtime instance to switch agents.
	if agentFlag != "" {
		rt, err := runtimeNew()
		if err != nil {
			return err
		}
		if err := rt.SetAgent(agentFlag); err != nil {
			return err
		}
		response, err := rt.RunContext(ctx, []providers.Message{
			{Role: providers.UserRole, Content: task},
		})
		if err != nil {
			return mapRunError(err)
		}
		fmt.Println(response.Content)
		return nil
	}

	response, err := runtimeRun(ctx, []providers.Message{
		{
			Role:    providers.UserRole,
			Content: task,
		},
	})
	if err != nil {
		return mapRunError(err)
	}

	fmt.Println(response.Content)
	return nil
}

// mapRunError reports cancellation plainly (exit 1 via cobra) while
// preserving the context error for errors.Is callers.
func mapRunError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("operation cancelled: %w", err)
	}
	return err
}

func init() {
	rootCmd.AddCommand(runCmd)
}
