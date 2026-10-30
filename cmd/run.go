package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"forcefield/internal/config"
	"forcefield/internal/orchevents"
	"forcefield/internal/providers"
	"forcefield/internal/recovery"
	"forcefield/internal/runtime"
	"forcefield/internal/session"

	"github.com/spf13/cobra"
)

// runtimeRun is a package var so tests can inject a fake without
// redesigning the command architecture. Production creates a runtime
// and runs the headless flow with the caller's context so SIGINT/SIGTERM
// cancels the operation instead of leaving tools or provider streams
// running. Since v1.6 the run is persisted as a session (resumable via
// --resume) with a structured event log beside it, so every headless
// run leaves local evidence of what happened. The status is returned
// alongside the response so the process exit code reflects
// verification: only a verified completion exits 0. The warnings carry
// pull-based MCP integration notes (dead servers, missing tools) for
// stderr reporting; headless runs have no /mcp surface.
var runtimeRun = func(ctx context.Context, msgs []providers.Message) (providers.Response, runtime.Status, []string, error) {
	rt, err := runtime.New()
	if err != nil {
		return providers.Response{}, "", nil, err
	}
	defer func() { _ = rt.Close() }()
	return runHeadlessSession(ctx, rt, msgs)
}

// runtimeNew is a package var so tests can inject a fake runtime for
// agent-aware runs.
var runtimeNew = runtime.New

// resumeSessionID and resumeMaxTurns back the --resume/--max-turns flags.
// Package vars (like agentFlag) so Args validation and tests observe them.
var resumeSessionID string
var resumeMaxTurns int

// osExit, resumeRunner, and newEventWriter are seams so tests can
// observe exit codes, stub the resume path, and inject event-log faults
// without spawning processes or external providers.
var osExit = os.Exit
var resumeRunner = runResumeSession
var newEventWriter = orchevents.NewWriter

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
		response, status, warns, err := runHeadlessSession(ctx, rt, []providers.Message{
			{Role: providers.UserRole, Content: task},
		})
		if err != nil {
			return mapRunError(err)
		}
		reportMCPWarnings(warns)
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

// runHeadlessSession executes a fresh headless agent run and persists
// its evidence: the session file (resumable by --resume) and a
// structured orchestration-event log beside it. The run folds through
// the same recovery driver the resume path uses, but the returned
// status stays in the plain-run vocabulary: only a verified completion
// maps to exit 0, an unverified or runtime-stopped finish maps to 5,
// and cancellations, failures, and save-gate breaches stay errors (1).
//
// Evidence failure can never upgrade an outcome: a persist failure
// fails the run through the same session-save gate the resume path
// applies, and a verified completion whose event log could not be
// written downgrades to unverified (exit 5) with the reason on stderr.
func runHeadlessSession(ctx context.Context, rt *runtime.Runtime, msgs []providers.Message) (providers.Response, runtime.Status, []string, error) {
	if len(msgs) == 0 {
		return providers.Response{}, "", nil, errors.New("no task given")
	}
	warns := rt.MCPWarnings()

	// Plain `ff run` historically ran a single throwaway turn without
	// any session on disk. Persisting first means every headless run is
	// resumable and every delegation shows up in the event log.
	sess := session.New()
	if agentFlag != "" {
		sess.Agent = agentFlag
	}
	taskChars := 0
	for _, m := range msgs {
		sess.AddProviderMessage(m)
		taskChars += len(m.Content)
	}
	if err := sess.Save(); err != nil {
		// Without this first save there is no evidence at all, not even
		// the record that the run happened: refuse to start.
		return providers.Response{}, "", warns, fmt.Errorf("ff run: persist session: %w", err)
	}

	evw, openErr := newEventWriter(sess.EventLogPath(), "")
	if openErr != nil {
		fmt.Fprintf(os.Stderr, "ff run: orchestration event log could not be opened (%v): %s\n", openErr, sess.EventLogPath())
	} else {
		defer evw.Close()
		rt.SetOrchestrationEventSink(evw)
		defer rt.SetOrchestrationEventSink(nil)
	}
	emit := func(e orchevents.Event) {
		if evw != nil {
			evw.Emit(e)
		}
	}

	emit(orchevents.Event{
		Type:      orchevents.RunStarted,
		Agent:     sess.Agent,
		TaskChars: taskChars,
	})

	start := time.Now()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := rt.StreamChat(runCtx, sess.ProviderMessages())
	if err != nil {
		exitCode := 1
		emit(orchevents.Event{
			Type:       orchevents.RunFailed,
			Outcome:    orchevents.OutcomeError,
			Status:     string(runtime.StatusFailed),
			DurationMS: time.Since(start).Milliseconds(),
			ExitCode:   &exitCode,
			Error:      err.Error(),
		})
		if evw != nil && openErr == nil {
			closeErr := evw.Close()
			if writeErr := eventLogWriteErr(evw, closeErr); writeErr != "" {
				fmt.Fprintf(os.Stderr, "ff run: orchestration event log write failed (%s); session evidence may be incomplete\n", writeErr)
			}
		}
		return providers.Response{}, "", warns, err
	}
	driver := recovery.NewDriver(sess)
	driver.OnPersistFailure(cancel)
	for ev := range events {
		driver.HandleEvent(ev)
		if mapped, ok := mapRunEvent(ev); ok {
			emit(mapped)
		}
	}

	// A persistence failure anywhere in the run overrides the outcome:
	// even a verified Done is unusable when its record never reached
	// disk. This is the same gate runResumeSession applies.
	if driver.PersistFailed() {
		err := fmt.Errorf("ff run: session save failed (%s)", driver.PersistErr())
		exitCode := 1
		emit(orchevents.Event{
			Type:       orchevents.RunFailed,
			Outcome:    orchevents.OutcomeError,
			Status:     string(runtime.StatusFailed),
			DurationMS: time.Since(start).Milliseconds(),
			ExitCode:   &exitCode,
			Error:      err.Error(),
		})
		if evw != nil && openErr == nil {
			closeErr := evw.Close()
			if writeErr := eventLogWriteErr(evw, closeErr); writeErr != "" {
				fmt.Fprintf(os.Stderr, "ff run: orchestration event log write failed (%s); session evidence may be incomplete\n", writeErr)
			}
		}
		return providers.Response{}, "", warns, err
	}

	// Terminal classification mirrors the plain run's historical exit
	// vocabulary: EventDone resolves through the verification status
	// (0 verified / 5 otherwise), a runtime-enforced stop (EventBlocked)
	// behaves like an unverified finish, and cancellation and errors
	// return as errors (exit 1 via mapRunError).
	kind, termErr, hasTerm := driver.Outcome()
	status, statusOK := driver.FinalStatus()
	if !statusOK {
		if hasTerm && kind == runtime.EventBlocked {
			status = runtime.StatusBlocked
		} else {
			status = runtime.StatusPartial
		}
	}
	resp, respOK := driver.FinalResponse()

	var finalType orchevents.Type
	finalOutcome := ""
	switch {
	case !hasTerm:
		err = errors.New("ff run ended without a terminal event")
		finalType, finalOutcome = orchevents.RunFailed, orchevents.OutcomeError
	case kind == runtime.EventDone:
		finalType, finalOutcome = orchevents.RunCompleted, orchevents.OutcomeDone
	case kind == runtime.EventBlocked:
		finalType, finalOutcome = orchevents.RunBlocked, orchevents.OutcomeBlocked
	case kind == runtime.EventCancelled:
		if termErr == nil {
			termErr = context.Canceled
		}
		err = termErr
		finalType, finalOutcome = orchevents.RunCancelled, orchevents.OutcomeCancelled
	case kind == runtime.EventError:
		if termErr == nil {
			termErr = errors.New("run failed")
		}
		err = termErr
		finalType, finalOutcome = orchevents.RunFailed, orchevents.OutcomeError
	default:
		err = fmt.Errorf("ff run: unexpected terminal event %d", kind)
		finalType, finalOutcome = orchevents.RunFailed, orchevents.OutcomeError
	}

	// Telemetry can never upgrade an outcome: a verified completion
	// whose own evidence could not be written exits unverified instead,
	// with the reason on stderr. Failed runs keep failing.
	logWriteWarned := false
	demotedVerified := false
	if evw != nil && openErr == nil && evw.Err() != "" {
		fmt.Fprintf(os.Stderr, "ff run: orchestration event log write failed (%s); session evidence may be incomplete\n", evw.Err())
		logWriteWarned = true
	}
	if (openErr != nil || (evw != nil && evw.Err() != "")) && err == nil && status == runtime.StatusVerified {
		fmt.Fprintln(os.Stderr, "ff run: result marked unverified: orchestration event log unusable")
		status = runtime.StatusPartial
		demotedVerified = true
	}

	exitCode := recovery.ExitUnverified
	switch {
	case err != nil:
		exitCode = 1
	case status == runtime.StatusVerified:
		exitCode = 0
	}
	final := orchevents.Event{
		Type:       finalType,
		Outcome:    finalOutcome,
		Status:     string(status),
		DurationMS: time.Since(start).Milliseconds(),
		ExitCode:   &exitCode,
	}
	if termErr != nil {
		final.Error = termErr.Error()
	}
	emit(final)
	var closeErr error
	if evw != nil {
		closeErr = evw.Close()
	}
	writeErr := eventLogWriteErr(evw, closeErr)
	if !logWriteWarned && openErr == nil && writeErr != "" {
		fmt.Fprintf(os.Stderr, "ff run: orchestration event log write failed (%s); session evidence may be incomplete\n", writeErr)
	}
	if !demotedVerified && writeErr != "" && err == nil && status == runtime.StatusVerified {
		fmt.Fprintln(os.Stderr, "ff run: result marked unverified: orchestration event log unusable")
		status = runtime.StatusPartial
	}

	if respOK {
		return resp, status, warns, err
	}
	return providers.Response{}, status, warns, err
}

func eventLogWriteErr(evw *orchevents.Writer, closeErr error) string {
	if evw != nil && evw.Err() != "" {
		return evw.Err()
	}
	if closeErr != nil {
		return closeErr.Error()
	}
	return ""
}

// mapRunEvent translates one runtime event into the orchestration event
// vocabulary. Text, thinking, and progress paints carry no run evidence
// and produce no line. Only identity, counters, and durations cross:
// no content, no arguments, no paths.
func mapRunEvent(ev runtime.Event) (orchevents.Event, bool) {
	switch ev.Type {
	case runtime.EventToolStart:
		if ev.ToolCall == nil {
			return orchevents.Event{}, false
		}
		return orchevents.Event{
			Type:   orchevents.ToolStarted,
			CallID: ev.ToolCall.ID,
			Tool:   ev.ToolCall.Name,
		}, true
	case runtime.EventToolFinish:
		return mapToolTerminal(ev.ToolResult, orchevents.ToolCompleted)
	case runtime.EventToolFailed:
		return mapToolTerminal(ev.ToolResult, orchevents.ToolFailed)
	case runtime.EventToolDenied:
		return mapToolTerminal(ev.ToolResult, orchevents.ToolDenied)
	case runtime.EventToolCancelled:
		return mapToolTerminal(ev.ToolResult, orchevents.ToolCancelled)
	default:
		return orchevents.Event{}, false
	}
}

// mapToolTerminal stamps one terminal tool outcome. IsError on a plain
// finish (the scheduler's composition flag) still counts as a failure;
// a provider-visible shell exit code rides metadata for forensics.
func mapToolTerminal(result *runtime.ToolResult, terminal orchevents.Type) (orchevents.Event, bool) {
	if result == nil {
		return orchevents.Event{}, false
	}
	if result.IsError && terminal == orchevents.ToolCompleted {
		terminal = orchevents.ToolFailed
	}
	e := orchevents.Event{
		Type:       terminal,
		CallID:     result.ToolCallID,
		Tool:       result.Name,
		DurationMS: result.Duration.Milliseconds(),
		Attempt:    result.Attempt,
	}
	if result.Err != nil {
		e.Error = result.Err.Error()
	}
	if result.HasExitCode {
		e.Meta = map[string]any{"exit_code": result.ExitCode}
	}
	return e, true
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
	defer func() { _ = rt.Close() }()
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
