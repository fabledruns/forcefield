package shell

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"forcefield/internal/sandbox"
	"forcefield/internal/tools"
)

// Job actions. This closed set is the primitive surface: start runs in
// the background, poll/list observe, cancel terminates. No action
// executes anything outside the registry's tracked processes.
const (
	JobActionStart  = "start"
	JobActionPoll   = "poll"
	JobActionList   = "list"
	JobActionCancel = "cancel"
)

// ShellJob runs and inspects background shell commands through a
// JobRegistry: start returns immediately, later turns poll or cancel.
// Jobs are in-process only and bounded (output cap, absolute deadline,
// idle TTL); they cannot outlive the process beyond their deadline.
type ShellJob struct {
	jobs *JobRegistry
	// limits overrides job defaults. Zero values resolve via
	// tools.DefaultLimitsFor("shell_job").
	limits tools.Limits
}

// NewShellJob returns a ready-to-register ShellJob using native execution.
func NewShellJob() *ShellJob { return &ShellJob{jobs: NewJobRegistry(nil)} }

// NewShellJobWithExecutor returns a ShellJob whose commands are built by
// the given sandbox.Executor (workspace policy enforced at start).
func NewShellJobWithExecutor(e sandbox.Executor) *ShellJob {
	return &ShellJob{jobs: NewJobRegistry(e)}
}

// Registry exposes the underlying job registry (for lifecycle owners
// and tests).
func (s *ShellJob) Registry() *JobRegistry { return s.jobs }

// SetLimits overrides job defaults. Only positive fields take effect;
// the rest resolve to the tool defaults.
func (s *ShellJob) SetLimits(l tools.Limits) {
	s.limits = l
	if l.MaxBytes > 0 {
		s.jobs.setMaxOutput(l.MaxBytes)
	}
}

// ToolLimits reports the resolved bounds.
func (s *ShellJob) ToolLimits() tools.Limits {
	return s.resolveLimits()
}

func (s *ShellJob) resolveLimits() tools.Limits {
	if s == nil {
		return tools.DefaultLimitsFor("shell_job")
	}
	return s.limits.WithDefaults(tools.DefaultLimitsFor("shell_job"))
}

func (ShellJob) Name() string { return "shell_job" }

func (ShellJob) Description() string {
	return "Run a shell command in the background and inspect it later. " +
		"Actions: start (returns a job id immediately), poll (state + output), list (all jobs), cancel (terminate). " +
		"Jobs are bounded (output cap, time limit, idle expiry) and never survive the Forcefield process."
}

func (ShellJob) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "One of \"start\", \"poll\", \"list\", \"cancel\".",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "Shell command to run (start only).",
			},
			"job_id": map[string]any{
				"type":        "string",
				"description": "Job ID from a previous start (poll/cancel).",
			},
			"cwd": map[string]any{
				"type":        "string",
				"description": "Working directory for start. Defaults to the workspace.",
			},
			"env": map[string]any{
				"type":        "object",
				"description": "Additional environment variables for start, as key/value string pairs.",
			},
			"timeout_seconds": map[string]any{
				"type":        "number",
				"description": "Maximum job lifetime in seconds (start only). Defaults to 300, at most 300.",
			},
			"tail_lines": map[string]any{
				"type":        "string",
				"description": "Poll output lines to return (poll only), a number or \"all\". Defaults to 100.",
			},
		},
		"required": []string{"action"},
	}
}

// Metadata advertises shell_job like shell: approval-gated execution.
func (s *ShellJob) Metadata() tools.Metadata {
	return tools.Metadata{
		Timeout:              s.resolveLimits().Timeout,
		SupportsStreaming:    false,
		SupportsCancellation: true,
		SupportsParallel:     true,
		RequiredPermissions:  []tools.Permission{tools.PermissionExecuteShell},
		Retryable:            false,
	}
}

func (s ShellJob) Execute(ctx context.Context, args map[string]any) (tools.Result, error) {
	action, err := tools.StringArg(args, "action")
	if err != nil {
		return tools.Result{}, err
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case JobActionStart:
		return s.start(ctx, args)
	case JobActionPoll:
		return s.poll(args)
	case JobActionList:
		return s.list()
	case JobActionCancel:
		return s.cancel(args)
	default:
		return tools.Result{IsError: true, Content: fmt.Sprintf(
			"unsupported shell_job action %q (supported: start, poll, list, cancel)", action)}, nil
	}
}

func (s ShellJob) start(ctx context.Context, args map[string]any) (tools.Result, error) {
	command, err := tools.StringArg(args, "command")
	if err != nil {
		return tools.Result{}, err
	}
	if strings.TrimSpace(command) == "" {
		return tools.Result{}, &tools.ArgumentError{Field: "command", Reason: "must not be empty"}
	}
	if prog, ok := detectInteractiveCommand(command); ok {
		return tools.Result{
			IsError: true,
			Content: fmt.Sprintf(
				"refusing to run %q: it requires an interactive terminal (tty), which shell jobs do not provide",
				prog,
			),
			Tool: "shell_job",
		}, nil
	}
	cwd, err := tools.OptionalStringArg(args, "cwd", "")
	if err != nil {
		return tools.Result{}, err
	}
	envPairs, err := extraEnvArgs(args)
	if err != nil {
		return tools.Result{}, err
	}
	timeout := s.resolveLimits().Timeout
	if raw, ok := args["timeout_seconds"]; ok {
		secs, ok := toFloat(raw)
		if !ok || secs <= 0 {
			return tools.Result{}, &tools.ArgumentError{Field: "timeout_seconds", Reason: "must be a positive number of seconds"}
		}
		if secs > float64(tools.MaxTimeout/time.Second) {
			return tools.Result{}, &tools.ArgumentError{Field: "timeout_seconds", Reason: fmt.Sprintf("must be at most %d seconds", int(tools.MaxTimeout/time.Second))}
		}
		timeout = time.Duration(secs * float64(time.Second))
	}

	snap, err := s.jobs.Start(ctx, command, cwd, envPairs, timeout)
	if err != nil {
		return tools.Result{IsError: true, Content: jobStartError(err), Tool: "shell_job"}, nil
	}
	return tools.Result{
		Content: fmt.Sprintf("started %s (running): %s\npoll with shell_job action=poll job_id=%s", snap.ID, shortCommand(command), snap.ID),
		Tool:    "shell_job",
		Command: command,
	}, nil
}

// jobStartError maps registry/executor failures to model-actionable
// soft errors, preserving workspace-boundary wording.
func jobStartError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("cannot start background job: %v", err)
}

func (s ShellJob) poll(args map[string]any) (tools.Result, error) {
	id, err := tools.StringArg(args, "job_id")
	if err != nil {
		return tools.Result{}, err
	}
	tail := defaultPollTailLines
	all := false
	if raw, _ := args["tail_lines"].(string); strings.TrimSpace(raw) != "" {
		if strings.EqualFold(strings.TrimSpace(raw), "all") {
			all = true
		} else {
			n, verr := strconv.Atoi(strings.TrimSpace(raw))
			if verr != nil || n < 0 {
				return tools.Result{}, &tools.ArgumentError{Field: "tail_lines", Reason: "must be a non-negative number or \"all\""}
			}
			tail = n
		}
	}
	snap, err := s.jobs.Poll(strings.TrimSpace(id))
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot poll job: %v", err), Tool: "shell_job"}, nil
	}
	return tools.Result{
		Content:  formatSnapshot(snap, tail, all),
		ExitCode: snap.ExitCode,
		Stdout:   snap.Output,
		Tool:     "shell_job",
		Command:  snap.Command,
		Metadata: pollMeta(snap),
	}, nil
}

func (s ShellJob) list() (tools.Result, error) {
	snaps := s.jobs.List()
	if len(snaps) == 0 {
		return tools.Result{Content: "no background jobs", Tool: "shell_job"}, nil
	}
	var b strings.Builder
	for _, snap := range snaps {
		line := fmt.Sprintf("%s [%s] %s", snap.ID, snap.State, shortCommand(snap.Command))
		if snap.HasExit {
			line += fmt.Sprintf(" (exit %d)", snap.ExitCode)
		}
		if snap.Note != "" {
			line += fmt.Sprintf(" — %s", snap.Note)
		}
		b.WriteString(line + "\n")
	}
	return tools.Result{Content: strings.TrimRight(b.String(), "\n"), Tool: "shell_job"}, nil
}

func (s ShellJob) cancel(args map[string]any) (tools.Result, error) {
	id, err := tools.StringArg(args, "job_id")
	if err != nil {
		return tools.Result{}, err
	}
	snap, err := s.jobs.Cancel(strings.TrimSpace(id))
	if err != nil {
		return tools.Result{IsError: true, Content: fmt.Sprintf("cannot cancel job: %v", err), Tool: "shell_job"}, nil
	}
	return tools.Result{
		Content:  formatSnapshot(snap, defaultPollTailLines, false),
		ExitCode: snap.ExitCode,
		Tool:     "shell_job",
		Command:  snap.Command,
	}, nil
}

// formatSnapshot renders one job for the model: identity, lifecycle,
// and a bounded output slice.
func formatSnapshot(snap Snapshot, tail int, all bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s]", snap.ID, snap.State)
	if snap.HasExit {
		fmt.Fprintf(&b, " (exit %d)", snap.ExitCode)
	}
	if snap.Note != "" {
		fmt.Fprintf(&b, " — %s", snap.Note)
	}
	out := snap.Output
	if !all {
		lines := strings.Split(out, "\n")
		// A trailing newline would inflate the count by one phantom line.
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) > tail {
			fmt.Fprintf(&b, "\noutput (last %d of %d lines):\n", tail, len(lines))
			out = strings.Join(lines[len(lines)-tail:], "\n")
		} else if out != "" {
			b.WriteString("\noutput:\n")
		}
	} else if out != "" {
		b.WriteString("\noutput:\n")
	}
	b.WriteString(out)
	if strings.TrimSpace(out) == "" {
		b.WriteString("(no output yet)")
	}
	return b.String()
}

// pollMeta carries structured truncation state when the job's captured
// output hit its cap.
func pollMeta(snap Snapshot) map[string]any {
	if !snap.Truncated {
		return nil
	}
	return map[string]any{"truncated": true, "job_id": snap.ID}
}

// shortCommand keeps listings to one readable line.
func shortCommand(command string) string {
	line := strings.TrimSpace(strings.SplitN(command, "\n", 2)[0])
	if len(line) > 120 {
		return line[:120] + "…"
	}
	if line == "" {
		return "(empty command)"
	}
	return line
}
