package permissions

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// TestStdinAsker_CancelUnblocksPrompt pins that Ctrl+C during "ff run"
// unblocks a prompt waiting on terminal input instead of hanging until
// the user types. The blocked read parks (one reader per prompt, freed
// on EOF/process end); the Ask call itself must return promptly.
func TestStdinAsker_CancelUnblocksPrompt(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	asker := &StdinAsker{In: pr, Out: io.Discard}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := asker.Ask(ctx, Request{Tool: "shell", Arguments: map[string]any{}})
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Ask() error = nil, want the cancellation")
		}
		if got := context.Cause(ctx); got != context.Canceled {
			t.Fatalf("ctx cause = %v, want context.Canceled", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ask did not return within 5s of cancel; prompt hangs on terminal read")
	}
}

// TestStdinAsker_AnswerStillWorks pins that the interruptible read did
// not break normal prompting, including multi-line piped input (no
// over-read loss from the single-reader design).
func TestStdinAsker_AnswerStillWorks(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  Decision
	}{
		{"y\n", Allow},
		{"n\n", Deny},
	} {
		asker := &StdinAsker{In: strings.NewReader(tc.input), Out: io.Discard}
		got, err := asker.Ask(context.Background(), Request{Tool: "shell", Arguments: map[string]any{}})
		if err != nil {
			t.Fatalf("Ask(%q) error = %v", tc.input, err)
		}
		// PromptAllowOnce.Decision() == Allow; PromptDenyOnce == Deny.
		if got.Decision() != tc.want {
			t.Errorf("Ask(%q) decision = %v, want %v", tc.input, got.Decision(), tc.want)
		}
	}
}

// TestStdinAsker_ScrubsSecrets pins that the headless permission prompt
// never prints credentials to stdout (where logs or pipes may capture
// them). Regression test for the audit finding that Arguments were
// marshaled and printed raw: string values, nested env objects, and
// non-string values must all be display-scrubbed while the approval
// decision itself is unaffected.
func TestStdinAsker_ScrubsSecrets(t *testing.T) {
	const secret = "sk-12345678901234567890abcdef"
	var out bytes.Buffer
	asker := &StdinAsker{In: strings.NewReader("n\n"), Out: &out}
	got, err := asker.Ask(context.Background(), Request{
		Tool: "shell",
		Arguments: map[string]any{
			"command": "deploy",
			"env": map[string]any{
				"DEPLOY_TOKEN": secret,
				"RETRIES":      float64(3),
			},
		},
	})
	if err != nil {
		t.Fatalf("Ask error = %v", err)
	}
	if got.Decision() != Deny {
		t.Fatalf("decision = %v, want Deny (answer was n)", got.Decision())
	}
	if rendered := out.String(); strings.Contains(rendered, secret) {
		t.Errorf("headless prompt leaked secret:\n%s", rendered)
	}
}
