//go:build !windows

package sandbox

import (
	"context"
	"fmt"
)

// wslExecutor is a compile-time placeholder on non-Windows platforms. WSL
// execution is a Windows capability; on other systems the mode exists in
// configuration but cannot be constructed, and the failure says exactly
// that instead of falling back to native execution.
type wslExecutor struct {
	policy Policy
}

func newWSLExecutor(p Policy) (*wslExecutor, error) {
	return nil, fmt.Errorf("%w: sandbox mode \"wsl\" requires Windows; set sandbox.mode to \"native\" or run Forcefield on Windows",
		ErrUnsupported)
}

func (*wslExecutor) Prepare(_ context.Context, _ Request) (*Prepared, error) {
	return nil, ErrUnsupported
}

func (*wslExecutor) Probe(context.Context) error {
	return fmt.Errorf("%w: sandbox mode \"wsl\" requires Windows", ErrUnsupported)
}

func (*wslExecutor) Describe(context.Context) Enforcement {
	return Enforcement{
		Mode: ModeWSL,
		Limitations: []Limitation{
			{ID: LimPlatformWSLWindows, Warn: true, Detail: "sandbox mode \"wsl\" requires Windows; on this platform the executor cannot be constructed (no fallback to native)"},
			{ID: LimPlatformHostOnly, Detail: "no Linux/macOS isolation backend exists in v1.5.0; native runs on the host"},
		},
	}
}

var _ Executor = (*wslExecutor)(nil)
