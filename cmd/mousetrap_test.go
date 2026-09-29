package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestMousetrapSplashDisabled pins that cobra's Windows Explorer
// mousetrap splash stays off. With the default text, every Execute
// walks the full OS process list (Toolhelp32 snapshot) to detect an
// Explorer double-click launch — ~19ms of CPU on every invocation,
// including headless runs, for a splash a terminal tool never needs.
// Blank text skips the detection entirely (cobra/command_win.go).
func TestMousetrapSplashDisabled(t *testing.T) {
	if cobra.MousetrapHelpText != "" {
		t.Errorf("cobra.MousetrapHelpText = %q, want blank so startup skips the process-list walk", cobra.MousetrapHelpText)
	}
}
