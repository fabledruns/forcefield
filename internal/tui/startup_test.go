package tui

// TUI-first startup tests: the first rendered frame must precede
// background runtime initialization, submission must wait for readiness,
// failures must surface without crashing, and quitting mid-init must
// terminate promptly without leaking goroutines.

import (
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/config"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// errStartupTestSentinel marks builder failures injected by these tests.
var errStartupTestSentinel = errors.New("startup test injected failure")

// blockedBuilder returns a runtimeBuilder that waits until release is
// closed, then calls through to done. Tests observe started to know the
// background init began.
func blockedBuilder(release chan struct{}, started chan struct{}, done chan struct{}) runtimeBuilder {
	return func(cfg *config.Config, sess *session.Session) (*runtime.Runtime, error) {
		close(started)
		<-release
		close(done)
		return nil, errStartupTestSentinel
	}
}

// TestStartupFirstFrameBeforeReady proves the ordering invariant:
// the first View happens while the runtime is still initializing.
func TestStartupFirstFrameBeforeReady(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	done := make(chan struct{})
	defer close(release)

	var mu sync.Mutex
	var phases []startupPhase
	prevHook := viewPhaseHook
	viewPhaseHook = func(p startupPhase) {
		mu.Lock()
		defer mu.Unlock()
		phases = append(phases, p)
	}
	defer func() { viewPhaseHook = prevHook }()

	cfg := &config.Config{}
	cfg.Model.Provider = "test"
	cfg.Model.Name = "test-model"
	m := newStartingModel(cfg, session.New(), &tuiAsker{})
	m.initBuilder = blockedBuilder(release, started, done)
	m.width, m.height = 80, 24
	m.ready = true

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, outR)

	program := tea.NewProgram(m, tea.WithInput(inR), tea.WithOutput(outW))
	progDone := make(chan model, 1)
	go func() {
		final, err := program.Run()
		if err != nil {
			progDone <- m
			return
		}
		progDone <- final.(model)
	}()

	// Background init must have started; the first frame must render
	// while it is still blocked.
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("background runtime init never started")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(phases)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no View observed while init blocked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	first := phases[0]
	mu.Unlock()
	if first != startupStarting {
		t.Fatalf("first observed phase = %v, want starting", first)
	}

	inW.WriteString("\x03")
	select {
	case final := <-progDone:
		if final.startupPhase != startupStarting {
			t.Fatalf("final phase = %v, want starting (init still blocked)", final.startupPhase)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("program did not quit while init blocked")
	}
	outW.Close()
}

// TestStartupSubmitBlockedWhileStarting proves Enter does not start a
// stream before the runtime is ready.
func TestStartupSubmitBlockedWhileStarting(t *testing.T) {
	m := newStartingModel(&config.Config{}, session.New(), &tuiAsker{})
	m.registry = newRegistry()
	// SetValue (not keystroke typing) keeps the test independent of
	// paste-burst timing: the zero lastKeyAt reads as a deliberate submit.
	m.input.SetValue("hello")
	m.lastKeyAt = time.Now().Add(-time.Second)
	next, _ := m.Update(enterMsg())
	got := next.(model)
	if got.waiting {
		t.Fatal("stream started while runtime still initializing")
	}
	if strings.TrimSpace(got.status) == "" {
		t.Fatal("no status notice when submitting while starting")
	}
	if got.runtime != nil {
		t.Fatal("runtime installed without init completing")
	}
}

// TestStartupQuitAllowedWhileStarting proves /quit still terminates the
// program before readiness.
func TestStartupQuitAllowedWhileStarting(t *testing.T) {
	m := newStartingModel(&config.Config{}, session.New(), &tuiAsker{})
	m.registry = newRegistry()
	m.input.SetValue("/quit")
	m.lastKeyAt = time.Now().Add(-time.Second)
	next, cmd := m.Update(enterMsg())
	got := next.(model)
	if !got.quitting || cmd == nil {
		t.Fatal("/quit while starting did not quit")
	}
}

// TestStartupFailure surfaces a background init error without crashing
// and keeps the TUI quittable.
func TestStartupFailure(t *testing.T) {
	m := newStartingModel(&config.Config{}, session.New(), &tuiAsker{})
	next, _ := m.Update(runtimeReadyMsg{err: errStartupTestSentinel})
	got := next.(model)
	if got.startupPhase != startupFailed {
		t.Fatalf("phase = %v, want failed", got.startupPhase)
	}
	if got.startupErr == nil {
		t.Fatal("startup error not recorded")
	}
	if strings.TrimSpace(got.status) == "" {
		t.Fatal("failure not communicated in status")
	}
	// A failed TUI must still quit cleanly via Ctrl+C.
	after, cmd := got.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !after.(model).quitting || cmd == nil {
		t.Fatal("failed TUI did not quit on Ctrl+C")
	}
}

// TestStartupFailureBlocksSubmit proves failed startup never streams.
func TestStartupFailureBlocksSubmit(t *testing.T) {
	m := newStartingModel(&config.Config{}, session.New(), &tuiAsker{})
	m.registry = newRegistry()
	next, _ := m.Update(runtimeReadyMsg{err: errStartupTestSentinel})
	got := next.(model)
	got.input.SetValue("hello")
	got.lastKeyAt = time.Now().Add(-time.Second)
	after, _ := got.Update(enterMsg())
	if after.(model).waiting {
		t.Fatal("stream started after failed init")
	}
}

// TestStartupQuitDuringSlowInit proves quitting is prompt even when the
// background builder is slow, and the builder goroutine still terminates
// (no leak) once released.
func TestStartupQuitDuringSlowInit(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	done := make(chan struct{})

	m := newStartingModel(&config.Config{}, session.New(), &tuiAsker{})
	slow := func(cfg *config.Config, sess *session.Session) (*runtime.Runtime, error) {
		close(started)
		<-release
		close(done)
		return nil, errStartupTestSentinel
	}
	m.initBuilder = slow
	m.width, m.height = 80, 24
	m.ready = true

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, outR)

	program := tea.NewProgram(m, tea.WithInput(inR), tea.WithOutput(outW))
	progDone := make(chan struct{})
	go func() {
		_, _ = program.Run()
		close(progDone)
	}()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("background init never started")
	}
	quitStart := time.Now()
	inW.WriteString("\x03")
	select {
	case <-progDone:
	case <-time.After(10 * time.Second):
		t.Fatal("program did not quit during slow init")
	}
	if elapsed := time.Since(quitStart); elapsed > 5*time.Second {
		t.Fatalf("quit took %v during slow init", elapsed)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("background builder goroutine leaked after release")
	}
	outW.Close()
}

// TestStartupInitCmdNilBuilder guards legacy test models (no builder):
// Init must not fail.
func TestStartupInitCmdNilBuilder(t *testing.T) {
	m := newInputModel()
	if cmd := m.Init(); cmd == nil {
		t.Fatal("Init returned nil cmd")
	}
}
