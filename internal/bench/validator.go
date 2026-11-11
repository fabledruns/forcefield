package bench

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Approved validation tools (explicit trust boundary).
var approvedValidationBins = map[string]bool{
	"go":  true,
	"git": true,
}

// Disallowed command characters to prevent shell escape tricks.
const forbiddenCommandChars = ";|&$`><\n\r"

// ValidationResult records the outcome of the independent validation suite.
type ValidationResult struct {
	Passed      bool          `json:"passed"`
	ExitCode    int           `json:"exit_code"`
	Command     string        `json:"command"`
	Stdout      string        `json:"stdout,omitempty"`
	Stderr      string        `json:"stderr,omitempty"`
	Duration    time.Duration `json:"duration"`
	DurationMS  int64         `json:"duration_ms"`
	Error       string        `json:"error,omitempty"`
	CommandRuns []CommandRun  `json:"command_runs,omitempty"`
}

// CommandRun records an individual command outcome.
type CommandRun struct {
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// ValidateCommand checks if argv adheres to the strict security trust boundary.
func ValidateCommand(argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	bin := strings.ToLower(strings.TrimSpace(argv[0]))
	// Reject paths with slashes or directory traversal in executable name
	if strings.ContainsAny(bin, `/\`) {
		return fmt.Errorf("executable path traversal disallowed: %q", bin)
	}
	if !approvedValidationBins[bin] {
		return fmt.Errorf("untrusted validation command %q (allowed: go, git)", bin)
	}
	for _, arg := range argv {
		if strings.ContainsAny(arg, forbiddenCommandChars) {
			return fmt.Errorf("command argument contains forbidden shell metacharacter: %q", arg)
		}
	}
	return nil
}

// RunValidation executes the task's validation commands in the isolated workspace.
func RunValidation(ctx context.Context, ws *Workspace) ValidationResult {
	if ws == nil || ws.Task == nil {
		return ValidationResult{
			Passed: false,
			Error:  "invalid nil workspace or task",
		}
	}
	task := ws.Task
	timeout := task.Validation.ParsedValidationTimeout()
	vCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startAll := time.Now()
	res := ValidationResult{
		Passed: true,
	}

	for _, argv := range task.Validation.Commands {
		cmdStr := strings.Join(argv, " ")
		if err := ValidateCommand(argv); err != nil {
			res.Passed = false
			res.Error = fmt.Sprintf("validation security check failed: %v", err)
			res.CommandRuns = append(res.CommandRuns, CommandRun{
				Command: cmdStr,
				Error:   res.Error,
			})
			break
		}

		cmdStart := time.Now()
		cmd := exec.CommandContext(vCtx, argv[0], argv[1:]...)
		cmd.Dir = ws.Dir

		var stdoutBuf, stderrBuf bytes.Buffer
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf

		runErr := cmd.Run()
		cmdDuration := time.Since(cmdStart)

		exitCode := 0
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		} else if runErr != nil {
			exitCode = 1
		}

		cRun := CommandRun{
			Command:    cmdStr,
			ExitCode:   exitCode,
			Stdout:     strings.TrimSpace(stdoutBuf.String()),
			Stderr:     strings.TrimSpace(stderrBuf.String()),
			DurationMS: cmdDuration.Milliseconds(),
		}
		if runErr != nil {
			cRun.Error = runErr.Error()
		}
		res.CommandRuns = append(res.CommandRuns, cRun)

		if runErr != nil || exitCode != 0 {
			res.Passed = false
			res.ExitCode = exitCode
			res.Command = cmdStr
			res.Stdout = cRun.Stdout
			res.Stderr = cRun.Stderr
			if runErr != nil {
				res.Error = runErr.Error()
			}
			break
		}
		// On passing command, update latest stdout/stderr/command
		res.ExitCode = exitCode
		res.Command = cmdStr
		res.Stdout = cRun.Stdout
		res.Stderr = cRun.Stderr
	}

	res.Duration = time.Since(startAll)
	res.DurationMS = res.Duration.Milliseconds()
	return res
}
