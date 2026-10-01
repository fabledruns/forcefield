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
// instead of leaving tools or provider streams running. The status is
// returned alongside the response so the process exit code reflects
// verification: only a verified completion exits 0. The warnings carry
// pull-based MCP integration notes (dead servers, missing tools) for
// stderr reporting; headless runs have no /mcp surface.
var runtimeRun = func(ctx context.Context, msgs []providers.Message) (providers.Response, runtime.Status, []string, error) {
	rt, err := runtime.New()
	if err != nil {
		return providers.Response{}, "", nil, err
	}
	resp, status, err := rt.RunContextWithStatus(ctx, msgs)
	return resp, status, rt.MCPWarnings(), err
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

The exit code reflects verification: 0 only when the run establishes a
verified completion (plain answers count as verified); 5 when the model
finished without verification. Supervisors and pipelines must treat 5
as unreviewed output, not success.

With --resume <session-id>, continue an existing session headlessly instead:
the session is healed with the standard recovery semantics, its history is
replayed, and the run continues without the TUI. The process exit code
follows the recovery contract (internal/recovery): 0 verified completion,
2 terminal failure or runtime-enforced stop, 3 retryable interruption (safe
for a future supervisor to restart), 4 cancelled or stalled on approvals,
5 finished without verification (output printed, but unverified — needs
human review, never auto-restarted). This command never restarts itself.`,
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
		// Reap background work (shell jobs, MCP servers) on the way
		// out so headless runs never orphan helper processes.
		defer func() { _ = rt.Close() }()
		if err := rt.SetAgent(agentFlag); err != nil {
			return err
		}
		response, status, err := rt.RunContextWithStatus(ctx, []providers.Message{
			{Role: providers.UserRole, Content: task},
		})
		if err != nil {
			return mapRunError(err)
		}
		reportMCPWarnings(rt.MCPWarnings())
		fmt.Println(response.Content)
		return exitForRunStatus(status)
	}

	response, status, warns, err := runtimeRun(ctx, []providers.Message{
		{
			Role:    providers.UserRole,
			Content: task,
		},
	})
	if err != nil {
		return mapRunError(err)
	}

	reportMCPWarnings(warns)
	fmt.Println(response.Content)
	return exitForRunStatus(status)
}

// reportMCPWarnings surfaces pull-based MCP integration warnings on
// stderr for headless runs: without the TUI's /mcp surface, a dead
// server or a requested-but-missing tool would otherwise stay invisible
// (only .forcefield/mcp-status.json records it). Stdout stays clean for
// piping; no warnings means no output.
func reportMCPWarnings(warns []string) {
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "ff run: mcp warning: %s\n", w)
	}
}

// exitForRunStatus maps the terminal verification outcome to the
// process result. Verified completions (including plain chat, which
// FinalStatus reports as verified) succeed; anything else the model
// finished without verifying exits unverified so supervisors and
// pipelines never mistake output for success. The content is already
// printed; only the exit code carries the verdict.
func exitForRunStatus(status runtime.Status) error {
	if status == runtime.StatusVerified {
		return nil
	}
	fmt.Fprintf(os.Stderr, "ff run finished without verification (status %q); output above is unverified\n", string(status))
	osExit(recovery.ExitUnverified)
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
	// MCP servers start with the runtime: surface any startup failures
	// now, before model output, so a dead server is visible even though
	// headless runs have no /mcp surface.
	reportMCPWarnings(rt.MCPWarnings())
	recovery.Heal(sess)
	recovery.AlignAgent(rt, sess)
	// Adoption writes (heal + agent alignment) must reach disk before
	// the run continues: replaying history the file does not contain
	// would fork memory from durability on the first event.
	if err := saveGateError(sess, resumeID); err != nil {
		return recovery.ExitTerminal, err
	}

	// The driver owns a cancel scoped to this run: the first
	// lifecycle-save failure stops the runtime loop (which exits at its
	// next cancellation check without starting new tools) instead of
	// letting execution advance against a stale file. Signal teardown
	// still propagates through the parent context.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := rt.StreamChat(runCtx, sess.ProviderMessages())
	if err != nil {
		return 1, err
	}
	driver := recovery.NewDriver(sess)
	driver.OnPersistFailure(cancel)
	for event := range events {
		driver.HandleEvent(event)
	}

	// A persistence failure anywhere in the run overrides the outcome:
	// even a verified Done is unusable when its record never reached
	// disk, and the supervisor must park rather than treat it as
	// success or retryable progress.
	if driver.PersistFailed() {
		return recovery.ExitTerminal, fmt.Errorf("ff run --resume %s failed: session save failed (%s)", resumeID, driver.PersistErr())
	}
	code := driver.ExitCode(runCtx.Err())
	return finishResumeSession(sess, driver, resumeID, code)
}

// saveGateError reports a session-persistence failure as a terminal run
// error, or nil when the session file is healthy. It reads the sticky
// LastSaveError (in-memory only, never loaded from disk), so it gates
// exactly the saves this process attempted.
func saveGateError(sess *session.Session, resumeID string) error {
	if sess != nil && sess.LastSaveError != "" {
		return fmt.Errorf("ff run --resume %s failed: session save failed (%s)", resumeID, sess.LastSaveError)
	}
	return nil
}

// finishResumeSession applies the save-health gate, settles supervisor
// lifecycle, and reports the outcome for stderr. A failed final save
// means the file is stale even when the run otherwise completed: it
// reports terminal without clearing supervisor state, so a supervisor
// (or operator) retries instead of assuming success. A clean final
// save preserves the existing settle/report behavior exactly.
func finishResumeSession(sess *session.Session, driver *recovery.Driver, resumeID string, code int) (int, error) {
	if err := saveGateError(sess, resumeID); err != nil {
		return recovery.ExitTerminal, err
	}
	settleSupervisorEpisode(sess, code)
	// Lifecycle bookkeeping above saves too: re-check so a failed
	// episode-clear cannot pass a success through on a stale file.
	if err := saveGateError(sess, resumeID); err != nil {
		return recovery.ExitTerminal, err
	}
	if code == recovery.ExitOK {
		if response, ok := driver.FinalResponse(); ok {
			fmt.Println(response.Content)
		}
		return code, nil
	}
	return code, resumeOutcomeError(resumeID, driver)
}

// settleSupervisorEpisode drops supervised-restart lifecycle when a
// headless run reaches a terminal outcome (completed, terminal failure,
// or cancellation/denial — including quota/auth failures, which
// classify as terminal). Retryable interruptions and setup failures
// keep it: the episode is still open and a supervisor (or a fixed
// manual retry) may legitimately continue it.
func settleSupervisorEpisode(sess *session.Session, code int) {
	switch code {
	case recovery.ExitOK, recovery.ExitTerminal, recovery.ExitNeedsHuman, recovery.ExitUnverified:
		recovery.ClearSupervisor(sess)
	}
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
	case runtime.EventDone:
		// Reached only for non-zero codes: the run finished without
		// verification (see Classify). Name the status so the operator
		// knows the output needs review, not a retry.
		if status, ok := driver.FinalStatus(); ok {
			return fmt.Errorf("%s finished unverified (status %q): review the output before trusting it", prefix, string(status))
		}
		return fmt.Errorf("%s finished without verification", prefix)
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
