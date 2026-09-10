package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"forcefield/internal/recovery"
	"forcefield/internal/session"

	"github.com/spf13/cobra"
)

// superviseMaxRestarts, superviseBackoff, superviseMaxBackoff,
// superviseMaxTurns and superviseResetBudget back the ff supervise
// flags. Package vars (like agentFlag) so Args validation and tests
// observe them.
var superviseMaxRestarts int
var superviseBackoff time.Duration
var superviseMaxBackoff time.Duration
var superviseMaxTurns int
var superviseResetBudget bool

// superviseSpawn runs one supervised child attempt. It is a package var
// so tests can stub the child process without spawning anything.
var superviseSpawn = func(ctx context.Context, sessionID string, maxTurns int) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("resolve supervisor binary: %w", err)
	}
	// The child inherits stdio so it stays a normal interactive CLI
	// process: approval prompts still reach the terminal, and its own
	// stdout/stderr contract is unchanged.
	return runChildCommand(ctx, exe, childArgs(sessionID, maxTurns), os.Stdout, os.Stderr, os.Stdin)
}

var superviseCmd = &cobra.Command{
	Use:   "supervise <session-id>",
	Short: "Supervise a headless resumed run with bounded restarts",
	Long: `Repeatedly run "ff run --resume <session-id>" until it parks.

The supervisor is stateless and dumb: it spawns the child, inspects its
exit code, and stops — the session file owns all run state, and the child
owns all execution (load, heal, replay, tool calls). Only exit code 3
(retryable interruption: rate limits, server errors, timeouts, dropped
connections) restarts, bounded by --max-restarts with exponential backoff.
Exit 0 (done), 2 (terminal failure, including quota/billing and auth), and
4 (cancelled or waiting on approvals) stop immediately, as does any child
the supervisor cannot prove retryable: setup failures, unknown exit codes,
and processes killed without an exit code (OOM, external kill) fail closed
with exit 1 and are never retried.

Restart lifecycle persists in the session file (see session.SupervisorState):
a killed supervisor resumes with its remaining budget, and an exhausted
episode refuses further supervised restarts until a terminal child outcome,
a successful manual run, or --reset-budget starts a new episode. Manual
ff run --resume and the TUI never consult this state.`,
	Args: cobra.ExactArgs(1),

	RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateSuperviseFlags(superviseMaxRestarts, superviseBackoff, superviseMaxBackoff, superviseMaxTurns); err != nil {
			return err
		}
		// Like ff run, Ctrl+C cancels through the context chain: the
		// running child is stopped and the wait loop is abandoned.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		budget := recovery.Budget{
			MaxRestarts: superviseMaxRestarts,
			BaseBackoff: superviseBackoff,
			MaxBackoff:  superviseMaxBackoff,
		}
		code := superviseSessionCommand(ctx, args[0], superviseMaxTurns, budget, superviseResetBudget)
		if code != 0 {
			osExit(code)
		}
		return nil
	},
}

// validateSuperviseFlags rejects meaningless flag values before anything
// spawns. Zero is valid everywhere (0 restarts = single attempt,
// 0 backoff = Budget defaults, 0 max-turns = configured bound).
func validateSuperviseFlags(maxRestarts int, backoff, maxBackoff time.Duration, maxTurns int) error {
	if maxRestarts < 0 {
		return fmt.Errorf("invalid --max-restarts %d: must be >= 0", maxRestarts)
	}
	if backoff < 0 {
		return fmt.Errorf("invalid --backoff %s: must be >= 0", backoff)
	}
	if maxBackoff < 0 {
		return fmt.Errorf("invalid --max-backoff %s: must be >= 0", maxBackoff)
	}
	if maxTurns < 0 {
		return fmt.Errorf("invalid --max-turns %d: must be >= 0 (0 keeps the configured bound)", maxTurns)
	}
	return nil
}

// childArgs builds the child argv for one supervised attempt. The
// session ID passes through verbatim on every restart; --max-turns is
// forwarded only when set so default child behavior is unchanged.
func childArgs(sessionID string, maxTurns int) []string {
	args := []string{"run", "--resume", sessionID}
	if maxTurns != 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", maxTurns))
	}
	return args
}

// runChildCommand executes one child process portably (os/exec only, no
// shell, no Unix-specific APIs) and maps its fate to the recovery
// contract. See classifyChildWait for the mapping.
func runChildCommand(ctx context.Context, exe string, args []string, stdout, stderr io.Writer, stdin io.Reader) (int, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = stdin
	return classifyChildWait(ctx, cmd.Run())
}

// classifyChildWait maps a child wait outcome to (exit code, error):
//   - clean wait → the child's code (0/2/3/4 flow into the loop;
//     anything else fails closed in Supervise).
//   - wait failed but our context is done → the death is fallout from
//     cancelling supervision itself (CommandContext kills the child),
//     so report NeedsHuman rather than a mysterious failure.
//   - any other wait failure (spawn error, signaled without a code as
//     from OOM or an external kill) → error: unprovable, never retried.
func classifyChildWait(ctx context.Context, err error) (int, error) {
	if err == nil {
		return recovery.ExitOK, nil
	}
	if ctx.Err() != nil {
		return recovery.ExitNeedsHuman, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code >= 0 {
			return code, nil
		}
	}
	return 0, fmt.Errorf("supervised process did not exit normally: %v", err)
}

// superviseCommand drives supervised restarts for one session and
// returns the supervisor exit code: the last meaningful child code
// (0/2/3/4), or 1 for supervisor-level failure. It keeps the Phase 2
// contract exactly: no session IO, in-memory budget from zero. New code
// should prefer superviseSessionCommand, which adds persisted lifecycle.
func superviseCommand(ctx context.Context, sessionID string, maxTurns int, budget recovery.Budget) int {
	fmt.Fprintf(os.Stderr, "ff supervise %s: running ff run --resume %s (max %d restarts)\n",
		sessionID, sessionID, budget.MaxRestarts)
	return driveSupervision(ctx, sessionID, maxTurns, budget, 0, func(e recovery.SuperviseEvent) {
		reportSuperviseEvent(sessionID, budget, e)
	})
}

// superviseSessionCommand is superviseCommand with persisted restart
// lifecycle: the session file carries the episode's spent restarts and
// exhaustion latch across supervisor kills and external restarts (see
// internal/recovery supervisor_state.go and session.SupervisorState).
// When the session cannot be loaded, it degrades explicitly to
// superviseCommand (Phase 2 in-memory budget, still bounded) instead of
// refusing to run: without the file there is no lifecycle to consult,
// and the child will fail closed on its own if the id is bad.
func superviseSessionCommand(ctx context.Context, sessionID string, maxTurns int, budget recovery.Budget, resetBudget bool) int {
	sess, err := session.Load(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ff supervise %s: cannot load session (%v); running with in-memory budget only\n",
			sessionID, err)
		return superviseCommand(ctx, sessionID, maxTurns, budget)
	}
	if resetBudget {
		recovery.ClearSupervisor(sess)
	} else if sess.Supervisor != nil && sess.Supervisor.ExhaustedAt != 0 {
		fmt.Fprintf(os.Stderr, "ff supervise %s: restart budget previously exhausted (%s); run `ff run --resume %s` to continue manually or pass --reset-budget to start a new episode\n",
			sessionID,
			time.Unix(sess.Supervisor.ExhaustedAt, 0).UTC().Format(time.RFC3339),
			sessionID)
		return recovery.ExitRetryable
	}
	used := 0
	if sess.Supervisor != nil {
		used = sess.Supervisor.Restarts
	}
	fmt.Fprintf(os.Stderr, "ff supervise %s: running ff run --resume %s (max %d restarts)\n",
		sessionID, sessionID, budget.MaxRestarts)
	return driveSupervision(ctx, sessionID, maxTurns, budget, used, func(e recovery.SuperviseEvent) {
		reportSuperviseEvent(sessionID, budget, e)
		persistSuperviseEvent(sessionID, used, e)
	})
}

// driveSupervision is the shared spawn loop: one child closure over the
// superviseSpawn seam driven by the recovery policy from a used-restart
// offset. It performs no session IO itself; callers layer reporting and
// persistence through emit.
func driveSupervision(ctx context.Context, sessionID string, maxTurns int, budget recovery.Budget, used int, emit func(recovery.SuperviseEvent)) int {
	child := func(ctx context.Context) (int, error) {
		return superviseSpawn(ctx, sessionID, maxTurns)
	}
	return recovery.SuperviseFrom(ctx, budget, used, child, recovery.SleepContext, emit)
}

// persistSuperviseEvent mirrors the loop's counter into the session file
// (fresh load per event, best-effort) so the budget survives supervisor
// restarts:
//
//   - Retry committed → record the spent total (starting offset plus the
//     1-based attempt number, set absolutely so replay converges) before
//     the backoff wait, so a kill during backoff resumes with remaining
//     budget.
//   - Budget exhausted → latch the episode.
//   - Terminal child outcome (0/2/4) → drop episode lifecycle so a later
//     episode starts clean. Terminal failures — including quota/auth and
//     denials — therefore never accumulate retry state.
//
// Spawn failures and unknown codes record nothing (nothing ran) and fail
// closed. Load failures are skipped silently: the in-memory budget still
// bounds the invocation, and the startup line already warned when the
// session was unloadable.
func persistSuperviseEvent(sessionID string, startUsed int, e recovery.SuperviseEvent) {
	switch {
	case e.Retry:
		if sess, err := session.Load(sessionID); err == nil {
			recovery.NoteSupervisorRestart(sess, startUsed+e.Attempt)
		}
	case e.Exhausted:
		if sess, err := session.Load(sessionID); err == nil {
			recovery.NoteSupervisorExhausted(sess)
		}
	case e.Final && (e.Code == recovery.ExitOK || e.Code == recovery.ExitTerminal || e.Code == recovery.ExitNeedsHuman):
		if sess, err := session.Load(sessionID); err == nil {
			recovery.ClearSupervisor(sess)
		}
	}
}

// reportSuperviseEvent prints one terse lifecycle line to stderr. Retry
// and stop lines only — no dashboard.
func reportSuperviseEvent(sessionID string, budget recovery.Budget, e recovery.SuperviseEvent) {
	prefix := fmt.Sprintf("ff supervise %s", sessionID)
	switch {
	case e.Retry:
		fmt.Fprintf(os.Stderr, "%s: attempt %d exited 3 (retryable); restarting in %s (restart %d of %d)\n",
			prefix, e.Attempt, e.Wait.Round(time.Second), e.Attempt, budget.MaxRestarts)
	case e.Err != nil:
		fmt.Fprintf(os.Stderr, "%s: attempt %d failed: %v; not retrying\n", prefix, e.Attempt, e.Err)
	case e.Exhausted:
		fmt.Fprintf(os.Stderr, "%s: restart budget exhausted after attempt %d (%d restarts); last exit 3\n",
			prefix, e.Attempt, budget.MaxRestarts)
	case e.Code != recovery.ExitOK && e.Code != recovery.ExitTerminal &&
		e.Code != recovery.ExitRetryable && e.Code != recovery.ExitNeedsHuman:
		fmt.Fprintf(os.Stderr, "%s: attempt %d exited %d (unexpected; not retrying)\n",
			prefix, e.Attempt, e.Code)
	default:
		fmt.Fprintf(os.Stderr, "%s: stopped after attempt %d with exit %d\n",
			prefix, e.Attempt, e.Code)
	}
}

func init() {
	superviseCmd.Flags().IntVar(
		&superviseMaxRestarts,
		"max-restarts",
		5,
		"restart a retryable (exit 3) child at most this many times",
	)
	superviseCmd.Flags().DurationVar(
		&superviseBackoff,
		"backoff",
		recovery.DefaultBaseBackoff,
		"wait before the first restart (doubles up to --max-backoff)",
	)
	superviseCmd.Flags().DurationVar(
		&superviseMaxBackoff,
		"max-backoff",
		recovery.DefaultMaxBackoff,
		"cap the exponential restart backoff",
	)
	superviseCmd.Flags().IntVar(
		&superviseMaxTurns,
		"max-turns",
		0,
		"forwarded to each ff run --resume child (0 keeps the configured bound)",
	)
	superviseCmd.Flags().BoolVar(
		&superviseResetBudget,
		"reset-budget",
		false,
		"clear supervised-restart lifecycle and start a new episode",
	)
	rootCmd.AddCommand(superviseCmd)
}
