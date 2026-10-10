//go:build !linux

package sandbox

import (
	"errors"
	"testing"
)

// TestIsolatedRequiresLinux pins honest non-Linux behavior: the
// isolated mode exists in configuration but cannot be constructed
// here, and the failure names the platform requirement instead of
// falling back to native execution. Runs on the Windows and macOS CI
// legs. Landlock coverage is never claimed there.
func TestIsolatedRequiresLinux(t *testing.T) {
	_, err := NewExecutor(Policy{Mode: ModeIsolated, Workspace: t.TempDir()})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}
