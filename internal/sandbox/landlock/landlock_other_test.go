//go:build !linux

package landlock

import (
	"errors"
	"testing"
)

// TestSpikeUnsupportedOffLinux pins honest non-Linux behavior: every
// operation reports unavailability instead of silently succeeding.
// Runs on the Windows and macOS CI legs.
func TestSpikeUnsupportedOffLinux(t *testing.T) {
	if _, err := QueryABI(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("QueryABI error = %v, want ErrUnsupported", err)
	}
	if err := SetNoNewPrivs(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("SetNoNewPrivs error = %v, want ErrUnsupported", err)
	}
	if err := InstallRules(Policy{Version: PolicyVersion}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("InstallRules error = %v, want ErrUnsupported", err)
	}
	if err := ApplyPolicy(Policy{Version: PolicyVersion}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("ApplyPolicy error = %v, want ErrUnsupported", err)
	}
}
