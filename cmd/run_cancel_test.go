package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forcefield/internal/providers"
)

func TestRunCommand_MapsCancellationToCancelledError(t *testing.T) {
	origRun := runtimeRun
	defer func() { runtimeRun = origRun }()

	runtimeRun = func(context.Context, []providers.Message) (providers.Response, error) {
		return providers.Response{}, context.Canceled
	}
	err := runCommand([]string{"task"})
	if err == nil {
		t.Fatal("expected an error for a cancelled run")
	}
	if !strings.Contains(err.Error(), "cancel") {
		t.Errorf("error = %q, want it to report cancellation", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want errors.Is(..., context.Canceled) to hold", err)
	}
}

func TestRunCommand_PassesCancellableContext(t *testing.T) {
	origRun := runtimeRun
	defer func() { runtimeRun = origRun }()

	var errDuringCall error
	var hasDoneChannel bool
	runtimeRun = func(ctx context.Context, _ []providers.Message) (providers.Response, error) {
		// Capture liveness during the call: runCommand's deferred stop()
		// cancels the context after return, so everything must be
		// observed here. A signal-wired context is live with a non-nil
		// Done channel; context.Background would have a nil one.
		errDuringCall = ctx.Err()
		hasDoneChannel = ctx.Done() != nil
		return providers.Response{Content: "ok"}, nil
	}
	if err := runCommand([]string{"task"}); err != nil {
		t.Fatalf("runCommand error = %v", err)
	}
	if errDuringCall != nil {
		t.Errorf("context already done during the call: %v", errDuringCall)
	}
	if !hasDoneChannel {
		t.Error("runtimeRun received a context without a Done channel (not signal-wired)")
	}
}

func TestMapRunError_Passthrough(t *testing.T) {
	if err := mapRunError(nil); err != nil {
		t.Errorf("mapRunError(nil) = %v, want nil", err)
	}
	other := errors.New("boom")
	if err := mapRunError(other); err != other {
		t.Errorf("mapRunError(boom) = %v, want the original error", err)
	}
}
