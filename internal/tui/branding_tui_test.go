package tui

import (
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/session"
	"forcefield/internal/terminal"
)

func TestStart_AppliesAndRestoresBrandingOnSuccess(t *testing.T) {
	origBrander := tuiBrander
	origRunProgram := runProgram
	defer func() {
		tuiBrander = origBrander
		runProgram = origRunProgram
	}()

	var brandCalls []string
	restoreCalled := false

	tuiBrander = func(title string) func() {
		brandCalls = append(brandCalls, title)
		return func() {
			restoreCalled = true
		}
	}

	runProgram = func(p *tea.Program) (tea.Model, error) {
		// Verify branding was applied before program execution starts
		if len(brandCalls) != 1 || brandCalls[0] != terminal.DefaultBrandedTitle {
			t.Errorf("expected branding %q before runProgram, got %v", terminal.DefaultBrandedTitle, brandCalls)
		}
		if restoreCalled {
			t.Errorf("restore called prematurely before runProgram finished")
		}
		return nil, nil
	}

	sess := session.New()
	err := Start(sess)
	if err != nil {
		t.Fatalf("Start returned unexpected error: %v", err)
	}

	if !restoreCalled {
		t.Fatal("expected brand restoration function to be called after Start exit")
	}
}

func TestStart_RestoresBrandingOnRunError(t *testing.T) {
	origBrander := tuiBrander
	origRunProgram := runProgram
	defer func() {
		tuiBrander = origBrander
		runProgram = origRunProgram
	}()

	restoreCalled := false
	tuiBrander = func(title string) func() {
		return func() {
			restoreCalled = true
		}
	}

	injectedErr := errors.New("terminal error in event loop")
	runProgram = func(p *tea.Program) (tea.Model, error) {
		return nil, injectedErr
	}

	sess := session.New()
	err := Start(sess)
	if !errors.Is(err, injectedErr) {
		t.Fatalf("expected injected error %v, got %v", injectedErr, err)
	}

	if !restoreCalled {
		t.Fatal("expected branding to be restored when runProgram returns an error")
	}
}

func TestStart_RestoresBrandingOnCancellation(t *testing.T) {
	origBrander := tuiBrander
	origRunProgram := runProgram
	defer func() {
		tuiBrander = origBrander
		runProgram = origRunProgram
	}()

	restoreCalled := false
	tuiBrander = func(title string) func() {
		return func() {
			restoreCalled = true
		}
	}

	runProgram = func(p *tea.Program) (tea.Model, error) {
		return nil, tea.ErrInterrupted
	}

	sess := session.New()
	err := Start(sess)
	if !errors.Is(err, tea.ErrInterrupted) {
		t.Fatalf("expected tea.ErrInterrupted, got %v", err)
	}

	if !restoreCalled {
		t.Fatal("expected branding to be restored on cancellation/SIGINT")
	}
}

func TestStart_RestoresBrandingOnPanic(t *testing.T) {
	origBrander := tuiBrander
	origRunProgram := runProgram
	defer func() {
		tuiBrander = origBrander
		runProgram = origRunProgram
	}()

	restoreCalled := false
	tuiBrander = func(title string) func() {
		return func() {
			restoreCalled = true
		}
	}

	runProgram = func(p *tea.Program) (tea.Model, error) {
		panic("simulated panic in tui")
	}

	sess := session.New()
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic to propagate")
			}
		}()
		_ = Start(sess)
	}()

	if !restoreCalled {
		t.Fatal("expected branding to be restored when Start panics")
	}
}

func TestStart_CancelsUnstartedProgramOnExit(t *testing.T) {
	origBrander := tuiBrander
	origRunProgram := runProgram
	defer func() {
		tuiBrander = origBrander
		runProgram = origRunProgram
	}()

	restoreCalled := false
	tuiBrander = func(title string) func() {
		return func() {
			restoreCalled = true
		}
	}

	var capturedProg *tea.Program
	runProgram = func(p *tea.Program) (tea.Model, error) {
		capturedProg = p
		// Allow background input/drain loops (e.g. Windows CONIN$ early-msg drain)
		// enough time to observe the published program before returning without Run().
		time.Sleep(25 * time.Millisecond)
		return nil, nil
	}

	sess := session.New()
	startDone := make(chan error, 1)
	go func() {
		startDone <- Start(sess)
	}()

	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("Start returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start deadlocked when runProgram returned without running Bubble Tea event loop")
	}

	if !restoreCalled {
		t.Fatal("expected branding to be restored after Start exit")
	}
	if capturedProg == nil {
		t.Fatal("expected runProgram to receive a non-nil *tea.Program")
	}

	sendDone := make(chan struct{})
	go func() {
		capturedProg.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
		close(sendDone)
	}()

	select {
	case <-sendDone:
	case <-time.After(500 * time.Millisecond):
		capturedProg.Kill()
		t.Fatal("capturedProg.Send blocked after Start returned; unstarted program context was not cancelled")
	}
}
