package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"forcefield/internal/command"
	"forcefield/internal/config"
	"forcefield/internal/perfmark"
	"forcefield/internal/permissions"
	"forcefield/internal/recovery"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// startupPhase tracks background runtime initialization. The TUI renders
// its first frame while starting; it becomes ready only once the runtime
// is installed, or failed when initialization errored. It never claims
// ready before the runtime exists.
type startupPhase int

const (
	// startupReady means the runtime is installed and usable. It is the
	// zero value so models built without the startup path (legacy test
	// fixtures) behave exactly as before: ready with whatever runtime
	// they hold.
	startupReady startupPhase = iota
	// startupStarting means background init is still running: input that
	// needs the runtime is held with a notice, quit still works.
	startupStarting
	// startupFailed means init errored: the TUI stays alive to report
	// the error and quit cleanly, but never streams.
	startupFailed
)

// runtimeReadyMsg delivers the background-built runtime (or its error)
// into the event loop. It travels as a tea.Cmd result, never via
// program.Send, so a quit that lands mid-init cannot strand a sender on
// a stopped program: the message is simply dropped and the bounded init
// goroutine exits on its own.
type runtimeReadyMsg struct {
	rt  *runtime.Runtime
	err error
}

// runtimeBuilder constructs the runtime off the UI thread. Production
// runs the full construction (offline and bounded: config is already
// loaded, provider factories build locally, no network); tests inject
// slow or failing builders.
type runtimeBuilder func(cfg *config.Config, sess *session.Session) (*runtime.Runtime, error)

// defaultRuntimeBuilder mirrors the old synchronous construction order:
// runtime, then session healing and agent alignment. Session healing
// mutates sess, which is safe here because the UI thread does not touch
// the session until the runtime is installed (submission and session
// switching are gated on readiness).
func defaultRuntimeBuilder(cfg *config.Config, sess *session.Session) (*runtime.Runtime, error) {
	r, err := runtime.NewFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	recovery.Heal(sess)
	recovery.AlignAgent(r, sess)
	return r, nil
}

// viewPhaseHook observes the phase of every rendered frame. Nil in
// production; tests use it to prove the first frame precedes readiness.
var viewPhaseHook func(startupPhase)

// newStartingModel builds the smallest startup state capable of
// rendering the first useful frame: header labels from the already-
// loaded config, the saved transcript, the input box, and a starting
// phase. Nothing here touches the provider, skills, memory, tools,
// agents, or the network; those arrive via runtimeReadyMsg.
func newStartingModel(cfg *config.Config, sess *session.Session, asker permissions.Asker) model {
	agentName := "general"
	providerName := ""
	modelName := ""
	if cfg != nil {
		if n := strings.TrimSpace(cfg.Agent.Name); n != "" && !strings.EqualFold(n, "default") {
			agentName = n
		}
		providerName = cfg.Model.Provider
		modelName = cfg.Model.Name
	}
	return model{
		agentName:    agentName,
		providerName: providerName,
		modelName:    modelName,
		input:        newInput(),
		viewport:     viewport.New(0, 0),
		runtime:      nil,
		entries:      sessionEntries(sess),
		session:      sess,
		registry:     newRegistry(),
		activeTools:  make(map[string]int),
		following:    true,
		showActivity: true,
		mouseEnabled: true,
		status:       "initializing runtime…",
		startupPhase: startupStarting,
		initCfg:      cfg,
		initSess:     sess,
		initAsker:    asker,
		initBuilder:  defaultRuntimeBuilder,
	}
}

// initRuntimeCmd runs the runtime builder off the UI thread and reports
// back as runtimeReadyMsg. A nil builder (legacy test models) yields no
// command, preserving their exact Init behavior.
func (m model) initRuntimeCmd() tea.Cmd {
	builder := m.initBuilder
	cfg, sess := m.initCfg, m.initSess
	if builder == nil || cfg == nil || sess == nil {
		return nil
	}
	return func() tea.Msg {
		rt, err := builder(cfg, sess)
		return runtimeReadyMsg{rt: rt, err: err}
	}
}

// installRuntime swaps a built runtime into the model and flips the
// phase. On error the model enters failed: the error is recorded,
// reported in the transcript and status, and nothing claims ready.
func (m model) installRuntime(msg runtimeReadyMsg) model {
	asker := m.initAsker
	m.initCfg = nil
	m.initSess = nil
	m.initAsker = nil
	m.initBuilder = nil
	if msg.err != nil || msg.rt == nil {
		m.startupPhase = startupFailed
		if msg.err != nil {
			m.startupErr = msg.err
		} else {
			m.startupErr = fmt.Errorf("runtime unavailable")
		}
		m.status = "startup failed: " + m.startupErr.Error()
		m.entries = append(m.entries, chatEntry{Role: roleError, Content: m.status})
		m.refreshTranscript()
		return m
	}
	if asker != nil {
		msg.rt.SetPermissionAsker(asker)
	}
	m.runtime = msg.rt
	m.agentName = msg.rt.AgentDisplayName()
	m.providerName = msg.rt.CurrentProvider()
	m.modelName = msg.rt.CurrentModel()
	m.entries = sessionEntries(m.session)
	m.startupPhase = startupReady
	m.startupErr = nil
	m.status = ""
	m.refreshTranscript()
	perfmark.EventMem("runtime-ready")
	perfmark.Event("stage-session")
	return m
}

// startupBlocked reports the notice shown when input needs a runtime
// that is not ready yet.
func startupBlocked(phase startupPhase, err error) string {
	if phase == startupFailed && err != nil {
		return "startup failed: " + err.Error()
	}
	return "initializing runtime…"
}

// isQuitCommand reports whether task is the exit/quit command, which
// never needs a runtime and must work while starting or failed.
func isQuitCommand(task string) bool {
	parsed, ok := command.Parse(task)
	if !ok {
		return false
	}
	return parsed.Name == "exit" || parsed.Name == "quit"
}
