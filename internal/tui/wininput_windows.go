//go:build windows

package tui

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/erikgeiser/coninput"
	"golang.org/x/sys/windows"
)

var (
	kernel32ProcSetConsoleCtrlHandler = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")
)

func setConsoleCtrlHandler(handler uintptr, add bool) error {
	var addVal uintptr
	if add {
		addVal = 1
	}
	r, _, err := kernel32ProcSetConsoleCtrlHandler.Call(handler, addVal)
	if r == 0 {
		return err
	}
	return nil
}

// setupPlatformInput configures the Windows console input pump.
// It opens CONIN$, applies ENABLE_VIRTUAL_TERMINAL_INPUT and ENABLE_WINDOW_INPUT,
// sets tea.WithInput(nil) so Bubble Tea does not spawn a competing console reader,
// and feeds tea.Msg directly to programRef via Program.Send.
func setupPlatformInput(opts *[]tea.ProgramOption, programRef *atomic.Pointer[tea.Program]) (func(), error) {
	conin, err := windows.CreateFile(
		windows.StringToUTF16Ptr("CONIN$"),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		// Not an interactive console (e.g. redirected or headless); fall back to default
		return nil, nil
	}

	var origMode uint32
	if err := windows.GetConsoleMode(conin, &origMode); err != nil {
		_ = windows.CloseHandle(conin)
		return nil, nil
	}

	origCP, _ := windows.GetConsoleCP()
	const cpUTF8 = 65001
	_ = windows.SetConsoleCP(cpUTF8)

	// Disable line buffering, echo, and processed input.
	// Enable virtual terminal input (for ConPTY bracketed paste), window resize events,
	// extended flags, and mouse input.
	newMode := (origMode &^ (windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT)) |
		windows.ENABLE_VIRTUAL_TERMINAL_INPUT |
		windows.ENABLE_WINDOW_INPUT |
		windows.ENABLE_EXTENDED_FLAGS |
		windows.ENABLE_MOUSE_INPUT

	if err := windows.SetConsoleMode(conin, newMode); err != nil {
		_ = windows.CloseHandle(conin)
		return nil, fmt.Errorf("setting console mode (0x%08x): %w", newMode, err)
	}

	// Disable Bubble Tea's internal cancelreader/conInputReader so CONIN$ has one owner
	*opts = append(*opts, tea.WithInput(nil))

	pump := &windowsInputPump{
		conin:      conin,
		origMode:   origMode,
		origCP:     origCP,
		programRef: programRef,
		done:       make(chan struct{}),
		parser:     newInputParser(),
	}
	pump.parser.emit = pump.send

	ctrlCallback := windows.NewCallback(func(ctrlType uint32) uintptr {
		if ctrlType == windows.CTRL_C_EVENT || ctrlType == windows.CTRL_BREAK_EVENT {
			pump.send(tea.KeyMsg{Type: tea.KeyCtrlC})
			return 1 // handled: do not let default OS handler terminate process abruptly
		}
		return 0
	})
	if err := setConsoleCtrlHandler(ctrlCallback, true); err == nil {
		pump.ctrlHandler = ctrlCallback
	}

	// Query initial screen size and queue an initial WindowSizeMsg
	var csbi windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Stdout, &csbi); err == nil {
		w := int(csbi.Window.Right - csbi.Window.Left + 1)
		h := int(csbi.Window.Bottom - csbi.Window.Top + 1)
		if w > 0 && h > 0 {
			pump.send(tea.WindowSizeMsg{Width: w, Height: h})
		}
	}

	// Drain any early messages once programRef is assigned
	pump.startDrainLoop()

	pump.wg.Add(1)
	go pump.readLoop()

	cleanup := func() {
		pump.stop()
	}
	return cleanup, nil
}

type windowsInputPump struct {
	conin        windows.Handle
	origMode     uint32
	origCP       uint32
	programRef   *atomic.Pointer[tea.Program]
	done         chan struct{}
	wg           sync.WaitGroup
	mu           sync.Mutex
	dispatchMu   sync.Mutex
	stopped      bool
	earlyMsgs    []tea.Msg
	parser       *inputParser
	prevMouseBtn coninput.ButtonState
	ctrlHandler  uintptr
	sendMsg      func(tea.Msg)
}

const pumpStopGrace = 250 * time.Millisecond

func (p *windowsInputPump) startDrainLoop() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-p.done:
				// Pump is stopping; discard undrained early messages rather than
				// calling prog.Send on an unstarted or tearing-down program.
				return
			case <-ticker.C:
			}
			if p.programRef != nil && p.programRef.Load() != nil {
				p.drainEarlyMsgs()
				return
			}
		}
	}()
}

func (p *windowsInputPump) drainEarlyMsgs() {
	p.dispatchMu.Lock()
	defer p.dispatchMu.Unlock()

	p.mu.Lock()
	if p.stopped {
		p.earlyMsgs = nil
		p.mu.Unlock()
		return
	}
	var prog *tea.Program
	if p.programRef != nil {
		prog = p.programRef.Load()
	}
	if prog == nil || len(p.earlyMsgs) == 0 {
		p.mu.Unlock()
		return
	}
	msgs := p.earlyMsgs
	p.earlyMsgs = nil
	p.mu.Unlock()

	for _, m := range msgs {
		select {
		case <-p.done:
			return
		default:
		}
		prog.Send(m)
	}
}

func (p *windowsInputPump) send(msg tea.Msg) {
	p.dispatchMu.Lock()
	defer p.dispatchMu.Unlock()

	p.mu.Lock()
	if p.sendMsg != nil {
		send := p.sendMsg
		p.mu.Unlock()
		send(msg)
		return
	}

	if p.stopped {
		p.mu.Unlock()
		return
	}

	var prog *tea.Program
	if p.programRef != nil {
		prog = p.programRef.Load()
	}
	if prog == nil {
		p.earlyMsgs = append(p.earlyMsgs, msg)
		p.mu.Unlock()
		return
	}

	var msgs []tea.Msg
	if len(p.earlyMsgs) > 0 {
		msgs = p.earlyMsgs
		p.earlyMsgs = nil
	}
	p.mu.Unlock()

	for _, m := range msgs {
		select {
		case <-p.done:
			return
		default:
		}
		prog.Send(m)
	}
	select {
	case <-p.done:
		return
	default:
	}
	prog.Send(msg)
}

func (p *windowsInputPump) stop() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	p.earlyMsgs = nil
	if p.done != nil {
		close(p.done)
	}
	ctrlHandler := p.ctrlHandler
	p.ctrlHandler = 0
	p.mu.Unlock()

	waitDone := make(chan struct{})
	go func() {
		if p.parser != nil {
			p.parser.stopEscTimer()
		}
		if ctrlHandler != 0 {
			_ = setConsoleCtrlHandler(ctrlHandler, false)
		}
		p.wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-time.After(pumpStopGrace):
		// If a worker is still blocked in prog.Send (for instance when
		// programRef was published to an unstarted tea.Program without
		// Run/Kill), cancel the program context so Send returns and no
		// goroutine leaks.
		if p.programRef != nil {
			if prog := p.programRef.Load(); prog != nil {
				prog.Kill()
			}
		}
		<-waitDone
	}

	if p.conin != 0 && p.conin != windows.InvalidHandle {
		_ = windows.SetConsoleMode(p.conin, p.origMode)
		if p.origCP != 0 {
			_ = windows.SetConsoleCP(p.origCP)
		}
		_ = windows.CloseHandle(p.conin)
		p.conin = windows.InvalidHandle
	}
}

func (p *windowsInputPump) readLoop() {
	defer p.wg.Done()

	for {
		select {
		case <-p.done:
			return
		default:
		}

		// Wait up to 50ms for console input records to become available
		waitRes, err := windows.WaitForSingleObject(p.conin, 50)
		if err != nil {
			select {
			case <-p.done:
				return
			default:
				return
			}
		}

		if waitRes != windows.WAIT_OBJECT_0 {
			continue
		}

		events, err := coninput.GetNumberOfConsoleInputEvents(p.conin)
		if err != nil || events == 0 {
			continue
		}

		records, err := coninput.ReadNConsoleInputs(p.conin, events)
		if err != nil {
			select {
			case <-p.done:
				return
			default:
				return
			}
		}

		for _, rec := range records {
			select {
			case <-p.done:
				return
			default:
			}
			switch e := rec.Unwrap().(type) {
			case coninput.KeyEventRecord:
				p.parser.feedKeyRecord(e)

			case coninput.WindowBufferSizeEventRecord:
				w := int(e.Size.X)
				h := int(e.Size.Y)
				if w > 0 && h > 0 {
					p.send(tea.WindowSizeMsg{Width: w, Height: h})
				}

			case coninput.MouseEventRecord:
				p.handleMouseRecord(e)
			}
		}
	}
}

func (p *windowsInputPump) handleMouseRecord(e coninput.MouseEventRecord) {
	alt := e.ControlKeyState.Contains(coninput.LEFT_ALT_PRESSED | coninput.RIGHT_ALT_PRESSED)
	ctrl := e.ControlKeyState.Contains(coninput.LEFT_CTRL_PRESSED | coninput.RIGHT_CTRL_PRESSED)
	shift := e.ControlKeyState.Contains(coninput.SHIFT_PRESSED)
	x := int(e.MousePositon.X)
	y := int(e.MousePositon.Y)

	baseMsg := tea.MouseMsg{
		X:     x,
		Y:     y,
		Alt:   alt,
		Ctrl:  ctrl,
		Shift: shift,
	}

	if e.EventFlags == coninput.MOUSE_WHEELED {
		msg := baseMsg
		msg.Action = tea.MouseActionPress
		if e.WheelDirection > 0 {
			msg.Button = tea.MouseButtonWheelUp
		} else if e.WheelDirection < 0 {
			msg.Button = tea.MouseButtonWheelDown
		} else {
			return
		}
		p.send(msg)
		return
	}

	if e.EventFlags == coninput.MOUSE_MOVED {
		msg := baseMsg
		msg.Action = tea.MouseActionMotion
		if e.ButtonState.Contains(coninput.FROM_LEFT_1ST_BUTTON_PRESSED) {
			msg.Button = tea.MouseButtonLeft
		} else if e.ButtonState.Contains(coninput.RIGHTMOST_BUTTON_PRESSED) {
			msg.Button = tea.MouseButtonRight
		} else if e.ButtonState.Contains(coninput.FROM_LEFT_2ND_BUTTON_PRESSED) {
			msg.Button = tea.MouseButtonMiddle
		} else {
			msg.Button = tea.MouseButtonNone
		}
		p.send(msg)
		return
	}

	// Button press or release
	prev := p.prevMouseBtn
	curr := e.ButtonState
	p.prevMouseBtn = curr

	// Left button
	if curr.Contains(coninput.FROM_LEFT_1ST_BUTTON_PRESSED) && !prev.Contains(coninput.FROM_LEFT_1ST_BUTTON_PRESSED) {
		msg := baseMsg
		msg.Action = tea.MouseActionPress
		msg.Button = tea.MouseButtonLeft
		p.send(msg)
	} else if !curr.Contains(coninput.FROM_LEFT_1ST_BUTTON_PRESSED) && prev.Contains(coninput.FROM_LEFT_1ST_BUTTON_PRESSED) {
		msg := baseMsg
		msg.Action = tea.MouseActionRelease
		msg.Button = tea.MouseButtonLeft
		p.send(msg)
	}

	// Right button
	if curr.Contains(coninput.RIGHTMOST_BUTTON_PRESSED) && !prev.Contains(coninput.RIGHTMOST_BUTTON_PRESSED) {
		msg := baseMsg
		msg.Action = tea.MouseActionPress
		msg.Button = tea.MouseButtonRight
		p.send(msg)
	} else if !curr.Contains(coninput.RIGHTMOST_BUTTON_PRESSED) && prev.Contains(coninput.RIGHTMOST_BUTTON_PRESSED) {
		msg := baseMsg
		msg.Action = tea.MouseActionRelease
		msg.Button = tea.MouseButtonRight
		p.send(msg)
	}

	// Middle button
	if curr.Contains(coninput.FROM_LEFT_2ND_BUTTON_PRESSED) && !prev.Contains(coninput.FROM_LEFT_2ND_BUTTON_PRESSED) {
		msg := baseMsg
		msg.Action = tea.MouseActionPress
		msg.Button = tea.MouseButtonMiddle
		p.send(msg)
	} else if !curr.Contains(coninput.FROM_LEFT_2ND_BUTTON_PRESSED) && prev.Contains(coninput.FROM_LEFT_2ND_BUTTON_PRESSED) {
		msg := baseMsg
		msg.Action = tea.MouseActionRelease
		msg.Button = tea.MouseButtonMiddle
		p.send(msg)
	}
}

type pasteState int

const (
	stateNormal pasteState = iota
	stateCandidateStart
	stateInPaste
	stateCandidateEnd
)

const (
	bpStart = "\x1b[200~"
	bpEnd   = "\x1b[201~"
)

type inputParser struct {
	mu              sync.Mutex
	state           pasteState
	candidateBuf    []rune
	pasteBuf        []rune
	endCandidateBuf []rune
	hasLead         bool
	leadSurrogate   rune
	escTimer        *time.Timer
	emit            func(tea.Msg)
}

func newInputParser() *inputParser {
	return &inputParser{
		state: stateNormal,
		emit:  func(tea.Msg) {},
	}
}

func (p *inputParser) startEscTimerLocked() {
	p.stopEscTimerLocked()
	p.escTimer = time.AfterFunc(25*time.Millisecond, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.state == stateCandidateStart {
			p.flushCandidateStart()
			p.state = stateNormal
		}
	})
}

func (p *inputParser) stopEscTimerLocked() {
	if p.escTimer != nil {
		p.escTimer.Stop()
		p.escTimer = nil
	}
}

func (p *inputParser) stopEscTimer() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopEscTimerLocked()
}

func (p *inputParser) feedKeyRecord(e coninput.KeyEventRecord) {
	if !e.KeyDown {
		return
	}

	// Ignore modifier-only key events
	switch e.VirtualKeyCode {
	case coninput.VK_SHIFT, 0x11 /* VK_CONTROL */, 0x12 /* VK_MENU */, 0x14, /* VK_CAPITAL */
		0x5B /* VK_LWIN */, 0x5C, /* VK_RWIN */
		0xA0 /* VK_LSHIFT */, 0xA1, /* VK_RSHIFT */
		0xA2 /* VK_LCONTROL */, 0xA3, /* VK_RCONTROL */
		0xA4 /* VK_LMENU */, 0xA5 /* VK_RMENU */ :
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Decode UTF-16 surrogates
	var r rune
	if utf16.IsSurrogate(e.Char) {
		if 0xD800 <= e.Char && e.Char <= 0xDBFF {
			// High surrogate
			p.hasLead = true
			p.leadSurrogate = e.Char
			return
		} else if 0xDC00 <= e.Char && e.Char <= 0xDFFF {
			// Low surrogate
			if p.hasLead {
				r = utf16.DecodeRune(p.leadSurrogate, e.Char)
				p.hasLead = false
			} else {
				r = e.Char
			}
		} else {
			r = e.Char
		}
	} else {
		p.hasLead = false
		r = e.Char
	}

	if r == 0 && e.VirtualKeyCode == 0 {
		return
	}

	repeat := int(e.RepeatCount)
	if repeat < 1 {
		repeat = 1
	}

	for i := 0; i < repeat; i++ {
		p.processRune(r, e)
	}
}

func (p *inputParser) processRune(r rune, e coninput.KeyEventRecord) {
	alt := e.ControlKeyState.Contains(coninput.LEFT_ALT_PRESSED | coninput.RIGHT_ALT_PRESSED)
	ctrl := e.ControlKeyState.Contains(coninput.LEFT_CTRL_PRESSED | coninput.RIGHT_CTRL_PRESSED)

	// Ctrl+C must NEVER be swallowed, regardless of parser state.
	isCtrlC := r == 3 || e.VirtualKeyCode == 0x03 /* VK_CANCEL */ || (ctrl && (e.VirtualKeyCode == 'C' || e.VirtualKeyCode == 'c'))
	if isCtrlC {
		p.stopEscTimerLocked()
		p.candidateBuf = nil
		p.endCandidateBuf = nil
		p.pasteBuf = nil
		p.state = stateNormal
		p.emit(tea.KeyMsg{Type: tea.KeyCtrlC})
		return
	}

	// Backspace must NEVER be swallowed in candidate states
	isBackspace := e.VirtualKeyCode == coninput.VK_BACK || r == 0x7f || (r == 0x08 && !ctrl)

	switch p.state {
	case stateNormal:
		// Physical Escape key press (VK_ESCAPE)
		if e.VirtualKeyCode == coninput.VK_ESCAPE {
			p.emit(tea.KeyMsg{Type: tea.KeyEscape, Alt: alt})
			return
		}

		// Escape byte (synthetic VT sequence or ESC byte)
		if r == 0x1b {
			p.state = stateCandidateStart
			p.candidateBuf = []rune{0x1b}
			p.startEscTimerLocked()
			return
		}

		p.emitKey(r, e)

	case stateCandidateStart:
		if isBackspace {
			p.stopEscTimerLocked()
			if len(p.candidateBuf) == 1 && p.candidateBuf[0] == 0x1b {
				p.candidateBuf = nil
				p.state = stateNormal
				p.emit(tea.KeyMsg{Type: tea.KeyBackspace, Alt: true})
				return
			}
			p.flushCandidateStart()
			p.state = stateNormal
			p.emit(tea.KeyMsg{Type: tea.KeyBackspace, Alt: alt})
			return
		}

		if r == 0x1b && len(p.candidateBuf) == 1 && p.candidateBuf[0] == 0x1b {
			// Alt+Escape or double Escape
			p.stopEscTimerLocked()
			p.emit(tea.KeyMsg{Type: tea.KeyEscape, Alt: true})
			p.candidateBuf = nil
			p.state = stateNormal
			return
		}

		p.candidateBuf = append(p.candidateBuf, r)
		seq := string(p.candidateBuf)

		// 1. Check exact match for bracketed paste start
		if seq == bpStart {
			p.stopEscTimerLocked()
			p.state = stateInPaste
			p.pasteBuf = nil
			p.candidateBuf = nil
			return
		}

		// 2. Check prefix match for bpStart
		if strings.HasPrefix(bpStart, seq) {
			p.startEscTimerLocked()
			return
		}

		// 3. Focus reporting: ESC [ I (focus) or ESC [ O (blur)
		if seq == "\x1b[I" {
			p.stopEscTimerLocked()
			p.emit(tea.FocusMsg{})
			p.state = stateNormal
			p.candidateBuf = nil
			return
		}
		if seq == "\x1b[O" {
			p.stopEscTimerLocked()
			p.emit(tea.BlurMsg{})
			p.state = stateNormal
			p.candidateBuf = nil
			return
		}

		// 4. SGR Mouse reporting: ESC [ < Cb ; Cx ; Cy (M or m)
		if strings.HasPrefix(seq, "\x1b[<") {
			if len(p.candidateBuf) == 3 {
				// Just received "\x1b[<"
				p.startEscTimerLocked()
				return
			}
			if r == 'M' || r == 'm' {
				p.stopEscTimerLocked()
				if msg, ok := parseSGRMouse(seq); ok {
					p.emit(msg)
					p.state = stateNormal
					p.candidateBuf = nil
					return
				}
				p.flushCandidateStart()
				p.state = stateNormal
				return
			}
			if (r >= '0' && r <= '9') || r == ';' {
				if len(p.candidateBuf) <= 32 {
					p.startEscTimerLocked()
					return
				}
			}
			p.stopEscTimerLocked()
			p.flushCandidateStart()
			p.state = stateNormal
			return
		}

		// 5. X10 Mouse reporting: ESC [ M Cb Cx Cy (6 runes)
		if strings.HasPrefix(seq, "\x1b[M") {
			if len(p.candidateBuf) < 6 {
				p.startEscTimerLocked()
				return
			}
			p.stopEscTimerLocked()
			if len(p.candidateBuf) == 6 {
				if msg, ok := parseX10Mouse(seq); ok {
					p.emit(msg)
					p.state = stateNormal
					p.candidateBuf = nil
					return
				}
			}
			p.flushCandidateStart()
			p.state = stateNormal
			return
		}

		// 6. Check exact match for known VT sequence
		if msg, ok := matchVTSequence(seq); ok {
			p.stopEscTimerLocked()
			p.emit(msg)
			p.state = stateNormal
			p.candidateBuf = nil
			return
		}

		// 7. Check prefix match for any known VT sequence
		if isVTPrefix(seq) && len(p.candidateBuf) < 16 {
			p.startEscTimerLocked()
			return
		}

		// 8. Not a prefix of any known sequence: flush candidateBuf
		p.stopEscTimerLocked()
		p.flushCandidateStart()
		p.state = stateNormal

	case stateInPaste:
		if r == 0x1b {
			p.state = stateCandidateEnd
			p.endCandidateBuf = []rune{0x1b}
			return
		}
		if r > 0 {
			p.pasteBuf = append(p.pasteBuf, r)
		}

	case stateCandidateEnd:
		p.endCandidateBuf = append(p.endCandidateBuf, r)
		seq := string(p.endCandidateBuf)

		// 1. Check exact match for bracketed paste end
		if seq == bpEnd {
			p.emit(tea.KeyMsg{
				Type:  tea.KeyRunes,
				Runes: p.pasteBuf,
				Paste: true,
			})
			p.state = stateNormal
			p.pasteBuf = nil
			p.endCandidateBuf = nil
			return
		}

		// 2. Check prefix match for bpEnd
		if strings.HasPrefix(bpEnd, seq) {
			return
		}

		// 3. Mismatch: candidate was not bpEnd! All runes belong to paste payload.
		p.pasteBuf = append(p.pasteBuf, p.endCandidateBuf...)
		p.endCandidateBuf = nil
		p.state = stateInPaste
	}
}

func (p *inputParser) flushCandidateStart() {
	if len(p.candidateBuf) == 0 {
		return
	}
	seq := string(p.candidateBuf)
	if msg, ok := matchVTSequence(seq); ok {
		p.emit(msg)
		p.candidateBuf = nil
		return
	}

	if p.candidateBuf[0] == 0x1b {
		if len(p.candidateBuf) == 1 {
			p.emit(tea.KeyMsg{Type: tea.KeyEscape})
		} else if len(p.candidateBuf) == 2 {
			r2 := p.candidateBuf[1]
			if r2 == '[' || r2 == 'O' {
				p.emit(tea.KeyMsg{Type: tea.KeyEscape})
				p.emit(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r2}})
			} else {
				switch r2 {
				case '\r':
					p.emit(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
				case '\n':
					p.emit(tea.KeyMsg{Type: tea.KeyCtrlJ, Alt: true})
				case '\t':
					p.emit(tea.KeyMsg{Type: tea.KeyTab, Alt: true})
				case 0x7f, 0x08:
					p.emit(tea.KeyMsg{Type: tea.KeyBackspace, Alt: true})
				case 0x1b:
					p.emit(tea.KeyMsg{Type: tea.KeyEscape, Alt: true})
				default:
					p.emit(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r2}, Alt: true})
				}
			}
		} else {
			p.emit(tea.KeyMsg{Type: tea.KeyEscape})
			p.emit(tea.KeyMsg{Type: tea.KeyRunes, Runes: p.candidateBuf[1:]})
		}
	} else {
		p.emit(tea.KeyMsg{Type: tea.KeyRunes, Runes: p.candidateBuf})
	}
	p.candidateBuf = nil
}

func (p *inputParser) emitKey(r rune, e coninput.KeyEventRecord) {
	alt := e.ControlKeyState.Contains(coninput.LEFT_ALT_PRESSED | coninput.RIGHT_ALT_PRESSED)
	ctrl := e.ControlKeyState.Contains(coninput.LEFT_CTRL_PRESSED | coninput.RIGHT_CTRL_PRESSED)
	shift := e.ControlKeyState.Contains(coninput.SHIFT_PRESSED)

	// Ctrl+C check
	if r == 3 || e.VirtualKeyCode == 0x03 /* VK_CANCEL */ || (ctrl && (e.VirtualKeyCode == 'C' || e.VirtualKeyCode == 'c')) {
		p.emit(tea.KeyMsg{Type: tea.KeyCtrlC})
		return
	}

	// Backspace check:
	// VK_BACK, ASCII DEL (0x7f, ConPTY Backspace), or ASCII BS (0x08 without ctrl)
	if e.VirtualKeyCode == coninput.VK_BACK || r == 0x7f || (r == 0x08 && !ctrl) {
		p.emit(tea.KeyMsg{Type: tea.KeyBackspace, Alt: alt})
		return
	}

	// Enter / Return:
	if e.VirtualKeyCode == coninput.VK_RETURN {
		if shift || ctrl || r == '\n' {
			p.emit(tea.KeyMsg{Type: tea.KeyCtrlJ, Alt: alt})
		} else {
			p.emit(tea.KeyMsg{Type: tea.KeyEnter, Alt: alt})
		}
		return
	}
	if r == '\r' {
		if shift || ctrl {
			p.emit(tea.KeyMsg{Type: tea.KeyCtrlJ, Alt: alt})
		} else {
			p.emit(tea.KeyMsg{Type: tea.KeyEnter, Alt: alt})
		}
		return
	}
	if r == '\n' {
		p.emit(tea.KeyMsg{Type: tea.KeyCtrlJ, Alt: alt})
		return
	}

	// Tab:
	if e.VirtualKeyCode == coninput.VK_TAB {
		if shift {
			p.emit(tea.KeyMsg{Type: tea.KeyShiftTab, Alt: alt})
		} else {
			p.emit(tea.KeyMsg{Type: tea.KeyTab, Alt: alt})
		}
		return
	}
	if r == '\t' {
		if shift {
			p.emit(tea.KeyMsg{Type: tea.KeyShiftTab, Alt: alt})
		} else {
			p.emit(tea.KeyMsg{Type: tea.KeyTab, Alt: alt})
		}
		return
	}

	// Navigation keys by VirtualKeyCode
	switch e.VirtualKeyCode {
	case coninput.VK_UP:
		if ctrl {
			p.emit(tea.KeyMsg{Type: tea.KeyCtrlUp, Alt: alt})
		} else if shift {
			p.emit(tea.KeyMsg{Type: tea.KeyShiftUp, Alt: alt})
		} else {
			p.emit(tea.KeyMsg{Type: tea.KeyUp, Alt: alt})
		}
		return
	case coninput.VK_DOWN:
		if ctrl {
			p.emit(tea.KeyMsg{Type: tea.KeyCtrlDown, Alt: alt})
		} else if shift {
			p.emit(tea.KeyMsg{Type: tea.KeyShiftDown, Alt: alt})
		} else {
			p.emit(tea.KeyMsg{Type: tea.KeyDown, Alt: alt})
		}
		return
	case coninput.VK_LEFT:
		if ctrl {
			p.emit(tea.KeyMsg{Type: tea.KeyCtrlLeft, Alt: alt})
		} else if shift {
			p.emit(tea.KeyMsg{Type: tea.KeyShiftLeft, Alt: alt})
		} else {
			p.emit(tea.KeyMsg{Type: tea.KeyLeft, Alt: alt})
		}
		return
	case coninput.VK_RIGHT:
		if ctrl {
			p.emit(tea.KeyMsg{Type: tea.KeyCtrlRight, Alt: alt})
		} else if shift {
			p.emit(tea.KeyMsg{Type: tea.KeyShiftRight, Alt: alt})
		} else {
			p.emit(tea.KeyMsg{Type: tea.KeyRight, Alt: alt})
		}
		return
	case coninput.VK_HOME:
		if ctrl {
			p.emit(tea.KeyMsg{Type: tea.KeyCtrlHome, Alt: alt})
		} else if shift {
			p.emit(tea.KeyMsg{Type: tea.KeyShiftHome, Alt: alt})
		} else {
			p.emit(tea.KeyMsg{Type: tea.KeyHome, Alt: alt})
		}
		return
	case coninput.VK_END:
		if ctrl {
			p.emit(tea.KeyMsg{Type: tea.KeyCtrlEnd, Alt: alt})
		} else if shift {
			p.emit(tea.KeyMsg{Type: tea.KeyShiftEnd, Alt: alt})
		} else {
			p.emit(tea.KeyMsg{Type: tea.KeyEnd, Alt: alt})
		}
		return
	case coninput.VK_PRIOR: // Page Up
		if ctrl {
			p.emit(tea.KeyMsg{Type: tea.KeyCtrlPgUp, Alt: alt})
		} else {
			p.emit(tea.KeyMsg{Type: tea.KeyPgUp, Alt: alt})
		}
		return
	case coninput.VK_NEXT: // Page Down
		if ctrl {
			p.emit(tea.KeyMsg{Type: tea.KeyCtrlPgDown, Alt: alt})
		} else {
			p.emit(tea.KeyMsg{Type: tea.KeyPgDown, Alt: alt})
		}
		return
	case coninput.VK_DELETE:
		p.emit(tea.KeyMsg{Type: tea.KeyDelete, Alt: alt})
		return
	case coninput.VK_INSERT:
		p.emit(tea.KeyMsg{Type: tea.KeyInsert, Alt: alt})
		return
	}

	// Function keys F1-F12
	switch e.VirtualKeyCode {
	case coninput.VK_F1:
		p.emit(tea.KeyMsg{Type: tea.KeyF1, Alt: alt})
		return
	case coninput.VK_F2:
		p.emit(tea.KeyMsg{Type: tea.KeyF2, Alt: alt})
		return
	case coninput.VK_F3:
		p.emit(tea.KeyMsg{Type: tea.KeyF3, Alt: alt})
		return
	case coninput.VK_F4:
		p.emit(tea.KeyMsg{Type: tea.KeyF4, Alt: alt})
		return
	case coninput.VK_F5:
		p.emit(tea.KeyMsg{Type: tea.KeyF5, Alt: alt})
		return
	case coninput.VK_F6:
		p.emit(tea.KeyMsg{Type: tea.KeyF6, Alt: alt})
		return
	case coninput.VK_F7:
		p.emit(tea.KeyMsg{Type: tea.KeyF7, Alt: alt})
		return
	case coninput.VK_F8:
		p.emit(tea.KeyMsg{Type: tea.KeyF8, Alt: alt})
		return
	case coninput.VK_F9:
		p.emit(tea.KeyMsg{Type: tea.KeyF9, Alt: alt})
		return
	case coninput.VK_F10:
		p.emit(tea.KeyMsg{Type: tea.KeyF10, Alt: alt})
		return
	case coninput.VK_F11:
		p.emit(tea.KeyMsg{Type: tea.KeyF11, Alt: alt})
		return
	case coninput.VK_F12:
		p.emit(tea.KeyMsg{Type: tea.KeyF12, Alt: alt})
		return
	}

	// Control key chords:
	// In ConPTY, control characters 1..26 arrive as runes with VK=0 and CtrlState=0.
	// In ConHost, they arrive with ctrl=true and VK in 'A'..'Z' or rune 1..26.
	// Exclude 9 (Tab), 10 (Ctrl+J/LF), 13 (Enter/CR) as they are handled above.
	if r >= 1 && r <= 26 && r != 9 && r != 10 && r != 13 {
		p.emit(tea.KeyMsg{Type: tea.KeyCtrlA + tea.KeyType(r-1), Alt: alt})
		return
	}
	if ctrl && !alt {
		if e.VirtualKeyCode >= 'A' && e.VirtualKeyCode <= 'Z' {
			p.emit(tea.KeyMsg{Type: tea.KeyCtrlA + tea.KeyType(e.VirtualKeyCode-'A')})
			return
		}
		if e.VirtualKeyCode >= 'a' && e.VirtualKeyCode <= 'z' {
			p.emit(tea.KeyMsg{Type: tea.KeyCtrlA + tea.KeyType(e.VirtualKeyCode-'a')})
			return
		}
		if e.VirtualKeyCode == coninput.VK_SPACE || r == 0 {
			p.emit(tea.KeyMsg{Type: tea.KeyCtrlAt})
			return
		}
	}

	// Normal runes / Space
	if r == ' ' || e.VirtualKeyCode == coninput.VK_SPACE {
		p.emit(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}, Alt: alt})
	} else if r > 0 {
		p.emit(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: alt})
	}
}

func isVTPrefix(seq string) bool {
	if strings.HasPrefix(bpStart, seq) {
		return true
	}
	if strings.HasPrefix("\x1b[<", seq) || strings.HasPrefix(seq, "\x1b[<") {
		return true
	}
	if strings.HasPrefix("\x1b[M", seq) || (strings.HasPrefix(seq, "\x1b[M") && len(seq) < 6) {
		return true
	}
	if strings.HasPrefix("\x1b[I", seq) || strings.HasPrefix("\x1b[O", seq) {
		return true
	}
	for k := range knownVTSequences {
		if strings.HasPrefix(k, seq) {
			return true
		}
	}
	return false
}

func parseSGRMouse(seq string) (tea.MouseMsg, bool) {
	if len(seq) < 6 || !strings.HasPrefix(seq, "\x1b[<") {
		return tea.MouseMsg{}, false
	}
	last := seq[len(seq)-1]
	if last != 'M' && last != 'm' {
		return tea.MouseMsg{}, false
	}
	parts := strings.Split(seq[3:len(seq)-1], ";")
	if len(parts) != 3 {
		return tea.MouseMsg{}, false
	}
	b, err1 := strconv.Atoi(parts[0])
	x, err2 := strconv.Atoi(parts[1])
	y, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return tea.MouseMsg{}, false
	}

	release := (last == 'm')
	m := parseVTMouseButton(b, true)

	if m.Action != tea.MouseActionMotion && !m.IsWheel() && release {
		m.Action = tea.MouseActionRelease
	}

	// (1,1) is upper-left in VT coordinates; 0-based in Bubble Tea
	m.X = x - 1
	m.Y = y - 1
	if m.X < 0 {
		m.X = 0
	}
	if m.Y < 0 {
		m.Y = 0
	}

	return tea.MouseMsg(m), true
}

func parseX10Mouse(seq string) (tea.MouseMsg, bool) {
	r := []rune(seq)
	if len(r) != 6 || !strings.HasPrefix(seq, "\x1b[M") {
		return tea.MouseMsg{}, false
	}
	cb := int(r[3])
	cx := int(r[4])
	cy := int(r[5])

	m := parseVTMouseButton(cb, false)
	m.X = cx - 32 - 1
	m.Y = cy - 32 - 1
	if m.X < 0 {
		m.X = 0
	}
	if m.Y < 0 {
		m.Y = 0
	}

	return tea.MouseMsg(m), true
}

func parseVTMouseButton(b int, isSGR bool) tea.MouseEvent {
	var m tea.MouseEvent
	e := b
	if !isSGR {
		e -= 32
	}

	const (
		bitShift  = 0b0000_0100
		bitAlt    = 0b0000_1000
		bitCtrl   = 0b0001_0000
		bitMotion = 0b0010_0000
		bitWheel  = 0b0100_0000
		bitAdd    = 0b1000_0000

		bitsMask = 0b0000_0011
	)

	if e&bitAdd != 0 {
		m.Button = tea.MouseButtonBackward + tea.MouseButton(e&bitsMask)
	} else if e&bitWheel != 0 {
		m.Button = tea.MouseButtonWheelUp + tea.MouseButton(e&bitsMask)
	} else {
		m.Button = tea.MouseButtonLeft + tea.MouseButton(e&bitsMask)
		if e&bitsMask == bitsMask {
			m.Action = tea.MouseActionRelease
			m.Button = tea.MouseButtonNone
		}
	}

	if e&bitMotion != 0 && !m.IsWheel() {
		m.Action = tea.MouseActionMotion
	}

	m.Alt = e&bitAlt != 0
	m.Ctrl = e&bitCtrl != 0
	m.Shift = e&bitShift != 0

	switch {
	case m.Button == tea.MouseButtonLeft && m.Action == tea.MouseActionPress:
		m.Type = tea.MouseLeft
	case m.Button == tea.MouseButtonMiddle && m.Action == tea.MouseActionPress:
		m.Type = tea.MouseMiddle
	case m.Button == tea.MouseButtonRight && m.Action == tea.MouseActionPress:
		m.Type = tea.MouseRight
	case m.Button == tea.MouseButtonNone && m.Action == tea.MouseActionRelease:
		m.Type = tea.MouseRelease
	case m.Button == tea.MouseButtonWheelUp && m.Action == tea.MouseActionPress:
		m.Type = tea.MouseWheelUp
	case m.Button == tea.MouseButtonWheelDown && m.Action == tea.MouseActionPress:
		m.Type = tea.MouseWheelDown
	case m.Button == tea.MouseButtonWheelLeft && m.Action == tea.MouseActionPress:
		m.Type = tea.MouseWheelLeft
	case m.Button == tea.MouseButtonWheelRight && m.Action == tea.MouseActionPress:
		m.Type = tea.MouseWheelRight
	case m.Button == tea.MouseButtonBackward && m.Action == tea.MouseActionPress:
		m.Type = tea.MouseBackward
	case m.Button == tea.MouseButtonForward && m.Action == tea.MouseActionPress:
		m.Type = tea.MouseForward
	case m.Action == tea.MouseActionMotion:
		m.Type = tea.MouseMotion
		switch m.Button {
		case tea.MouseButtonLeft:
			m.Type = tea.MouseLeft
		case tea.MouseButtonMiddle:
			m.Type = tea.MouseMiddle
		case tea.MouseButtonRight:
			m.Type = tea.MouseRight
		case tea.MouseButtonBackward:
			m.Type = tea.MouseBackward
		case tea.MouseButtonForward:
			m.Type = tea.MouseForward
		}
	default:
		m.Type = tea.MouseUnknown
	}

	return m
}

func matchVTSequence(seq string) (tea.KeyMsg, bool) {
	msg, ok := knownVTSequences[seq]
	return msg, ok
}

var knownVTSequences = map[string]tea.KeyMsg{
	// Arrows
	"\x1b[A": {Type: tea.KeyUp},
	"\x1b[B": {Type: tea.KeyDown},
	"\x1b[C": {Type: tea.KeyRight},
	"\x1b[D": {Type: tea.KeyLeft},
	"\x1bOA": {Type: tea.KeyUp},
	"\x1bOB": {Type: tea.KeyDown},
	"\x1bOC": {Type: tea.KeyRight},
	"\x1bOD": {Type: tea.KeyLeft},

	// Modified arrows (Shift)
	"\x1b[1;2A": {Type: tea.KeyShiftUp},
	"\x1b[1;2B": {Type: tea.KeyShiftDown},
	"\x1b[1;2C": {Type: tea.KeyShiftRight},
	"\x1b[1;2D": {Type: tea.KeyShiftLeft},

	// Modified arrows (Alt)
	"\x1b[1;3A": {Type: tea.KeyUp, Alt: true},
	"\x1b[1;3B": {Type: tea.KeyDown, Alt: true},
	"\x1b[1;3C": {Type: tea.KeyRight, Alt: true},
	"\x1b[1;3D": {Type: tea.KeyLeft, Alt: true},

	// Modified arrows (Ctrl)
	"\x1b[1;5A": {Type: tea.KeyCtrlUp},
	"\x1b[1;5B": {Type: tea.KeyCtrlDown},
	"\x1b[1;5C": {Type: tea.KeyCtrlRight},
	"\x1b[1;5D": {Type: tea.KeyCtrlLeft},

	// Home / End
	"\x1b[H":    {Type: tea.KeyHome},
	"\x1b[F":    {Type: tea.KeyEnd},
	"\x1b[1~":   {Type: tea.KeyHome},
	"\x1b[4~":   {Type: tea.KeyEnd},
	"\x1b[7~":   {Type: tea.KeyHome},
	"\x1b[8~":   {Type: tea.KeyEnd},
	"\x1bOH":    {Type: tea.KeyHome},
	"\x1bOF":    {Type: tea.KeyEnd},
	"\x1b[1;5H": {Type: tea.KeyCtrlHome},
	"\x1b[1;5F": {Type: tea.KeyCtrlEnd},

	// Page Up / Page Down
	"\x1b[5~":   {Type: tea.KeyPgUp},
	"\x1b[6~":   {Type: tea.KeyPgDown},
	"\x1b[5;5~": {Type: tea.KeyCtrlPgUp},
	"\x1b[6;5~": {Type: tea.KeyCtrlPgDown},

	// Insert / Delete
	"\x1b[2~": {Type: tea.KeyInsert},
	"\x1b[3~": {Type: tea.KeyDelete},

	// Backtab
	"\x1b[Z": {Type: tea.KeyShiftTab},

	// Alt+Enter
	"\x1b\r": {Type: tea.KeyEnter, Alt: true},
	"\x1b\n": {Type: tea.KeyCtrlJ, Alt: true},

	// Function keys
	"\x1bOP":   {Type: tea.KeyF1},
	"\x1bOQ":   {Type: tea.KeyF2},
	"\x1bOR":   {Type: tea.KeyF3},
	"\x1bOS":   {Type: tea.KeyF4},
	"\x1b[11~": {Type: tea.KeyF1},
	"\x1b[12~": {Type: tea.KeyF2},
	"\x1b[13~": {Type: tea.KeyF3},
	"\x1b[14~": {Type: tea.KeyF4},
	"\x1b[15~": {Type: tea.KeyF5},
	"\x1b[17~": {Type: tea.KeyF6},
	"\x1b[18~": {Type: tea.KeyF7},
	"\x1b[19~": {Type: tea.KeyF8},
	"\x1b[20~": {Type: tea.KeyF9},
	"\x1b[21~": {Type: tea.KeyF10},
	"\x1b[23~": {Type: tea.KeyF11},
	"\x1b[24~": {Type: tea.KeyF12},
}
