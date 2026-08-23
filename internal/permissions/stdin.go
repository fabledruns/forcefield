package permissions

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// StdinAsker prompts the user on a plain terminal - the default
// interactive experience for "ff run" and any other non-TUI entry point.
// It blocks the calling goroutine until an answer arrives on In, honoring
// ctx cancellation only between reads (a blocked terminal read can't be
// interrupted short of closing In).
type StdinAsker struct {
	In  io.Reader
	Out io.Writer
}

// NewStdinAsker returns an asker using the process stdin and stdout.
func NewStdinAsker() *StdinAsker {
	return &StdinAsker{In: os.Stdin, Out: os.Stdout}
}

func (a *StdinAsker) Ask(ctx context.Context, req Request) (Prompt, error) {
	if err := ctx.Err(); err != nil {
		return PromptDenyOnce, err
	}

	out := a.Out
	if out == nil {
		out = os.Stdout
	}
	in := a.In
	if in == nil {
		in = os.Stdin
	}

	args, err := json.MarshalIndent(req.Arguments, "", "  ")
	if err != nil {
		args = []byte("{}")
	}

	fmt.Fprintf(out, "\nTool wants permission\n\nTool:\n%s\n\nArguments:\n%s\n", req.Tool, args)

	// Execution facts come from the executor itself, so the prompt can
	// never claim more isolation than exists. When absent (tools without
	// an execution story), nothing extra is printed.
	if req.Execution != nil {
		fmt.Fprintf(out, "\n%s\n", strings.Join(req.Execution.SummaryLines(), "\n"))
	}

	fmt.Fprint(out, "\nAllow?\n\n(y) Yes\n(n) No\n(a) Always allow this tool\n(d) Always deny this tool\n\n> ")

	reader := bufio.NewReader(in)
	for {
		if err := ctx.Err(); err != nil {
			return PromptDenyOnce, err
		}

		line, err := reader.ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))

		switch answer {
		case "y", "yes":
			return PromptAllowOnce, nil
		case "n", "no":
			return PromptDenyOnce, nil
		case "a", "always":
			return PromptAlwaysAllow, nil
		case "d", "deny":
			return PromptAlwaysDeny, nil
		}

		if err != nil {
			// EOF or read failure with no usable answer: fail closed.
			return PromptDenyOnce, fmt.Errorf("permissions: read answer: %w", err)
		}

		fmt.Fprint(out, "please enter y, n, a, or d: ")
	}
}
