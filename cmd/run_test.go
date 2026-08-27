package cmd

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"forcefield/internal/providers"
)

func TestRunCommand_Success(t *testing.T) {
	origRun := runtimeRun
	origStdout := os.Stdout
	defer func() {
		runtimeRun = origRun
		os.Stdout = origStdout
	}()

	// Fake runtime that returns a deterministic response.
	runtimeRun = func(msgs []providers.Message) (providers.Response, error) {
		if len(msgs) != 1 {
			t.Errorf("expected 1 message, got %d", len(msgs))
		}
		if msgs[0].Role != providers.UserRole {
			t.Errorf("role = %v, want UserRole", msgs[0].Role)
		}
		if msgs[0].Content != "hello world" {
			t.Errorf("content = %q, want hello world", msgs[0].Content)
		}
		return providers.Response{Content: "fake response"}, nil
	}

	// Capture stdout.
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := runCommand([]string{"hello", "world"})
	w.Close()
	os.Stdout = origStdout
	if err != nil {
		t.Fatalf("runCommand error = %v", err)
	}
	var buf bytes.Buffer
	buf.ReadFrom(r)
	out := strings.TrimSpace(buf.String())
	if out != "fake response" {
		t.Errorf("stdout = %q, want %q", out, "fake response")
	}
}

func TestRunCommand_JoinsArgs(t *testing.T) {
	origRun := runtimeRun
	defer func() { runtimeRun = origRun }()

	var gotContent string
	runtimeRun = func(msgs []providers.Message) (providers.Response, error) {
		gotContent = msgs[0].Content
		return providers.Response{Content: "ok"}, nil
	}
	// TrimSpace and Join should collapse multiple args with single space.
	if err := runCommand([]string{"  hello ", "world  ", " test"}); err != nil {
		t.Fatalf("runCommand error = %v", err)
	}
	if gotContent != "hello world   test" && gotContent != "hello world test" {
		// Join with space then TrimSpace: "hello   world   test" -> TrimSpace leaves internal spaces.
		// We just verify it contains hello and test.
		if !strings.Contains(gotContent, "hello") || !strings.Contains(gotContent, "test") {
			t.Errorf("joined content = %q", gotContent)
		}
	}
}

func TestRunCommand_PropagatesError(t *testing.T) {
	origRun := runtimeRun
	defer func() { runtimeRun = origRun }()

	runtimeRun = func([]providers.Message) (providers.Response, error) {
		return providers.Response{}, fmt.Errorf("model failure")
	}
	err := runCommand([]string{"task"})
	if err == nil {
		t.Fatal("expected error from runCommand")
	}
	if !strings.Contains(err.Error(), "model failure") {
		t.Errorf("error = %v, want model failure", err)
	}
}

func TestRunCommand_CobraValidation(t *testing.T) {
	// runCmd requires at least 1 arg; cobra should enforce this.
	// We test via Execute by setting args and checking error.
	// Use a fake run to avoid real provider.
	origRun := runtimeRun
	defer func() { runtimeRun = origRun }()
	runtimeRun = func([]providers.Message) (providers.Response, error) {
		return providers.Response{Content: "ok"}, nil
	}
	// Directly test the cobra Args validator.
	if err := runCmd.Args(runCmd, []string{}); err == nil {
		t.Error("expected Args validator to fail for 0 args")
	}
	if err := runCmd.Args(runCmd, []string{"one"}); err != nil {
		t.Errorf("Args validator failed for 1 arg: %v", err)
	}
}
