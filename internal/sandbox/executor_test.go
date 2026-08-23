package sandbox

import (
	"errors"
	"strings"
	"testing"
)

func TestNewExecutorDispatch(t *testing.T) {
	native, err := NewExecutor(DefaultPolicy())
	if err != nil {
		t.Fatalf("native executor error = %v", err)
	}
	if native == nil {
		t.Fatal("native executor = nil")
	}
}

func TestNewExecutorRejectsInvalidPolicy(t *testing.T) {
	if _, err := NewExecutor(Policy{Mode: ModeWSL, Distro: "; rm -rf"}); err == nil {
		t.Fatal("hostile distribution name accepted at construction")
	}
}

func TestNewExecutorWSLRequiresWindows(t *testing.T) {
	if runtimeCaseInsensitive() {
		t.Skip("non-Windows behavior")
	}
	_, err := NewExecutor(Policy{Mode: ModeWSL})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
	if !strings.Contains(err.Error(), "requires Windows") {
		t.Errorf("error = %q, want it to explain the platform requirement", err)
	}
}
