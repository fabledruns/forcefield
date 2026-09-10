package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"forcefield/internal/config"
	"forcefield/internal/providers"
	"forcefield/internal/recovery"
	"forcefield/internal/runtime"
	"forcefield/internal/session"

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

// resumeSessionID and resumeMaxTurns back the --resume/--max-turns flags.
// Package vars (like agentFlag) so Args validation and tests observe them.
var resumeSessionID string
var resumeMaxTurns int

// osExit and resumeRunner are seams so tests can observe exit codes and
// stub the resume path without spawning processes or providers.
var osExit = os.Exit
var resumeRunner = runResumeSession

var runCmd = &cobra.Command{
	Use:   "run [task]",
	Short: "Run a one-shot prompt",
	Long: `Run a one-shot prompt through the agent loop and print the final response.

With --resume <session-id>, continue an existing session headlessly instead:
the session is healed with the standard recovery semantics, its history is
replayed, and the run continues without the TUI. The process exit code
follows the recovery contract (internal/recovery): 0 completed, 2 terminal
failure or runtime-enforced stop, 3 retryable interruption (safe for a
future supervisor to restart), 4 cancelled or stalled on approvals. This
command never restarts itself.`,
	Args: func(cmd *cobra.Command, args []string) error {
		if resumeSessionID != "" {
			// Resume continues persisted history; there is no new task.
			return cobra.NoArgs(cmd, args)
		}
		return cobra.MinimumNArgs(1)(cmd, args)
	},

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

	// Headless resume: continue a persisted session without the TUI.
	if resumeSessionID != "" {
		code, err := resumeRunner(ctx, resumeSessionID, resumeMaxTurns)
		if err == nil {
			return nil
		}
		if code == 1 {
			// Setup/usage failure: let cobra report it (exit 1).
			return err
		}
		fmt.Fprintln(os.Stderr, err.Error())
		osExit(code)
		return nil
	}

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

// runResumeSession continues an existing session headlessly: load, heal
// with the standard recovery semantics, align the agent, replay history,
// and drive the event stream through the shared session driver. It
// returns the Phase 0 exit code; a nil error means completed (the final
// response is already printed), otherwise the error describes the
// outcome for stderr. Code 1 marks setup failures (bad flags, unreadable
// session/config); codes 2-4 are run outcomes. It never restarts itself.
func runResumeSession(ctx context.Context, resumeID string, maxTurns int) (int, error) {
	if maxTurns < 0 {
		return 1, fmt.Errorf("invalid --max-turns %d: must be >= 0 (0 keeps the configured bound)", maxTurns)
	}
	cfg, err := config.Load()
	if err != nil {
		return 1, err
	}
	sess, err := session.Load(resumeID)
	if err != nil {
		return 1, err
	}

	// CLI --agent wins over the resumed session, mirroring ff/ff chat.
	if agentFlag != "" {
		sess.Agent = agentFlag
		_ = sess.Save()
	}

	// --max-turns is an upper bound over configuration, never a raise:
	// cap the global and every per-agent profile so the effective
	// iteration limit cannot exceed it through any resolution path.
	if maxTurns > 0 {
		if cfg.Agent.MaxIterations <= 0 || cfg.Agent.MaxIterations > maxTurns {
			cfg.Agent.MaxIterations = maxTurns
		}
		for name, profile := range cfg.Agents {
			if profile.MaxIterations <= 0 || profile.MaxIterations > maxTurns {
				profile.MaxIterations = maxTurns
				cfg.Agents[name] = profile
			}
		}
	}

	rt, err := runtime.NewFromConfig(cfg)
	if err != nil {
		return 1, err
	}
	recovery.Heal(sess)
	recovery.AlignAgent(rt, sess)

	events, err := rt.StreamChat(ctx, sess.ProviderMessages())
	if err != nil {
		return 1, err
	}
	driver := recovery.NewDriver(sess)
	for event := range events {
		driver.HandleEvent(event)
	}

	code := driver.ExitCode(ctx.Err())
	if code == recovery.ExitOK {
		// Episode over successfully: drop any supervised-restart
		// lifecycle so a later episode starts clean.
		recovery.ClearSupervisor(sess)
		if response, ok := driver.FinalResponse(); ok {
			fmt.Println(response.Content)
		}
		return code, nil
	}
	if code == recovery.ExitTerminal || code == recovery.ExitNeedsHuman {
		// Episode over for a non-retryable reason (including quota/auth
		// and denials, which classify here): terminal outcomes never
		// accumulate retry state. Retryable outcomes keep it.
		recovery.ClearSupervisor(sess)
	}
	return code, resumeOutcomeError(resumeID, driver)
}

// resumeOutcomeError describes a non-zero resume outcome for stderr,
// using the terminal event and error the driver observed.
func resumeOutcomeError(resumeID string, driver *recovery.Driver) error {
	prefix := fmt.Sprintf("ff run --resume %s", resumeID)
	eventType, err, ok := driver.Outcome()
	if !ok {
		return fmt.Errorf("%s ended without a terminal event", prefix)
	}
	switch eventType {
	case runtime.EventBlocked:
		return fmt.Errorf("%s stopped: %v", prefix, err)
	case runtime.EventCancelled:
		return fmt.Errorf("%s cancelled", prefix)
	case runtime.EventError:
		if driver.Stats().DeniedOnly() {
			return fmt.Errorf("%s stalled: every tool call was denied, resume after approving permissions", prefix)
		}
		if err != nil {
			return fmt.Errorf("%s failed: %v", prefix, err)
		}
		return fmt.Errorf("%s failed", prefix)
	default:
		return fmt.Errorf("%s ended unexpectedly", prefix)
	}
}

func init() {
	runCmd.Flags().StringVar(
		&resumeSessionID,
		"resume",
		"",
		"continue an existing session headlessly (no task argument)",
	)
	runCmd.Flags().IntVar(
		&resumeMaxTurns,
		"max-turns",
		0,
		"cap model-turn iterations for this run (0 keeps the configured bound)",
	)
	rootCmd.AddCommand(runCmd)
}
