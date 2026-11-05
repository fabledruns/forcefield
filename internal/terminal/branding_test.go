package terminal

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func withMockSeams(t *testing.T) {
	origIsTerm := isTerminalFunc
	origGetTitle := getConsoleTitleFunc
	origSetTitle := setConsoleTitleFunc
	origGetenv := getenvFunc

	t.Cleanup(func() {
		isTerminalFunc = origIsTerm
		getConsoleTitleFunc = origGetTitle
		setConsoleTitleFunc = origSetTitle
		getenvFunc = origGetenv
	})
}

func TestApplyBranding_IncompatibleWhenNotTerminal(t *testing.T) {
	withMockSeams(t)

	isTerminalFunc = func(w io.Writer) bool { return false }
	getenvFunc = func(k string) string {
		if k == "WT_SESSION" {
			return "test-session"
		}
		return ""
	}

	buf := &bytes.Buffer{}
	restore := ApplyBrandingTo("Forcefield", buf)
	if buf.Len() != 0 {
		t.Fatalf("expected no escape sequences for non-terminal writer, got %q", buf.String())
	}

	restore()
	if buf.Len() != 0 {
		t.Fatalf("expected no restore sequences for non-terminal writer, got %q", buf.String())
	}
}

func TestApplyBranding_IncompatibleWhenDumbTerm(t *testing.T) {
	withMockSeams(t)

	isTerminalFunc = func(w io.Writer) bool { return true }
	getenvFunc = func(k string) string {
		if k == "TERM" {
			return "dumb"
		}
		return ""
	}

	buf := &bytes.Buffer{}
	restore := ApplyBrandingTo("Forcefield", buf)
	if buf.Len() != 0 {
		t.Fatalf("expected no escape sequences for TERM=dumb, got %q", buf.String())
	}

	restore()
	if buf.Len() != 0 {
		t.Fatalf("expected no restore sequences for TERM=dumb, got %q", buf.String())
	}
}

func TestApplyBranding_Compatible_WindowsTerminal(t *testing.T) {
	withMockSeams(t)

	isTerminalFunc = func(w io.Writer) bool { return true }
	getenvFunc = func(k string) string {
		if k == "WT_SESSION" {
			return "6c3b06ff-3b2d-45db-9a84-60c7f7e9a8f2"
		}
		return ""
	}

	var setTitles []string
	setConsoleTitleFunc = func(title string) error {
		setTitles = append(setTitles, title)
		return nil
	}
	getConsoleTitleFunc = func() (string, bool) {
		return "pwsh.exe", true
	}

	buf := &bytes.Buffer{}
	restore := ApplyBrandingTo("Forcefield", buf)

	// Check title set
	output := buf.String()
	if !strings.Contains(output, xtSaveTitle) {
		t.Errorf("expected xtSaveTitle (%q), got %q", xtSaveTitle, output)
	}
	if !strings.Contains(output, "\x1b]2;Forcefield\x07") {
		t.Errorf("expected OSC 2 sequence for Forcefield, got %q", output)
	}
	if len(setTitles) != 1 || setTitles[0] != "Forcefield" {
		t.Errorf("expected setConsoleTitle called with Forcefield, got %v", setTitles)
	}

	// Restore
	buf.Reset()
	restore()

	restoreOutput := buf.String()
	if !strings.Contains(restoreOutput, "\x1b]2;pwsh.exe\x07") {
		t.Errorf("expected OSC 2 sequence restoring pwsh.exe, got %q", restoreOutput)
	}
	if !strings.Contains(restoreOutput, xtRestoreTitle) {
		t.Errorf("expected xtRestoreTitle (%q), got %q", xtRestoreTitle, restoreOutput)
	}
	if len(setTitles) != 2 || setTitles[1] != "pwsh.exe" {
		t.Errorf("expected setConsoleTitle restored to pwsh.exe, got %v", setTitles)
	}
}

func TestApplyBranding_Restore_Idempotent(t *testing.T) {
	withMockSeams(t)

	isTerminalFunc = func(w io.Writer) bool { return true }
	getenvFunc = func(k string) string {
		if k == "WT_SESSION" {
			return "session"
		}
		return ""
	}

	setCount := 0
	setConsoleTitleFunc = func(title string) error {
		setCount++
		return nil
	}
	getConsoleTitleFunc = func() (string, bool) {
		return "original", true
	}

	buf := &bytes.Buffer{}
	restore := ApplyBrandingTo("Forcefield", buf)
	if setCount != 1 {
		t.Fatalf("expected 1 call to setConsoleTitle, got %d", setCount)
	}

	buf.Reset()
	restore()
	firstLen := buf.Len()
	if setCount != 2 {
		t.Fatalf("expected 2 calls to setConsoleTitle after first restore, got %d", setCount)
	}

	// Call restore again - must be a no-op
	restore()
	if setCount != 2 {
		t.Fatalf("expected setConsoleTitle not called again on second restore, got %d", setCount)
	}
	if buf.Len() != firstLen {
		t.Fatalf("expected no additional bytes written on second restore, got len %d vs %d", buf.Len(), firstLen)
	}
}

func TestApplyBranding_WithoutPreviousTitle(t *testing.T) {
	withMockSeams(t)

	isTerminalFunc = func(w io.Writer) bool { return true }
	getenvFunc = func(k string) string {
		if k == "TERM" {
			return "xterm-256color"
		}
		return ""
	}

	getConsoleTitleFunc = func() (string, bool) {
		return "", false
	}

	buf := &bytes.Buffer{}
	restore := ApplyBrandingTo("Forcefield", buf)
	buf.Reset()

	restore()
	restoreOutput := buf.String()
	// Should pop stack
	if !strings.Contains(restoreOutput, xtRestoreTitle) {
		t.Errorf("expected xtRestoreTitle, got %q", restoreOutput)
	}
	// Should NOT emit empty OSC 2
	if strings.Contains(restoreOutput, "\x1b]2;\x07") {
		t.Errorf("unexpected empty OSC 2 sequence in %q", restoreOutput)
	}
}

func TestApplyBranding_DefaultTitle(t *testing.T) {
	withMockSeams(t)

	isTerminalFunc = func(w io.Writer) bool { return true }
	getenvFunc = func(k string) string {
		if k == "WT_SESSION" {
			return "active"
		}
		return ""
	}

	buf := &bytes.Buffer{}
	restore := ApplyBrandingTo("", buf)
	defer restore()

	if !strings.Contains(buf.String(), "\x1b]2;Forcefield\x07") {
		t.Errorf("expected default title Forcefield in %q", buf.String())
	}
}

func TestIsCompatible(t *testing.T) {
	withMockSeams(t)

	tests := []struct {
		name       string
		isTerm     bool
		env        map[string]string
		compatible bool
	}{
		{
			name:       "not a terminal",
			isTerm:     false,
			env:        map[string]string{"WT_SESSION": "123"},
			compatible: false,
		},
		{
			name:       "TERM=dumb",
			isTerm:     true,
			env:        map[string]string{"TERM": "dumb"},
			compatible: false,
		},
		{
			name:       "WT_SESSION present",
			isTerm:     true,
			env:        map[string]string{"WT_SESSION": "active-guid"},
			compatible: true,
		},
		{
			name:       "TERM=xterm-256color",
			isTerm:     true,
			env:        map[string]string{"TERM": "xterm-256color"},
			compatible: true,
		},
		{
			name:       "COLORTERM=truecolor",
			isTerm:     true,
			env:        map[string]string{"COLORTERM": "truecolor"},
			compatible: true,
		},
		{
			name:       "TERM_PROGRAM=vscode",
			isTerm:     true,
			env:        map[string]string{"TERM_PROGRAM": "vscode"},
			compatible: true,
		},
		{
			name:       "no env variables set",
			isTerm:     true,
			env:        map[string]string{},
			compatible: false, // on non-Windows/no console
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isTerminalFunc = func(w io.Writer) bool { return tt.isTerm }
			getenvFunc = func(k string) string { return tt.env[k] }

			buf := &bytes.Buffer{}
			got := IsCompatible(buf)
			if got != tt.compatible {
				t.Errorf("IsCompatible() = %v, want %v", got, tt.compatible)
			}
		})
	}
}
