package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// shutdownCmd reaps without a runtime and quits: failed startup and
// runtime-less tests must exit promptly instead of hanging on cleanup.
func TestShutdownCmdNilRuntimeQuits(t *testing.T) {
	m := model{}
	done := make(chan tea.Msg, 1)
	go func() { done <- m.shutdownCmd()() }()
	select {
	case msg := <-done:
		if _, ok := msg.(tea.QuitMsg); !ok {
			t.Fatalf("shutdown msg = %T, want tea.QuitMsg", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdownCmd hung with nil runtime")
	}
}

// The shutdown bound stays a small constant: quit must never wait out
// a wedged child.
func TestShutdownTimeoutBounded(t *testing.T) {
	if shutdownTimeout <= 0 || shutdownTimeout > 30*time.Second {
		t.Errorf("shutdownTimeout = %s, want a small positive bound", shutdownTimeout)
	}
}
