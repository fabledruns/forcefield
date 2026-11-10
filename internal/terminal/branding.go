package terminal

import (
	"io"
	"os"
	"sync"

	"github.com/charmbracelet/x/term"
)

const (
	// DefaultBrandedTitle is the window/tab title applied when Forcefield runs.
	DefaultBrandedTitle = "Forcefield"

	// OSC 2 sequence: set window/tab title. Supported across Windows Terminal,
	// modern conhost with VT processing, and Unix terminal emulators.
	oscTitlePrefix = "\x1b]2;"
	oscTitleSuffix = "\x07"

	// XTerm window title stack sequences (XTWINOPS):
	// CSI 22 ; 0 t pushes both window and icon titles onto the stack.
	// CSI 23 ; 0 t pops both titles from the stack.
	xtSaveTitle    = "\x1b[22;0t"
	xtRestoreTitle = "\x1b[23;0t"
)

var (
	// Testable seams
	isTerminalFunc      = defaultIsTerminal
	getConsoleTitleFunc = defaultGetConsoleTitle
	setConsoleTitleFunc = defaultSetConsoleTitle
	getenvFunc          = os.Getenv
)

func defaultIsTerminal(w io.Writer) bool {
	if f, ok := w.(*os.File); ok {
		return term.IsTerminal(f.Fd())
	}
	return false
}

// IsCompatible checks whether the given writer is an interactive terminal
// capable of supporting title escape sequences or console title APIs.
// If the output is redirected to a file or pipe, or running under an explicitly
// dumb terminal, it returns false to avoid emitting escape sequences into non-terminal streams.
func IsCompatible(w io.Writer) bool {
	if !isTerminalFunc(w) {
		return false
	}

	termEnv := getenvFunc("TERM")
	if termEnv == "dumb" {
		return false
	}

	// Windows Terminal explicitly injects WT_SESSION for every active tab/pane session.
	if getenvFunc("WT_SESSION") != "" {
		return true
	}

	// Native Windows console handle (e.g. conhost or ConPTY)
	if isWindowsConsole(w) {
		return true
	}

	// Standard Unix terminal with TERM set (e.g. xterm, xterm-256color, tmux, screen)
	if termEnv != "" {
		return true
	}

	// Modern terminal environment indicators (e.g. VSCode, TrueColor, Ghostty)
	if getenvFunc("COLORTERM") != "" || getenvFunc("TERM_PROGRAM") != "" {
		return true
	}

	return false
}

// ApplyBranding changes the active terminal tab/window title to title on os.Stdout,
// and returns a cleanup function that restores the previous terminal title.
// If title is empty, DefaultBrandedTitle ("Forcefield") is used.
// The returned cleanup function is idempotent and safe to call multiple times.
// If the terminal is not compatible, a no-op cleanup function is returned.
func ApplyBranding(title string) func() {
	return ApplyBrandingTo(title, os.Stdout)
}

// ApplyBrandingTo changes the active terminal tab/window title to title on the provided writer w,
// and returns a cleanup function that restores the previous terminal title.
func ApplyBrandingTo(title string, w io.Writer) func() {
	if title == "" {
		title = DefaultBrandedTitle
	}

	if !IsCompatible(w) {
		return func() {}
	}

	// Query and save the active terminal title if supported by the OS (e.g., Windows GetConsoleTitleW).
	prevTitle, hasPrev := getConsoleTitleFunc()

	// Push title onto xterm title stack for terminals that support XTWINOPS (e.g. xterm, mintty).
	_, _ = io.WriteString(w, xtSaveTitle)

	// Set window/tab title via OSC 2.
	_, _ = io.WriteString(w, oscTitlePrefix+title+oscTitleSuffix)

	// On Windows, also call SetConsoleTitleW to update ConPTY and classic console headers.
	_ = setConsoleTitleFunc(title)

	// Ensure Windows Terminal JSON fragment with logo.png is installed if Windows Terminal is present.
	_ = EnsureWindowsTerminalFragment(findLogoPath())

	var once sync.Once
	return func() {
		once.Do(func() {
			// Restore the previous title if captured.
			if hasPrev {
				_ = setConsoleTitleFunc(prevTitle)
				if prevTitle != "" {
					_, _ = io.WriteString(w, oscTitlePrefix+prevTitle+oscTitleSuffix)
				}
			}

			// Pop the saved title from the xterm title stack.
			_, _ = io.WriteString(w, xtRestoreTitle)
		})
	}
}
