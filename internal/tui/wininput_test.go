//go:build windows

package tui

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/erikgeiser/coninput"
	"golang.org/x/sys/windows"
)

func collectParserMessages(feed func(p *inputParser)) []tea.Msg {
	var msgs []tea.Msg
	p := newInputParser()
	p.emit = func(m tea.Msg) {
		msgs = append(msgs, m)
	}
	feed(p)
	return msgs
}

func feedRunes(p *inputParser, s string) {
	for _, r := range s {
		if utf16.IsSurrogate(r) {
			// If already surrogate (not typical in string iteration, but handle):
			p.feedKeyRecord(coninput.KeyEventRecord{
				KeyDown: true,
				Char:    r,
			})
		} else {
			p.feedKeyRecord(coninput.KeyEventRecord{
				KeyDown: true,
				Char:    r,
			})
		}
	}
}

func TestInputParser_SingleLinePaste(t *testing.T) {
	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[200~hello world\x1b[201~")
	})

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}

	km, ok := msgs[0].(tea.KeyMsg)
	if !ok {
		t.Fatalf("expected tea.KeyMsg, got %T", msgs[0])
	}

	if !km.Paste {
		t.Errorf("expected Paste=true, got false")
	}

	if string(km.Runes) != "hello world" {
		t.Errorf("got runes %q, want %q", string(km.Runes), "hello world")
	}
}

func TestInputParser_MultilinePasteCRLF(t *testing.T) {
	raw := "\x1b[200~line 1\r\nline 2\r\nline 3\x1b[201~"
	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, raw)
	})

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}

	km, ok := msgs[0].(tea.KeyMsg)
	if !ok || !km.Paste {
		t.Fatalf("expected KeyMsg with Paste=true, got %+v", msgs[0])
	}

	want := "line 1\r\nline 2\r\nline 3"
	if string(km.Runes) != want {
		t.Errorf("got runes %q, want %q", string(km.Runes), want)
	}
}

func TestInputParser_UnicodeAndEmojiSurrogates(t *testing.T) {
	// Emoji 🚀 is U+1F680, which encodes to UTF-16 surrogates: 0xD83D, 0xDE80
	r1, r2 := utf16.EncodeRune('🚀')

	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[200~héllo wörld 日本語 ✓ → αβγ ")
		// Feed surrogate pair as separate Windows KeyEventRecords
		p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, Char: r1})
		p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, Char: r2})
		feedRunes(p, "\x1b[201~")
	})

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}

	km, ok := msgs[0].(tea.KeyMsg)
	if !ok || !km.Paste {
		t.Fatalf("expected KeyMsg with Paste=true, got %+v", msgs[0])
	}

	want := "héllo wörld 日本語 ✓ → αβγ 🚀"
	if string(km.Runes) != want {
		t.Errorf("got runes %q, want %q", string(km.Runes), want)
	}
}

func TestInputParser_LargePaste(t *testing.T) {
	// 50,000 characters
	payload := strings.Repeat("A-123456789\n", 5000)
	raw := "\x1b[200~" + payload + "\x1b[201~"

	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, raw)
	})

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	km, ok := msgs[0].(tea.KeyMsg)
	if !ok || !km.Paste {
		t.Fatalf("expected KeyMsg with Paste=true, got %+v", msgs[0])
	}

	if len(km.Runes) != len(payload) {
		t.Errorf("got length %d, want %d", len(km.Runes), len(payload))
	}
}

func TestInputParser_EmptyPaste(t *testing.T) {
	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[200~\x1b[201~")
	})

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}

	km, ok := msgs[0].(tea.KeyMsg)
	if !ok || !km.Paste {
		t.Fatalf("expected KeyMsg with Paste=true, got %+v", msgs[0])
	}

	if len(km.Runes) != 0 {
		t.Errorf("expected empty runes, got %q", string(km.Runes))
	}
}

func TestInputParser_RepeatedPastes(t *testing.T) {
	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[200~first\x1b[201~")
		feedRunes(p, "\x1b[200~second\x1b[201~")
	})

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(msgs), msgs)
	}

	km1 := msgs[0].(tea.KeyMsg)
	km2 := msgs[1].(tea.KeyMsg)

	if !km1.Paste || string(km1.Runes) != "first" {
		t.Errorf("msg1 = %+v, want Paste: true 'first'", km1)
	}
	if !km2.Paste || string(km2.Runes) != "second" {
		t.Errorf("msg2 = %+v, want Paste: true 'second'", km2)
	}
}

func TestInputParser_EmbeddedEscapeInsidePayload(t *testing.T) {
	// A paste whose payload contains ESC sequences and partial candidate sequences
	// like \x1b[A, \x1b[20, and literal [201~ without ESC
	payload := "embedded \x1b[A arrow and \x1b[20 partial and [201~ literal"
	raw := "\x1b[200~" + payload + "\x1b[201~"

	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, raw)
	})

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}

	km := msgs[0].(tea.KeyMsg)
	if !km.Paste {
		t.Errorf("expected Paste=true, got false")
	}

	if string(km.Runes) != payload {
		t.Errorf("got runes %q, want %q", string(km.Runes), payload)
	}
}

func TestInputParser_ChunkedSplits(t *testing.T) {
	// Feed records in 2-rune chunks to prove marker split across chunks works
	raw := "\x1b[200~split marker across chunks\x1b[201~"
	runes := []rune(raw)

	msgs := collectParserMessages(func(p *inputParser) {
		for i := 0; i < len(runes); i += 2 {
			end := i + 2
			if end > len(runes) {
				end = len(runes)
			}
			for _, r := range runes[i:end] {
				p.feedKeyRecord(coninput.KeyEventRecord{
					KeyDown: true,
					Char:    r,
				})
			}
		}
	})

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}

	km := msgs[0].(tea.KeyMsg)
	if !km.Paste || string(km.Runes) != "split marker across chunks" {
		t.Errorf("got %+v", km)
	}
}

func TestInputParser_OrdinaryTyping(t *testing.T) {
	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, "hello world")
	})

	if len(msgs) != 11 {
		t.Fatalf("expected 11 messages, got %d", len(msgs))
	}

	for _, m := range msgs {
		km := m.(tea.KeyMsg)
		if km.Paste {
			t.Errorf("ordinary typing produced Paste=true: %+v", km)
		}
	}
}

func TestInputParser_PhysicalEscape(t *testing.T) {
	msgs := collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:         true,
			VirtualKeyCode:  coninput.VK_ESCAPE,
			ControlKeyState: 0,
		})
	})

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	km := msgs[0].(tea.KeyMsg)
	if km.Type != tea.KeyEscape {
		t.Errorf("got type %v, want KeyEscape", km.Type)
	}
}

func TestInputParser_SyntheticEscapeNonCandidate(t *testing.T) {
	// Synthetic escape followed by a normal rune 'x'
	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1bx")
	})

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (Alt+x), got %d: %+v", len(msgs), msgs)
	}

	km := msgs[0].(tea.KeyMsg)
	if !km.Alt || string(km.Runes) != "x" {
		t.Errorf("got %+v, want Alt+x", km)
	}
}

func TestInputParser_VTSequences(t *testing.T) {
	tests := []struct {
		seq      string
		wantType tea.KeyType
		wantAlt  bool
	}{
		{"\x1b[A", tea.KeyUp, false},
		{"\x1b[B", tea.KeyDown, false},
		{"\x1b[C", tea.KeyRight, false},
		{"\x1b[D", tea.KeyLeft, false},
		{"\x1b[H", tea.KeyHome, false},
		{"\x1b[F", tea.KeyEnd, false},
		{"\x1b[Z", tea.KeyShiftTab, false},
		{"\x1b[2~", tea.KeyInsert, false},
		{"\x1b[3~", tea.KeyDelete, false},
		{"\x1b[5~", tea.KeyPgUp, false},
		{"\x1b[6~", tea.KeyPgDown, false},
		{"\x1b[1;5A", tea.KeyCtrlUp, false},
		{"\x1b[1;2A", tea.KeyShiftUp, false},
		{"\x1b[1;3A", tea.KeyUp, true},
		{"\x1b\r", tea.KeyEnter, true},
		{"\x1bOP", tea.KeyF1, false},
		{"\x1b[24~", tea.KeyF12, false},
	}

	for _, tc := range tests {
		msgs := collectParserMessages(func(p *inputParser) {
			feedRunes(p, tc.seq)
		})

		if len(msgs) != 1 {
			t.Errorf("seq %q produced %d messages, want 1", tc.seq, len(msgs))
			continue
		}

		km := msgs[0].(tea.KeyMsg)
		if km.Type != tc.wantType || km.Alt != tc.wantAlt {
			t.Errorf("seq %q: got type=%v alt=%v, want type=%v alt=%v",
				tc.seq, km.Type, km.Alt, tc.wantType, tc.wantAlt)
		}
	}
}

func TestInputParser_ControlKeyChords(t *testing.T) {
	tests := []struct {
		char     rune
		wantType tea.KeyType
	}{
		{1, tea.KeyCtrlA},
		{3, tea.KeyCtrlC},
		{5, tea.KeyCtrlE},
		{18, tea.KeyCtrlR},
		{20, tea.KeyCtrlT},
		{25, tea.KeyCtrlY},
	}

	for _, tc := range tests {
		msgs := collectParserMessages(func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{
				KeyDown:         true,
				Char:            tc.char,
				ControlKeyState: coninput.LEFT_CTRL_PRESSED,
			})
		})

		if len(msgs) != 1 {
			t.Errorf("char %d produced %d messages, want 1", tc.char, len(msgs))
			continue
		}

		km := msgs[0].(tea.KeyMsg)
		if km.Type != tc.wantType {
			t.Errorf("char %d: got type=%v, want %v", tc.char, km.Type, tc.wantType)
		}
	}
}

func TestInputParser_PhysicalSpecialKeys(t *testing.T) {
	tests := []struct {
		vk       coninput.VirtualKeyCode
		shift    bool
		alt      bool
		wantType tea.KeyType
	}{
		{coninput.VK_RETURN, false, false, tea.KeyEnter},
		{coninput.VK_RETURN, false, true, tea.KeyEnter},
		{coninput.VK_RETURN, true, false, tea.KeyCtrlJ}, // Shift+Enter
		{coninput.VK_BACK, false, false, tea.KeyBackspace},
		{coninput.VK_TAB, false, false, tea.KeyTab},
		{coninput.VK_TAB, true, false, tea.KeyShiftTab},
		{coninput.VK_UP, false, false, tea.KeyUp},
		{coninput.VK_DOWN, false, false, tea.KeyDown},
		{coninput.VK_LEFT, false, false, tea.KeyLeft},
		{coninput.VK_RIGHT, false, false, tea.KeyRight},
		{coninput.VK_HOME, false, false, tea.KeyHome},
		{coninput.VK_END, false, false, tea.KeyEnd},
		{coninput.VK_DELETE, false, false, tea.KeyDelete},
		{coninput.VK_INSERT, false, false, tea.KeyInsert},
		{coninput.VK_F1, false, false, tea.KeyF1},
		{coninput.VK_F12, false, false, tea.KeyF12},
	}

	for _, tc := range tests {
		var ctrlState coninput.ControlKeyState
		if tc.shift {
			ctrlState |= coninput.SHIFT_PRESSED
		}
		if tc.alt {
			ctrlState |= coninput.LEFT_ALT_PRESSED
		}

		msgs := collectParserMessages(func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{
				KeyDown:         true,
				VirtualKeyCode:  tc.vk,
				ControlKeyState: ctrlState,
			})
		})

		if len(msgs) != 1 {
			t.Errorf("vk 0x%x (shift=%v alt=%v) produced %d messages, want 1",
				tc.vk, tc.shift, tc.alt, len(msgs))
			continue
		}

		km := msgs[0].(tea.KeyMsg)
		if km.Type != tc.wantType {
			t.Errorf("vk 0x%x: got type=%v, want %v", tc.vk, km.Type, tc.wantType)
		}
		if km.Alt != tc.alt {
			t.Errorf("vk 0x%x: got alt=%v, want %v", tc.vk, km.Alt, tc.alt)
		}
	}
}

func TestWindowsInputPump_MouseHandling(t *testing.T) {
	var msgs []tea.Msg
	pump := &windowsInputPump{}
	pump.sendMsg = func(m tea.Msg) {
		msgs = append(msgs, m)
	}

	// 1. Wheel Up
	pump.handleMouseRecord(coninput.MouseEventRecord{
		EventFlags:     coninput.MOUSE_WHEELED,
		WheelDirection: 1,
		MousePositon:   coninput.Coord{X: 10, Y: 5},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	mm1 := msgs[0].(tea.MouseMsg)
	if mm1.Action != tea.MouseActionPress || mm1.Button != tea.MouseButtonWheelUp || mm1.X != 10 || mm1.Y != 5 {
		t.Errorf("wheel up: got %+v", mm1)
	}

	// 2. Wheel Down
	msgs = nil
	pump.handleMouseRecord(coninput.MouseEventRecord{
		EventFlags:     coninput.MOUSE_WHEELED,
		WheelDirection: -1,
		MousePositon:   coninput.Coord{X: 12, Y: 8},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	mm2 := msgs[0].(tea.MouseMsg)
	if mm2.Action != tea.MouseActionPress || mm2.Button != tea.MouseButtonWheelDown || mm2.X != 12 || mm2.Y != 8 {
		t.Errorf("wheel down: got %+v", mm2)
	}

	// 3. Motion
	msgs = nil
	pump.handleMouseRecord(coninput.MouseEventRecord{
		EventFlags:   coninput.MOUSE_MOVED,
		MousePositon: coninput.Coord{X: 20, Y: 15},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	mm3 := msgs[0].(tea.MouseMsg)
	if mm3.Action != tea.MouseActionMotion || mm3.X != 20 || mm3.Y != 15 {
		t.Errorf("motion: got %+v", mm3)
	}

	// 4. Left button press
	msgs = nil
	pump.handleMouseRecord(coninput.MouseEventRecord{
		EventFlags:   coninput.CLICK,
		ButtonState:  coninput.FROM_LEFT_1ST_BUTTON_PRESSED,
		MousePositon: coninput.Coord{X: 30, Y: 20},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	mm4 := msgs[0].(tea.MouseMsg)
	if mm4.Action != tea.MouseActionPress || mm4.Button != tea.MouseButtonLeft {
		t.Errorf("left press: got %+v", mm4)
	}

	// 5. Left button release
	msgs = nil
	pump.handleMouseRecord(coninput.MouseEventRecord{
		EventFlags:   coninput.CLICK,
		ButtonState:  0,
		MousePositon: coninput.Coord{X: 30, Y: 20},
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	mm5 := msgs[0].(tea.MouseMsg)
	if mm5.Action != tea.MouseActionRelease || mm5.Button != tea.MouseButtonLeft {
		t.Errorf("left release: got %+v", mm5)
	}
}

func TestInputParser_SGRMouseClick(t *testing.T) {
	// Left press at x=25, y=15 (0-indexed: x=24, y=14)
	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[<0;25;15M")
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	mm, ok := msgs[0].(tea.MouseMsg)
	if !ok {
		t.Fatalf("expected tea.MouseMsg, got %T: %+v", msgs[0], msgs[0])
	}
	if mm.Action != tea.MouseActionPress || mm.Button != tea.MouseButtonLeft || mm.X != 24 || mm.Y != 14 {
		t.Errorf("left press mismatch: got %+v", mm)
	}

	// Left release at x=25, y=15
	msgs = collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[<0;25;15m")
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	mm = msgs[0].(tea.MouseMsg)
	if mm.Action != tea.MouseActionRelease || mm.Button != tea.MouseButtonLeft || mm.X != 24 || mm.Y != 14 {
		t.Errorf("left release mismatch: got %+v", mm)
	}

	// Right press at x=10, y=5 (0-indexed: x=9, y=4)
	msgs = collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[<2;10;5M")
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	mm = msgs[0].(tea.MouseMsg)
	if mm.Action != tea.MouseActionPress || mm.Button != tea.MouseButtonRight || mm.X != 9 || mm.Y != 4 {
		t.Errorf("right press mismatch: got %+v", mm)
	}
}

func TestInputParser_SGRMouseMotion(t *testing.T) {
	// Motion event without button: button 3 + motion bit 32 = 35; x=30, y=20 (0-indexed: x=29, y=19)
	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[<35;30;20M")
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	mm, ok := msgs[0].(tea.MouseMsg)
	if !ok {
		t.Fatalf("expected tea.MouseMsg, got %T: %+v", msgs[0], msgs[0])
	}
	if mm.Action != tea.MouseActionMotion || mm.X != 29 || mm.Y != 19 {
		t.Errorf("motion mismatch: got %+v", mm)
	}
}

func TestInputParser_SGRMouseWheel(t *testing.T) {
	// Wheel up: 64; x=10, y=10
	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[<64;10;10M")
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	mm := msgs[0].(tea.MouseMsg)
	if mm.Action != tea.MouseActionPress || mm.Button != tea.MouseButtonWheelUp || mm.X != 9 || mm.Y != 9 {
		t.Errorf("wheel up mismatch: got %+v", mm)
	}

	// Wheel down: 65; x=10, y=10
	msgs = collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[<65;10;10M")
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	mm = msgs[0].(tea.MouseMsg)
	if mm.Action != tea.MouseActionPress || mm.Button != tea.MouseButtonWheelDown || mm.X != 9 || mm.Y != 9 {
		t.Errorf("wheel down mismatch: got %+v", mm)
	}
}

func TestInputParser_FocusReporting(t *testing.T) {
	// Focus in: \x1b[I
	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[I")
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	if _, ok := msgs[0].(tea.FocusMsg); !ok {
		t.Errorf("expected tea.FocusMsg, got %T: %+v", msgs[0], msgs[0])
	}

	// Focus out: \x1b[O
	msgs = collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[O")
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	if _, ok := msgs[0].(tea.BlurMsg); !ok {
		t.Errorf("expected tea.BlurMsg, got %T: %+v", msgs[0], msgs[0])
	}
}

func TestInputParser_MouseNeverEmitsKeyMsg(t *testing.T) {
	// Prove that mouse sequences never leak KeyMsg keystrokes like [<0;67;24M into the prompt
	raw := "\x1b[<0;67;24M\x1b[<0;67;24m"
	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, raw)
	})

	if len(msgs) != 2 {
		t.Fatalf("expected 2 mouse messages, got %d: %+v", len(msgs), msgs)
	}
	for i, m := range msgs {
		if km, isKey := m.(tea.KeyMsg); isKey {
			t.Fatalf("message %d leaked as KeyMsg: %+v (runes: %q)", i, km, string(km.Runes))
		}
		if _, isMouse := m.(tea.MouseMsg); !isMouse {
			t.Fatalf("message %d expected MouseMsg, got %T", i, m)
		}
	}
}

// TestConPTY_EndToEnd proves that under real ConPTY, bracketed paste produces
// a single KeyRunes message with Paste: true and correct Unicode payload.
func TestConPTY_EndToEnd(t *testing.T) {
	origCP, _ := windows.GetConsoleCP()
	_ = windows.SetConsoleCP(65001)
	defer func() {
		if origCP != 0 {
			_ = windows.SetConsoleCP(origCP)
		}
	}()

	// Create PseudoConsole pipes
	var sa windows.SecurityAttributes
	sa.Length = uint32(unsafe.Sizeof(sa))
	sa.InheritHandle = 1

	var inRead, inWrite, outRead, outWrite windows.Handle
	if err := windows.CreatePipe(&inRead, &inWrite, &sa, 0); err != nil {
		t.Fatalf("CreatePipe in: %v", err)
	}
	defer windows.CloseHandle(inWrite)

	if err := windows.CreatePipe(&outRead, &outWrite, &sa, 0); err != nil {
		windows.CloseHandle(inRead)
		t.Fatalf("CreatePipe out: %v", err)
	}
	defer windows.CloseHandle(outRead)

	var pc windows.Handle
	err := windows.CreatePseudoConsole(windows.Coord{X: 80, Y: 24}, inRead, outWrite, 0, &pc)
	windows.CloseHandle(inRead)
	windows.CloseHandle(outWrite)
	if err != nil {
		t.Skipf("CreatePseudoConsole not supported: %v", err)
		return
	}
	defer windows.ClosePseudoConsole(pc)

	// Set up StartupInfoEx with PseudoConsole attribute
	const procThreadAttributePseudoConsole = 0x00020016
	al, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatalf("NewProcThreadAttributeList: %v", err)
	}
	defer al.Delete()

	hpcon := *(*unsafe.Pointer)(unsafe.Pointer(&pc))
	if err := al.Update(procThreadAttributePseudoConsole, hpcon, unsafe.Sizeof(pc)); err != nil {
		t.Fatalf("Update attribute list: %v", err)
	}

	si := windows.StartupInfoEx{}
	si.StartupInfo.Cb = uint32(unsafe.Sizeof(si))
	si.StartupInfo.Flags = windows.STARTF_USESHOWWINDOW
	si.ProcThreadAttributeList = al.List()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	// Command to run this test binary with helper flag
	cmdline := fmt.Sprintf("\"%s\" -test.run=TestWinInputConPTY_ChildProcess", exe)
	cmdPtr, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}

	os.Setenv("FORCEFIELD_CONPTY_CHILD", "1")
	defer os.Unsetenv("FORCEFIELD_CONPTY_CHILD")

	pi := &windows.ProcessInformation{}
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT)
	err = windows.CreateProcess(nil, cmdPtr, nil, nil, false, flags, nil, nil, &si.StartupInfo, pi)
	if err != nil {
		t.Fatalf("CreateProcess: %v", err)
	}
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)

	// Consume child stdout
	var outBuf bytes.Buffer
	var outMu sync.Mutex
	go func() {
		buf := make([]byte, 1024)
		for {
			var n uint32
			err := windows.ReadFile(outRead, buf, &n, nil)
			if err != nil || n == 0 {
				return
			}
			outMu.Lock()
			outBuf.Write(buf[:n])
			outMu.Unlock()
		}
	}()

	// Give child process time to initialize CONIN$
	time.Sleep(400 * time.Millisecond)

	var written uint32

	// 1. Send ordinary typing
	_ = windows.WriteFile(inWrite, []byte("typed\r"), &written, nil)
	time.Sleep(100 * time.Millisecond)

	// 2. Send Backspace (\x7f) and Ctrl+C (\x03)
	_ = windows.WriteFile(inWrite, []byte("\x7f\x03"), &written, nil)
	time.Sleep(100 * time.Millisecond)

	// 3. Send bracketed paste with Unicode (Latin, CJK, Greek) and CRLF
	pastePayload := "\x1b[200~héllo 日本語 αβγ\r\nline2\x1b[201~"
	_ = windows.WriteFile(inWrite, []byte(pastePayload), &written, nil)
	time.Sleep(100 * time.Millisecond)

	// 4. Resize ConPTY
	_ = windows.ResizePseudoConsole(pc, windows.Coord{X: 120, Y: 35})
	time.Sleep(100 * time.Millisecond)

	// 5. Send SGR mouse click
	mousePayload := "\x1b[<0;25;15M"
	_ = windows.WriteFile(inWrite, []byte(mousePayload), &written, nil)

	// Wait for child to exit
	event, _ := windows.WaitForSingleObject(pi.Process, 6000)
	if event != windows.WAIT_OBJECT_0 {
		_ = windows.TerminateProcess(pi.Process, 1)
		t.Fatalf("child process timed out waiting for input")
	}

	var exitCode uint32
	_ = windows.GetExitCodeProcess(pi.Process, &exitCode)
	if exitCode != 0 {
		outMu.Lock()
		output := outBuf.String()
		outMu.Unlock()
		t.Fatalf("child process exited with %d; output: %s", exitCode, output)
	}
}

// TestWinInputConPTY_ChildProcess is executed inside ConPTY by TestConPTY_EndToEnd.
func TestWinInputConPTY_ChildProcess(t *testing.T) {
	if os.Getenv("FORCEFIELD_CONPTY_CHILD") != "1" {
		return
	}

	// Check if running as child process under ConPTY
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
		return // Not running in a console
	}
	var origMode uint32
	if err := windows.GetConsoleMode(conin, &origMode); err != nil {
		windows.CloseHandle(conin)
		return
	}
	windows.CloseHandle(conin)

	// We are inside ConPTY!
	var program *tea.Program
	opts := []tea.ProgramOption{}

	cleanup, err := setupPlatformInput(&opts, &program)
	if err != nil {
		fmt.Printf("setup error: %v\n", err)
		os.Exit(1)
	}

	model := &testComprehensiveConPTYModel{
		events: make(chan string, 10),
	}
	program = tea.NewProgram(model, opts...)

	go func() {
		_, _ = program.Run()
	}()

	var receivedTyped, receivedBackspace, receivedCtrlC, receivedPaste, receivedResize, receivedMouse bool
	timeout := time.After(5 * time.Second)

loop:
	for {
		select {
		case ev := <-model.events:
			if ev == "typed:typed" {
				receivedTyped = true
			}
			if ev == "backspace" {
				receivedBackspace = true
			}
			if ev == "ctrlc" {
				receivedCtrlC = true
			}
			if ev == "paste:héllo 日本語 αβγ\r\nline2" {
				receivedPaste = true
			}
			if strings.HasPrefix(ev, "resize:120x35") {
				receivedResize = true
			}
			if strings.HasPrefix(ev, "mouse:") {
				receivedMouse = true
			}
			if receivedTyped && receivedBackspace && receivedCtrlC && receivedPaste && receivedResize && receivedMouse {
				break loop
			}
		case <-timeout:
			fmt.Printf("timed out waiting for events: typed=%v backspace=%v ctrlc=%v paste=%v resize=%v mouse=%v\n",
				receivedTyped, receivedBackspace, receivedCtrlC, receivedPaste, receivedResize, receivedMouse)
			os.Exit(2)
		}
	}

	program.Quit()
	cleanup()

	// Verify mode restoration
	conin2, err := windows.CreateFile(
		windows.StringToUTF16Ptr("CONIN$"),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err == nil {
		var restoredMode uint32
		if err := windows.GetConsoleMode(conin2, &restoredMode); err == nil {
			if restoredMode != origMode {
				fmt.Printf("mode mismatch: restored=0x%08x orig=0x%08x\n", restoredMode, origMode)
				os.Exit(3)
			}
		}
		windows.CloseHandle(conin2)
	}

	os.Exit(0)
}

type testComprehensiveConPTYModel struct {
	events chan string
	typed  strings.Builder
}

func (m *testComprehensiveConPTYModel) Init() tea.Cmd {
	return nil
}

func (m *testComprehensiveConPTYModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Paste {
			m.events <- "paste:" + string(msg.Runes)
		} else if msg.Type == tea.KeyRunes {
			m.typed.WriteString(string(msg.Runes))
		} else if msg.Type == tea.KeyEnter {
			m.events <- "typed:" + m.typed.String()
		} else if msg.Type == tea.KeyBackspace {
			m.events <- "backspace"
		} else if msg.Type == tea.KeyCtrlC {
			m.events <- "ctrlc"
		}
	case tea.WindowSizeMsg:
		m.events <- fmt.Sprintf("resize:%dx%d", msg.Width, msg.Height)
	case tea.MouseMsg:
		m.events <- fmt.Sprintf("mouse:action=%v,btn=%v,x=%d,y=%d", msg.Action, msg.Button, msg.X, msg.Y)
	}
	return m, nil
}

func (m *testComprehensiveConPTYModel) View() string {
	return ""
}

func keyMsgEqual(m tea.Msg, want tea.KeyMsg) bool {
	km, ok := m.(tea.KeyMsg)
	if !ok {
		return false
	}
	return km.Type == want.Type && km.Alt == want.Alt && km.Paste == want.Paste && string(km.Runes) == string(want.Runes)
}

func TestInputParser_BackspaceVariants(t *testing.T) {
	// 1. ConHost VK_BACK
	msgs := collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:        true,
			VirtualKeyCode: coninput.VK_BACK,
			Char:           0x08,
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyBackspace}) {
		t.Errorf("ConHost VK_BACK: got %+v, want KeyBackspace", msgs)
	}

	// 2. ConPTY DEL (0x7f)
	msgs = collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown: true,
			Char:    0x7f,
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyBackspace}) {
		t.Errorf("ConPTY DEL (0x7f): got %+v, want KeyBackspace", msgs)
	}

	// 3. ConPTY BS (0x08 without ctrl)
	msgs = collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown: true,
			Char:    0x08,
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyBackspace}) {
		t.Errorf("ConPTY BS (0x08): got %+v, want KeyBackspace", msgs)
	}

	// 4. Alt+Backspace (VK_BACK with alt)
	msgs = collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:         true,
			VirtualKeyCode:  coninput.VK_BACK,
			ControlKeyState: coninput.LEFT_ALT_PRESSED,
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyBackspace, Alt: true}) {
		t.Errorf("Alt+Backspace VK: got %+v, want KeyBackspace Alt=true", msgs)
	}

	// 5. Alt+Backspace VT sequence (\x1b\x7f)
	msgs = collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b\x7f")
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyBackspace, Alt: true}) {
		t.Errorf("Alt+Backspace VT: got %+v, want KeyBackspace Alt=true", msgs)
	}
}

func TestInputParser_EscapeStandalone(t *testing.T) {
	// 1. ConHost VK_ESCAPE
	msgs := collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:        true,
			VirtualKeyCode: coninput.VK_ESCAPE,
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyEscape}) {
		t.Errorf("ConHost VK_ESCAPE: got %+v, want KeyEscape", msgs)
	}

	// 2. ConPTY ESC byte (\x1b) resolving after timer
	var received []tea.Msg
	var mu sync.Mutex
	p := newInputParser()
	p.emit = func(m tea.Msg) {
		mu.Lock()
		received = append(received, m)
		mu.Unlock()
	}
	p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, Char: 0x1b})
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	count := len(received)
	var first tea.Msg
	if count > 0 {
		first = received[0]
	}
	mu.Unlock()

	if count != 1 || !keyMsgEqual(first, tea.KeyMsg{Type: tea.KeyEscape}) {
		t.Errorf("ConPTY standalone ESC: got %+v, want KeyEscape", received)
	}
}

func TestInputParser_EscapeFollowedByTyping(t *testing.T) {
	// 1. ESC then typing after timer expired
	var received []tea.Msg
	var mu sync.Mutex
	p := newInputParser()
	p.emit = func(m tea.Msg) {
		mu.Lock()
		received = append(received, m)
		mu.Unlock()
	}
	p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, Char: 0x1b})
	time.Sleep(50 * time.Millisecond) // wait for escape timer to resolve
	p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, Char: 'a'})

	mu.Lock()
	msgs := append([]tea.Msg(nil), received...)
	mu.Unlock()

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyEscape}) {
		t.Errorf("msg[0]: got %+v, want KeyEscape", msgs[0])
	}
	if km, ok := msgs[1].(tea.KeyMsg); !ok || km.Type != tea.KeyRunes || string(km.Runes) != "a" || km.Alt {
		t.Errorf("msg[1]: got %+v, want KeyRunes 'a' without Alt", msgs[1])
	}

	// 2. ESC immediately followed by 'a' without waiting (Alt+a)
	msgs = collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1ba")
	})
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message for Alt+a, got %d: %+v", len(msgs), msgs)
	}
	if km, ok := msgs[0].(tea.KeyMsg); !ok || km.Type != tea.KeyRunes || string(km.Runes) != "a" || !km.Alt {
		t.Errorf("Alt+a: got %+v, want KeyRunes 'a' with Alt=true", msgs[0])
	}
}

func TestInputParser_AltKeys(t *testing.T) {
	// Alt+Enter (\x1b\r)
	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b\r")
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyEnter, Alt: true}) {
		t.Errorf("Alt+Enter: got %+v, want KeyEnter Alt=true", msgs)
	}

	// Alt+Newline (\x1b\n)
	msgs = collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b\n")
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyCtrlJ, Alt: true}) {
		t.Errorf("Alt+Newline: got %+v, want KeyCtrlJ Alt=true", msgs)
	}

	// Alt+Tab (\x1b\t)
	msgs = collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b\t")
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyTab, Alt: true}) {
		t.Errorf("Alt+Tab: got %+v, want KeyTab Alt=true", msgs)
	}

	// Alt+Escape (\x1b\x1b)
	msgs = collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b\x1b")
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyEscape, Alt: true}) {
		t.Errorf("Alt+Escape: got %+v, want KeyEscape Alt=true", msgs)
	}
}

func TestInputParser_CtrlCInAllStates(t *testing.T) {
	// 1. Normal state via rune 3 (ConPTY)
	msgs := collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, Char: 3})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyCtrlC}) {
		t.Errorf("Ctrl+C in normal state (Char=3): got %+v", msgs)
	}

	// 2. Normal state via VK='C' + ctrl (ConHost)
	msgs = collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:         true,
			VirtualKeyCode:  'C',
			ControlKeyState: coninput.LEFT_CTRL_PRESSED,
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyCtrlC}) {
		t.Errorf("Ctrl+C in normal state (VK='C'+ctrl): got %+v", msgs)
	}

	// 3. State candidate start: \x1b[ buffered, then Ctrl+C
	msgs = collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[")
		p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, Char: 3})
		// Verify parser recovered to stateNormal by typing 'x'
		p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, Char: 'x'})
	})
	if len(msgs) != 2 {
		t.Fatalf("Ctrl+C in candidate start: expected 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyCtrlC}) {
		t.Errorf("Ctrl+C in candidate start: got msg[0]=%+v, want KeyCtrlC", msgs[0])
	}
	if km, ok := msgs[1].(tea.KeyMsg); !ok || string(km.Runes) != "x" {
		t.Errorf("subsequent char after Ctrl+C: got %+v, want 'x'", msgs[1])
	}

	// 4. State in paste: \x1b[200~hello buffered, then Ctrl+C
	msgs = collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[200~hello")
		p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, Char: 3})
		// Verify parser recovered to stateNormal
		p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, Char: 'y'})
	})
	if len(msgs) != 2 {
		t.Fatalf("Ctrl+C in paste: expected 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyCtrlC}) {
		t.Errorf("Ctrl+C in paste: got msg[0]=%+v, want KeyCtrlC", msgs[0])
	}
	if km, ok := msgs[1].(tea.KeyMsg); !ok || string(km.Runes) != "y" {
		t.Errorf("subsequent char after Ctrl+C in paste: got %+v, want 'y'", msgs[1])
	}
}

func TestInputParser_CtrlAtoZ(t *testing.T) {
	// Test all 26 control characters 1..26
	expectedTypes := map[rune]tea.KeyType{
		1:  tea.KeyCtrlA,
		2:  tea.KeyCtrlB,
		3:  tea.KeyCtrlC,
		4:  tea.KeyCtrlD,
		5:  tea.KeyCtrlE,
		6:  tea.KeyCtrlF,
		7:  tea.KeyCtrlG,
		8:  tea.KeyBackspace, // 0x08 without ctrl is Backspace in emitKey
		9:  tea.KeyTab,       // 0x09 is Tab
		10: tea.KeyCtrlJ,     // 0x0a is newline/Line Feed
		11: tea.KeyCtrlK,
		12: tea.KeyCtrlL,
		13: tea.KeyEnter,     // 0x0d is Enter
		14: tea.KeyCtrlN,
		15: tea.KeyCtrlO,
		16: tea.KeyCtrlP,
		17: tea.KeyCtrlQ,
		18: tea.KeyCtrlR,
		19: tea.KeyCtrlS,
		20: tea.KeyCtrlT,
		21: tea.KeyCtrlU,
		22: tea.KeyCtrlV,
		23: tea.KeyCtrlW,
		24: tea.KeyCtrlX,
		25: tea.KeyCtrlY,
		26: tea.KeyCtrlZ,
	}

	for r := rune(1); r <= 26; r++ {
		expected := expectedTypes[r]
		msgs := collectParserMessages(func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{
				KeyDown: true,
				Char:    r,
			})
		})
		if len(msgs) != 1 {
			t.Errorf("rune %d: expected 1 message, got %d: %+v", r, len(msgs), msgs)
			continue
		}
		km, ok := msgs[0].(tea.KeyMsg)
		if !ok || km.Type != expected {
			t.Errorf("rune %d: got %+v, want type %v", r, msgs[0], expected)
		}
	}

	// Test ConHost chords: VK_A through VK_Z with LEFT_CTRL_PRESSED
	for vk := 'A'; vk <= 'Z'; vk++ {
		offset := tea.KeyType(vk - 'A')
		expected := tea.KeyCtrlA + offset
		msgs := collectParserMessages(func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{
				KeyDown:         true,
				VirtualKeyCode:  coninput.VirtualKeyCode(vk),
				ControlKeyState: coninput.LEFT_CTRL_PRESSED,
			})
		})
		if len(msgs) != 1 {
			t.Errorf("VK %c: expected 1 message, got %d: %+v", vk, len(msgs), msgs)
			continue
		}
		km, ok := msgs[0].(tea.KeyMsg)
		if !ok || km.Type != expected {
			t.Errorf("VK %c: got %+v, want type %v", vk, msgs[0], expected)
		}
	}

	// Test Ctrl+Space -> KeyCtrlAt
	msgs := collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:         true,
			VirtualKeyCode:  coninput.VK_SPACE,
			ControlKeyState: coninput.LEFT_CTRL_PRESSED,
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyCtrlAt}) {
		t.Errorf("Ctrl+Space: got %+v, want KeyCtrlAt", msgs)
	}
}

func TestInputParser_EnterAndLinebreak(t *testing.T) {
	// ConHost Enter
	msgs := collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:        true,
			VirtualKeyCode: coninput.VK_RETURN,
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyEnter}) {
		t.Errorf("ConHost VK_RETURN: got %+v, want KeyEnter", msgs)
	}

	// ConHost Shift+Enter -> KeyCtrlJ
	msgs = collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:         true,
			VirtualKeyCode:  coninput.VK_RETURN,
			ControlKeyState: coninput.SHIFT_PRESSED,
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyCtrlJ}) {
		t.Errorf("ConHost Shift+Return: got %+v, want KeyCtrlJ", msgs)
	}

	// ConPTY \r -> KeyEnter
	msgs = collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown: true,
			Char:    '\r',
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyEnter}) {
		t.Errorf("ConPTY \\r: got %+v, want KeyEnter", msgs)
	}

	// ConPTY \n -> KeyCtrlJ
	msgs = collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown: true,
			Char:    '\n',
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyCtrlJ}) {
		t.Errorf("ConPTY \\n: got %+v, want KeyCtrlJ", msgs)
	}
}

func TestInputParser_TabAndShiftTab(t *testing.T) {
	// ConHost Tab
	msgs := collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:        true,
			VirtualKeyCode: coninput.VK_TAB,
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyTab}) {
		t.Errorf("ConHost VK_TAB: got %+v, want KeyTab", msgs)
	}

	// ConHost Shift+Tab
	msgs = collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:         true,
			VirtualKeyCode:  coninput.VK_TAB,
			ControlKeyState: coninput.SHIFT_PRESSED,
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyShiftTab}) {
		t.Errorf("ConHost Shift+Tab: got %+v, want KeyShiftTab", msgs)
	}

	// ConPTY \t -> KeyTab
	msgs = collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown: true,
			Char:    '\t',
		})
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyTab}) {
		t.Errorf("ConPTY \\t: got %+v, want KeyTab", msgs)
	}

	// ConPTY Shift+Tab VT sequence: \x1b[Z
	msgs = collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[Z")
	})
	if len(msgs) != 1 || !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyShiftTab}) {
		t.Errorf("ConPTY \\x1b[Z: got %+v, want KeyShiftTab", msgs)
	}
}

func TestInputParser_NavigationKeys(t *testing.T) {
	cases := []struct {
		name     string
		feed     func(p *inputParser)
		expected tea.KeyType
	}{
		{"VK_UP", func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, VirtualKeyCode: coninput.VK_UP})
		}, tea.KeyUp},
		{"VK_DOWN", func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, VirtualKeyCode: coninput.VK_DOWN})
		}, tea.KeyDown},
		{"VK_LEFT", func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, VirtualKeyCode: coninput.VK_LEFT})
		}, tea.KeyLeft},
		{"VK_RIGHT", func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, VirtualKeyCode: coninput.VK_RIGHT})
		}, tea.KeyRight},
		{"VK_HOME", func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, VirtualKeyCode: coninput.VK_HOME})
		}, tea.KeyHome},
		{"VK_END", func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, VirtualKeyCode: coninput.VK_END})
		}, tea.KeyEnd},
		{"VK_PRIOR (PgUp)", func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, VirtualKeyCode: coninput.VK_PRIOR})
		}, tea.KeyPgUp},
		{"VK_NEXT (PgDown)", func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, VirtualKeyCode: coninput.VK_NEXT})
		}, tea.KeyPgDown},
		{"VK_DELETE", func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, VirtualKeyCode: coninput.VK_DELETE})
		}, tea.KeyDelete},
		{"VK_INSERT", func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{KeyDown: true, VirtualKeyCode: coninput.VK_INSERT})
		}, tea.KeyInsert},

		{"VT Up", func(p *inputParser) { feedRunes(p, "\x1b[A") }, tea.KeyUp},
		{"VT Down", func(p *inputParser) { feedRunes(p, "\x1b[B") }, tea.KeyDown},
		{"VT Right", func(p *inputParser) { feedRunes(p, "\x1b[C") }, tea.KeyRight},
		{"VT Left", func(p *inputParser) { feedRunes(p, "\x1b[D") }, tea.KeyLeft},
		{"VT Home", func(p *inputParser) { feedRunes(p, "\x1b[H") }, tea.KeyHome},
		{"VT End", func(p *inputParser) { feedRunes(p, "\x1b[F") }, tea.KeyEnd},
		{"VT PgUp", func(p *inputParser) { feedRunes(p, "\x1b[5~") }, tea.KeyPgUp},
		{"VT PgDown", func(p *inputParser) { feedRunes(p, "\x1b[6~") }, tea.KeyPgDown},
		{"VT Delete", func(p *inputParser) { feedRunes(p, "\x1b[3~") }, tea.KeyDelete},

		{"VT Ctrl+Up", func(p *inputParser) { feedRunes(p, "\x1b[1;5A") }, tea.KeyCtrlUp},
		{"VT Ctrl+Down", func(p *inputParser) { feedRunes(p, "\x1b[1;5B") }, tea.KeyCtrlDown},
		{"VT Ctrl+Right", func(p *inputParser) { feedRunes(p, "\x1b[1;5C") }, tea.KeyCtrlRight},
		{"VT Ctrl+Left", func(p *inputParser) { feedRunes(p, "\x1b[1;5D") }, tea.KeyCtrlLeft},
	}

	for _, tc := range cases {
		msgs := collectParserMessages(tc.feed)
		if len(msgs) != 1 {
			t.Errorf("%s: expected 1 message, got %d: %+v", tc.name, len(msgs), msgs)
			continue
		}
		km, ok := msgs[0].(tea.KeyMsg)
		if !ok || km.Type != tc.expected {
			t.Errorf("%s: got %+v, want type %v", tc.name, msgs[0], tc.expected)
		}
	}
}

func TestInputParser_FunctionKeys(t *testing.T) {
	// VK_F1 .. VK_F12
	fkeys := []struct {
		vk       coninput.VirtualKeyCode
		expected tea.KeyType
	}{
		{coninput.VK_F1, tea.KeyF1},
		{coninput.VK_F2, tea.KeyF2},
		{coninput.VK_F3, tea.KeyF3},
		{coninput.VK_F4, tea.KeyF4},
		{coninput.VK_F5, tea.KeyF5},
		{coninput.VK_F6, tea.KeyF6},
		{coninput.VK_F7, tea.KeyF7},
		{coninput.VK_F8, tea.KeyF8},
		{coninput.VK_F9, tea.KeyF9},
		{coninput.VK_F10, tea.KeyF10},
		{coninput.VK_F11, tea.KeyF11},
		{coninput.VK_F12, tea.KeyF12},
	}

	for _, fk := range fkeys {
		msgs := collectParserMessages(func(p *inputParser) {
			p.feedKeyRecord(coninput.KeyEventRecord{
				KeyDown:        true,
				VirtualKeyCode: fk.vk,
			})
		})
		if len(msgs) != 1 {
			t.Errorf("VK %v: expected 1 message, got %d: %+v", fk.vk, len(msgs), msgs)
			continue
		}
		km, ok := msgs[0].(tea.KeyMsg)
		if !ok || km.Type != fk.expected {
			t.Errorf("VK %v: got %+v, want type %v", fk.vk, msgs[0], fk.expected)
		}
	}

	// VT F1-F4: \x1bOP - \x1bOS
	vtFkeys := []struct {
		seq      string
		expected tea.KeyType
	}{
		{"\x1bOP", tea.KeyF1},
		{"\x1bOQ", tea.KeyF2},
		{"\x1bOR", tea.KeyF3},
		{"\x1bOS", tea.KeyF4},
		{"\x1b[15~", tea.KeyF5},
		{"\x1b[17~", tea.KeyF6},
		{"\x1b[24~", tea.KeyF12},
	}
	for _, fk := range vtFkeys {
		msgs := collectParserMessages(func(p *inputParser) {
			feedRunes(p, fk.seq)
		})
		if len(msgs) != 1 {
			t.Errorf("seq %q: expected 1 message, got %d: %+v", fk.seq, len(msgs), msgs)
			continue
		}
		km, ok := msgs[0].(tea.KeyMsg)
		if !ok || km.Type != fk.expected {
			t.Errorf("seq %q: got %+v, want type %v", fk.seq, msgs[0], fk.expected)
		}
	}
}

func TestInputParser_BackspaceInIncompleteVTSequence(t *testing.T) {
	// Feed \x1b[ (incomplete sequence), then Backspace (0x7f)
	msgs := collectParserMessages(func(p *inputParser) {
		feedRunes(p, "\x1b[")
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown: true,
			Char:    0x7f,
		})
	})

	// Expected: Escape flushed, '[' flushed, then Backspace emitted
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(msgs), msgs)
	}
	if !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyEscape}) {
		t.Errorf("msg[0]: got %+v, want KeyEscape", msgs[0])
	}
	if km, ok := msgs[1].(tea.KeyMsg); !ok || string(km.Runes) != "[" {
		t.Errorf("msg[1]: got %+v, want '['", msgs[1])
	}
	if !keyMsgEqual(msgs[2], tea.KeyMsg{Type: tea.KeyBackspace}) {
		t.Errorf("msg[2]: got %+v, want KeyBackspace", msgs[2])
	}
}

func TestInputParser_IncompleteVTSequenceTimeout(t *testing.T) {
	var received []tea.Msg
	var mu sync.Mutex
	p := newInputParser()
	p.emit = func(m tea.Msg) {
		mu.Lock()
		received = append(received, m)
		mu.Unlock()
	}

	// Feed \x1b[ (incomplete)
	feedRunes(p, "\x1b[")
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	msgs := append([]tea.Msg(nil), received...)
	mu.Unlock()

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages on timeout, got %d: %+v", len(msgs), msgs)
	}
	if !keyMsgEqual(msgs[0], tea.KeyMsg{Type: tea.KeyEscape}) {
		t.Errorf("msg[0]: got %+v, want KeyEscape", msgs[0])
	}
	if km, ok := msgs[1].(tea.KeyMsg); !ok || string(km.Runes) != "[" {
		t.Errorf("msg[1]: got %+v, want '['", msgs[1])
	}
}

func TestInputParser_RepeatCount(t *testing.T) {
	msgs := collectParserMessages(func(p *inputParser) {
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:     true,
			Char:        'z',
			RepeatCount: 3,
		})
	})
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(msgs), msgs)
	}
	for i, m := range msgs {
		km, ok := m.(tea.KeyMsg)
		if !ok || string(km.Runes) != "z" {
			t.Errorf("msg[%d]: got %+v, want 'z'", i, m)
		}
	}
}

func TestInputParser_KeyUpAndModifiersIgnored(t *testing.T) {
	msgs := collectParserMessages(func(p *inputParser) {
		// Key up should be ignored
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown: false,
			Char:    'a',
		})
		// Modifier keys alone should be ignored
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:        true,
			VirtualKeyCode: coninput.VK_SHIFT,
		})
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:        true,
			VirtualKeyCode: 0x11, // VK_CONTROL
		})
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:        true,
			VirtualKeyCode: 0x12, // VK_MENU
		})
		p.feedKeyRecord(coninput.KeyEventRecord{
			KeyDown:        true,
			VirtualKeyCode: 0x14, // VK_CAPITAL
		})
	})
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages, got %d: %+v", len(msgs), msgs)
	}
}
