//go:build windows

package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	xwindows "github.com/charmbracelet/x/windows"
	"golang.org/x/sys/windows"
)

// Keep console mouse records while allowing Windows to deliver bracketed paste.
// VT characters pass through as one stream regardless of their virtual keys.
// Native non-text keys retain modifiers. Ctrl+O is the portable newline key.
func consoleInput(ctx context.Context, input, output *os.File) ([]tea.ProgramOption, func(), error) {
	handle := windows.Handle(input.Fd())
	var original uint32
	if err := windows.GetConsoleMode(handle, &original); err != nil {
		// Pipes and remote terminals continue through Bubble Tea's normal reader.
		return nil, func() {}, nil
	}
	mode := nativeConsoleMode(original)
	if err := windows.SetConsoleMode(handle, mode); err != nil {
		return nil, nil, fmt.Errorf("configure native console input: %w", err)
	}
	readCtx, cancel := context.WithCancel(ctx)
	r := &consoleReader{ctx: readCtx, handle: handle, output: windows.Handle(output.Fd())}
	cleanup := func() {
		cancel()
		_ = windows.SetConsoleMode(handle, original)
	}
	return []tea.ProgramOption{
		// Expose only io.Reader so Bubble Tea cannot bypass the native encoder.
		tea.WithInput(r),
		tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, quitting := msg.(tea.QuitMsg); quitting {
				cancel()
			}
			return msg
		}),
	}, cleanup, nil
}

func nativeConsoleMode(original uint32) uint32 {
	mode := original &^ (windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT |
		windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_QUICK_EDIT_MODE)
	// Without VT_INPUT Windows strips paste delimiters before ReadConsoleInput,
	// turning pasted newlines into indistinguishable submit keys. Hosts may
	// encode Shift+Enter as plain Enter in VT mode; Ctrl+O is unambiguous.
	return mode | windows.ENABLE_VIRTUAL_TERMINAL_INPUT | windows.ENABLE_EXTENDED_FLAGS |
		windows.ENABLE_WINDOW_INPUT | windows.ENABLE_MOUSE_INPUT
}

type consoleReader struct {
	ctx           context.Context
	handle        windows.Handle
	output        windows.Handle
	buffer        bytes.Buffer
	mouseState    uint32
	highSurrogate uint16
	width, height int
}

func (r *consoleReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for r.buffer.Len() == 0 {
		select {
		case <-r.ctx.Done():
			return 0, io.EOF
		default:
		}
		var records [64]xwindows.InputRecord
		var count uint32
		if err := xwindows.PeekConsoleInput(r.handle, &records[0], uint32(len(records)), &count); err != nil {
			return 0, err
		}
		if count == 0 {
			select {
			case <-r.ctx.Done():
				return 0, io.EOF
			case <-poll.C:
			}
			continue
		}
		if err := xwindows.ReadConsoleInput(r.handle, &records[0], count, &count); err != nil {
			return 0, err
		}
		for _, record := range records[:count] {
			r.encode(record)
		}
	}
	return r.buffer.Read(p)
}

func (r *consoleReader) encode(record xwindows.InputRecord) {
	switch record.EventType {
	case xwindows.FOCUS_EVENT:
		// Preserve native focus reports as VT events, as the standard terminal
		// reader does. Dropping them prevents the model restoring host colors.
		if record.FocusEvent().SetFocus {
			r.buffer.WriteString("\x1b[I")
		} else {
			r.buffer.WriteString("\x1b[O")
		}
	case xwindows.KEY_EVENT:
		e := record.KeyEvent()
		// A VT character stream has no key releases. Serializing native
		// keyups between its characters breaks escape sequences into text.
		if !e.KeyDown {
			return
		}
		modifiedEditingKey := (e.VirtualKeyCode == xwindows.VK_RETURN || e.VirtualKeyCode == xwindows.VK_TAB) &&
			e.ControlKeyState&(xwindows.SHIFT_PRESSED|xwindows.LEFT_ALT_PRESSED|xwindows.RIGHT_ALT_PRESSED|
				xwindows.LEFT_CTRL_PRESSED|xwindows.RIGHT_CTRL_PRESSED) != 0
		if e.Char != 0 && !modifiedEditingKey {
			// ConEmu can assign ordinary virtual keys to every byte of a VT
			// packet without retaining sequence markers. Decode the character
			// stream itself instead of wrapping it in Win32 input packets.
			for range max(1, int(e.RepeatCount)) {
				r.encodeCharacter(uint16(e.Char))
			}
			return
		}
		// Native function/navigation keys can still arrive without text.
		fmt.Fprintf(&r.buffer, "\x1b[%d;%d;%d;1;%d;%d_", e.VirtualKeyCode,
			e.VirtualScanCode, e.Char, e.ControlKeyState, e.RepeatCount)
	case xwindows.WINDOW_BUFFER_SIZE_EVENT:
		// A console buffer can include thousands of scrollback rows. Layout
		// follows the visible window, not that buffer; using the latter can
		// make rendering resize the buffer again in a feedback loop.
		var info windows.ConsoleScreenBufferInfo
		if windows.GetConsoleScreenBufferInfo(r.output, &info) == nil {
			r.encodeResize(info)
		}
	case xwindows.MOUSE_EVENT:
		e := record.MouseEvent()
		button, release := tea.MouseNone, false
		// The event flags cannot be compared for equality. Windows packs a
		// drag as MOUSE_MOVED|MOUSE_LEFT_BUTTON_DOWN, and
		// MOUSE_LEFT_BUTTON_DOWN carries the same value as MOUSE_WHEELED -
		// the two are indistinguishable in this field by construction.
		// Comparing exactly therefore classified every drag as something
		// that was not a move, so no motion event was ever emitted and the
		// scrollbar could not be dragged.
		//
		// MOUSE_MOVED is unambiguous. A wheel is then told apart from a
		// click by its delta, which lives in the high word of
		// ButtonState: a click and a drag always carry zero there, so this
		// holds even when a button is held while the wheel turns.
		motion := e.EventFlags&xwindows.MOUSE_MOVED != 0
		delta := int16(e.ButtonState >> 16)
		if !motion && delta != 0 {
			positive := delta > 0
			button = tea.MouseWheelDown
			if positive {
				button = tea.MouseWheelUp
			}
			if e.EventFlags&xwindows.MOUSE_HWHEELED != 0 {
				button = tea.MouseWheelLeft
				if positive {
					button = tea.MouseWheelRight
				}
			}
		} else {
			changed := (r.mouseState ^ e.ButtonState) & 0xffff
			release = changed != 0 && changed&e.ButtonState == 0
			buttons := changed
			if motion || changed == 0 {
				buttons = e.ButtonState
			}
			switch {
			case buttons&xwindows.FROM_LEFT_1ST_BUTTON_PRESSED != 0:
				button = tea.MouseLeft
			case buttons&xwindows.RIGHTMOST_BUTTON_PRESSED != 0:
				button = tea.MouseRight
			case buttons&xwindows.FROM_LEFT_2ND_BUTTON_PRESSED != 0:
				button = tea.MouseMiddle
			}
		}
		r.mouseState = e.ButtonState & 0xffff
		if motion && button == tea.MouseNone {
			return
		}
		flags := e.ControlKeyState
		r.buffer.WriteString(ansi.MouseSgr(ansi.EncodeMouseButton(button, motion,
			flags&xwindows.SHIFT_PRESSED != 0,
			flags&(xwindows.LEFT_ALT_PRESSED|xwindows.RIGHT_ALT_PRESSED) != 0,
			flags&(xwindows.LEFT_CTRL_PRESSED|xwindows.RIGHT_CTRL_PRESSED) != 0),
			int(e.MousePositon.X), int(e.MousePositon.Y), release))
	}
}

func (r *consoleReader) encodeResize(info windows.ConsoleScreenBufferInfo) {
	width := int(info.Window.Right) - int(info.Window.Left) + 1
	height := int(info.Window.Bottom) - int(info.Window.Top) + 1
	if width <= 0 || height <= 0 || width == r.width && height == r.height {
		return
	}
	r.width, r.height = width, height
	r.buffer.WriteString(ansi.WindowOp(8, height, width))
}

func (r *consoleReader) encodeCharacter(unit uint16) {
	if r.highSurrogate != 0 {
		high := r.highSurrogate
		r.highSurrogate = 0
		if unit >= 0xdc00 && unit <= 0xdfff {
			r.buffer.WriteRune(utf16.DecodeRune(rune(high), rune(unit)))
			return
		}
		r.buffer.WriteRune(utf8.RuneError)
	}
	if unit >= 0xd800 && unit <= 0xdbff {
		r.highSurrogate = unit
		return
	}
	r.buffer.WriteRune(rune(unit))
}
