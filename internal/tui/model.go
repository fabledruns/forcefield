package tui

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"forcefield/internal/command"
	"forcefield/internal/config"
	"forcefield/internal/permissions"
	"forcefield/internal/runtime"
	"forcefield/internal/session"
)

// firstFrameMarked fires the first-frame startup marker exactly once per
// process. View has a value receiver, so per-model state cannot persist
// across calls; process-global once semantics are exactly right because
// a benchmark measures one TUI run per process.
var firstFrameMarked atomic.Bool

// firstUsefulFrameMarked fires the first-useful-frame startup marker
// exactly once per process: the first View with m.ready true, i.e.
// after the first WindowSizeMsg installed real dimensions. The
// initial View renders before that message (bubbletea draws once
// before handling resize), so first-frame can represent the
// "Starting Forcefield…" placeholder; first-useful-frame is the
// first frame that renders real content.
var firstUsefulFrameMarked atomic.Bool

// minTranscriptHeight is the smallest the scrollable transcript area is
// ever allowed to shrink to, so a very short terminal window still shows
// something usable instead of a zero-height viewport.
const minTranscriptHeight = 3

// headerRows reports how many terminal rows the rendered header actually
// occupies, so viewport sizing and mouse hit-testing both derive chrome
// geometry from the real rendering instead of a hand-maintained constant.
// (A stale constant here shifts every click target by the difference —
// the header renders one row, but was long assumed to be two.)
//
// There is no equivalent fixed footerHeight: the footer grows with the
// input box, which itself grows with however many lines the prompt holds
// (see minInputHeight/maxInputHeight and (*model).layout), so its height
// is computed dynamically instead.
func (m model) headerRows() int {
	if m.width <= 0 {
		return headerMinRows
	}
	return lipgloss.Height(m.renderHeader())
}

// headerMinRows bounds the header when no width has arrived yet and it
// cannot be measured.
const headerMinRows = 1

// minInputHeight and maxInputHeight bound how many rows the prompt input
// box is allowed to occupy. It grows automatically as the user types or
// pastes multiple lines, and shrinks back down on submit, but is capped
// so a very large paste can't swallow the whole terminal window.
const (
	minInputHeight = 1
	maxInputHeight = 6
)

// cachedBlock holds one transcript entry's last rendered block and the
// fingerprint that produced it, so streaming can reuse stable entries.
type cachedBlock struct {
	rendered  string
	lines     int
	role      role
	content   string
	streaming bool
	hovered   bool
	// turn and groupStart capture turn-group membership: a reused block
	// must have rendered with the same header suppression as before.
	turn       uint64
	groupStart bool
	// thinking, when present
	thinkingText      string
	thinkingExpanded  bool
	thinkingStreaming bool
	// sysExpanded tracks a collapsible system block's toggle.
	sysExpanded bool
	// sysHover is the hovered section of a grouped entry (-1 when none).
	// sysSections snapshots per-section toggles and sysOffsets their
	// header rows, so reuse stays exact across toggles and hovers.
	sysHover    int
	sysSections []bool
	sysOffsets  []int
	// tool, when present
	toolPresent   bool
	toolExpanded  bool
	toolFinished  bool
	toolEventType runtime.EventType
	toolErr       string
	toolContent   string
	toolStdout    string
	toolStderr    string
	toolHasExit   bool
	toolExitCode  int
	toolDuration  time.Duration
	toolArgsKey   string
}

// model is Forcefield's interactive chat state. It is a thin presentation
// layer: it renders a transcript and forwards each submitted message through
// the runtime's streaming agent loop. See the package doc for what it
// deliberately does not add.
type model struct {
	runtime  *runtime.Runtime
	session  *session.Session
	registry *command.Registry
	stream   <-chan runtime.Event

	assistantBuffer string

	// turnKind tracks what the in-flight turn is for: an ordinary chat
	// turn, a read-only /plan turn, or a /build turn executing the
	// accepted plan. Chat and build share the runtime's normal policy;
	// only plan turns narrow it. planBuffer accumulates the planning
	// turn's streamed text so a finished plan can be persisted even
	// though the shared assistant buffer is recycled per tool batch.
	// Both reset on every stream teardown (see stopStream).
	turnKind   turnKind
	planBuffer string

	agentName    string
	providerName string
	modelName    string

	entries  []chatEntry
	viewport viewport.Model
	input    textarea.Model

	// loadingFrame is the current frame of the compact block loading
	// indicator in the footer (see loading.go). It advances only on
	// loadingTickMsg while waiting is true, so the animation runs solely
	// during an active model/tool operation and never touches the
	// transcript.
	loadingFrame int

	// activeTools maps a running tool call's ID to the index of its live
	// status line in entries, so concurrent tool calls (the scheduler may
	// run several at once) each get their own line that's updated in
	// place as progress events arrive, instead of clobbering a single
	// shared status string.
	activeTools map[string]int

	// picker is non-nil while the /sessions modal is open. It owns
	// nothing beyond its own selection state; switching the active
	// session is still handled by the model.
	picker *sessionPicker

	// selectPicker is non-nil while the /provider or /model modal is
	// open. Like picker, it owns nothing beyond its own selection
	// state.
	selectPicker *selectPicker

	// notify delivers messages into the running program from background
	// work (model discovery). It is wired once Start owns the program;
	// nil outside a live program (most tests), where background work is
	// simply skipped.
	notify func(tea.Msg)

	// permissionPrompt is non-nil while a tool's "ask" permission
	// decision is awaiting an answer. See permission.go and asker.go.
	permissionPrompt *permissionPrompt

	// suggestions holds the slash commands whose name has the currently
	// typed prefix, sorted alphabetically; recomputed on every keystroke
	// by updateSuggestions and cleared once the input isn't an
	// in-progress command name (see completion.go). Empty/nil hides the
	// command palette entirely.
	suggestions []command.Command

	// suggestionCursor is the palette's highlighted row. Typing resets it
	// to the top; Up/Down move it with wraparound; Enter or Tab confirms
	// it into the input.
	suggestionCursor int

	width, height int
	waiting       bool // true while a runTask command is in flight
	status        string
	// notice is the Crush-style bottom status strip notification. Nil
	// means no notification, in which case the footer shows the normal
	// help/loading line. Non-nil overlays that line with a full-width
	// solid color strip (see statusbar.go), exactly like Crush's
	// Status.Draw which draws the info message over the help view.
	notice   *statusNotice
	quitting bool
	ready    bool // true once the first WindowSizeMsg has arrived

	// startupPhase tracks background runtime initialization (see
	// startup.go): the first frame renders while starting, and input
	// needing the runtime is held until ready or failed.
	startupPhase startupPhase
	startupErr   error
	// initCfg/initSess/initAsker/initBuilder carry the background init
	// inputs; installRuntime clears them once the runtime lands.
	initCfg     *config.Config
	initSess    *session.Session
	initAsker   permissions.Asker
	initBuilder runtimeBuilder

	// following is true while the viewport should stick to the bottom of
	// the transcript as new output streams in. Scrolling up pauses the
	// auto-follow so older output stays put while reading; scrolling back
	// to the bottom (or submitting a message) resumes it.
	following bool

	// streamGen tags the active stream; every waitForChunk message carries
	// the generation it was spawned with, so events from a replaced stream
	// (session switch, /clear, quit) are dropped instead of landing in the
	// new transcript. cancelStream cancels the active stream's context.
	streamGen    uint64
	cancelStream context.CancelFunc

	// showActivity toggles the transient model-activity labels (Thinking,
	// Planning, Running…) in the footer. Footer labels stay one-line
	// status phrases; the reasoning text itself lives in the transcript's
	// collapsible Thinking blocks, not here.
	showActivity bool

	// lastKeyAt timestamps the most recent key event, for paste-burst
	// detection: input drivers without bracketed paste (the Windows
	// console API) deliver a paste as a rapid keystroke burst whose
	// newlines are plain Enter events. A KeyEnter arriving within
	// pasteBurstWindow of the previous key is treated as a pasted newline,
	// not a submit. The window sits well under keyboard auto-repeat
	// (~33ms at the default Windows rate), which never bursts.
	lastKeyAt time.Time

	// mouseEnabled mirrors whether Bubble Tea's mouse tracking is on.
	// It is always enabled so cursor/mouse interactions (clicks, wheel,
	// hover, pickers) work without interruption. Text selection remains
	// available via the terminal's Shift+drag (or equivalent) without
	// needing a toggle; keyboard behavior is identical.
	mouseEnabled bool

	// saveErrorNotified records the session save error already shown in
	// the transcript, so a persistent failure warns once per distinct
	// error instead of once per turn. It resets when saves succeed
	// again, so a later recurrence is still reported.
	saveErrorNotified string

	// hoverID is the hit-region ID under the pointer, refreshed from
	// every routed mouse event. Renderers apply subtle emphasis only.
	hoverID string

	// spans maps transcript content rows to interactive entries (tool and
	// thinking blocks), rebuilt whenever the transcript re-renders.
	spans []contentSpan

	// transcript cache for M1/M5: per-entry rendered blocks to avoid
	// full glamour re-parse on every streaming chunk or input keystroke.
	tcacheWidth   int
	tcacheHoverID string
	tcacheBlocks  []cachedBlock
	tcacheContent string
	tcacheSpans   []contentSpan
}

// pasteBurstWindow is the maximum gap between keystrokes for the run to
// count as one paste burst; see model.lastKeyAt.
const pasteBurstWindow = 25 * time.Millisecond
