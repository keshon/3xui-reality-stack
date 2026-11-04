//go:build windows

package tui

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf16"

	uv "github.com/charmbracelet/ultraviolet"
	xwindows "github.com/charmbracelet/x/windows"
)

// ConEmu PostString marks an XTerm sequence with scan code 255 on the
// first keydown and last keyup. Its characters can have nonzero virtual keys.
func conEmuSequenceRecords(text string) []xwindows.InputRecord {
	units := utf16.Encode([]rune(text))
	var records []xwindows.InputRecord
	for i, unit := range units {
		down := nativeKeyRecord(unit, unit, 0)
		up := down
		binary.LittleEndian.PutUint32(up.Event[0:4], 0)
		if i == 0 {
			binary.LittleEndian.PutUint16(down.Event[8:10], 255)
		}
		if i == len(units)-1 {
			binary.LittleEndian.PutUint16(up.Event[8:10], 255)
		}
		records = append(records, down, up)
	}
	return records
}

func TestConEmuMousePacketsDoNotBecomeText(t *testing.T) {
	r := &consoleReader{}
	for _, packet := range []string{"\x1b[<0;10;4M", "\x1b[<32;11;5M", "\x1b[<0;11;5m", "\x1b[<64;11;5M"} {
		for _, record := range conEmuSequenceRecords(packet) {
			r.encode(record)
		}
	}
	r.encode(nativeKeyRecord('X', 'x', 0))
	events := decodeConsoleEvents(t, r.buffer.String())
	if len(events) != 5 {
		t.Fatalf("mouse packets escaped as text: %d events", len(events))
	}
	for i, want := range []string{"click", "motion", "release", "wheelup"} {
		if got := mouseKind(events[i]); got != want {
			t.Fatalf("event %d: got %s, want %s", i, got, want)
		}
	}
	key, ok := events[4].(uv.KeyPressEvent)
	if !ok || key.Text != "x" {
		t.Fatalf("normal keyboard input after mouse packet was lost: %#v", events[4])
	}
}

func TestUnmarkedVTKeysAndMouseDoNotBecomeText(t *testing.T) {
	r := &consoleReader{}
	for _, packet := range []string{"\x1b[<0;29;1M", "\x1b[<3;29;1m", "\x1bOP", "\x1bOQ", "\x1bOR", "\t", "\x1b[A"} {
		for _, record := range conEmuSequenceRecords(packet) {
			// Some ConEmu paths deliver the translated character key pairs
			// without the scan-code markers. Include releases between every
			// character, exactly where a serialized keyup can break CSI/SS3.
			binary.LittleEndian.PutUint16(record.Event[8:10], 0)
			r.encode(record)
		}
	}
	for _, input := range []io.Reader{strings.NewReader(r.buffer.String()), iotest.OneByteReader(strings.NewReader(r.buffer.String()))} {
		events := readConsoleEvents(t, input)
		if len(events) != 7 {
			t.Fatalf("VT packets leaked into input: got %d events, want 7", len(events))
		}
		if mouseKind(events[0]) != "click" || mouseKind(events[1]) != "release" {
			t.Fatalf("mouse packets were not decoded: %#v", events[:2])
		}
		for i, want := range []string{"f1", "f2", "f3", "tab", "up"} {
			key, ok := events[i+2].(uv.KeyPressEvent)
			if !ok || key.String() != want || key.Text != "" {
				t.Fatalf("VT key %d: got %#v, want %s without text", i, events[i+2], want)
			}
		}
	}
}

func TestConEmuPasteKeepsNewlinesAndCommandsInDraft(t *testing.T) {
	text := "first\n/quit\nПривет 😀\n"
	r := &consoleReader{}
	for _, record := range conEmuSequenceRecords("\x1b[200~" + text + "\x1b[201~") {
		r.encode(record)
	}
	events := decodeConsoleEvents(t, r.buffer.String())
	pastes := 0
	for _, event := range events {
		switch e := event.(type) {
		case uv.PasteStartEvent, uv.PasteEndEvent:
		case uv.PasteEvent:
			pastes++
			if e.Content != text {
				t.Fatalf("ConEmu paste changed: %q", e.Content)
			}
		default:
			t.Fatalf("ConEmu paste became keyboard input: %T", event)
		}
	}
	if pastes != 1 {
		t.Fatalf("got %d pastes, want 1", pastes)
	}
}

func nativeMouseRecord(x, y int16, buttonState, control, flags uint32) xwindows.InputRecord {
	r := xwindows.InputRecord{EventType: xwindows.MOUSE_EVENT}
	binary.LittleEndian.PutUint16(r.Event[0:2], uint16(x))
	binary.LittleEndian.PutUint16(r.Event[2:4], uint16(y))
	binary.LittleEndian.PutUint32(r.Event[4:8], buttonState)
	binary.LittleEndian.PutUint32(r.Event[8:12], control)
	binary.LittleEndian.PutUint32(r.Event[12:16], flags)
	return r
}

func encodeMouse(records ...xwindows.InputRecord) string {
	r := &consoleReader{}
	for _, rec := range records {
		r.encode(rec)
	}
	return r.buffer.String()
}

// Windows packs a drag as MOUSE_MOVED|MOUSE_LEFT_BUTTON_DOWN, and
// MOUSE_LEFT_BUTTON_DOWN is the same value as MOUSE_WHEELED. Comparing
// event flags for equality classified every drag as "not a move", so no
// motion event was emitted and the scrollbar could not be dragged. The wheel
// and a click with a held button are told apart the same way.
//
// The delta lives in the high word of ButtonState: 120 per notch.
const (
	notchUp   = uint32(120) << 16
	notchDown = uint32(0xFF880000) // -120
	leftDown  = xwindows.FROM_LEFT_1ST_BUTTON_PRESSED
	// MOUSE_LEFT_BUTTON_DOWN shares MOUSE_WHEELED's value, which is the
	// whole reason the flags cannot be compared for equality.
	leftButtonDown = xwindows.MOUSE_WHEELED
)

func TestNativeConsoleClassifiesRealMouseRecords(t *testing.T) {
	cases := []struct {
		name    string
		records []xwindows.InputRecord
		want    []string
	}{
		{
			name: "drag is motion with the button held",
			records: []xwindows.InputRecord{
				nativeMouseRecord(5, 3, leftDown, 0, leftButtonDown),
				nativeMouseRecord(5, 4, leftDown, 0,
					xwindows.MOUSE_MOVED|leftButtonDown),
				nativeMouseRecord(5, 9, leftDown, 0,
					xwindows.MOUSE_MOVED|leftButtonDown),
			},
			// press, then two motions. A drag that arrives as clicks is
			// what made the scrollbar look dead.
			want: []string{"click", "motion", "motion"},
		},
		{
			name: "plain move with no button is dropped",
			records: []xwindows.InputRecord{
				nativeMouseRecord(5, 3, 0, 0, xwindows.MOUSE_MOVED),
			},
			want: nil,
		},
		{
			name: "wheel both ways with no button held",
			records: []xwindows.InputRecord{
				nativeMouseRecord(5, 3, notchUp, 0, xwindows.MOUSE_WHEELED),
				nativeMouseRecord(5, 3, notchDown, 0, xwindows.MOUSE_WHEELED),
			},
			want: []string{"wheelup", "wheeldown"},
		},
		{
			name: "wheel while a button is held is still a wheel",
			records: []xwindows.InputRecord{
				nativeMouseRecord(5, 3, notchUp|leftDown, 0, xwindows.MOUSE_WHEELED),
			},
			want: []string{"wheelup"},
		},
		{
			name: "release ends the drag",
			records: []xwindows.InputRecord{
				nativeMouseRecord(5, 3, leftDown, 0, leftButtonDown),
				nativeMouseRecord(5, 3, 0, 0, 0),
			},
			want: []string{"click", "release"},
		},
		{
			// A positive delta is away from the user, which for a
			// horizontal wheel is right.
			name: "horizontal wheel",
			records: []xwindows.InputRecord{
				nativeMouseRecord(5, 3, notchUp, 0, xwindows.MOUSE_HWHEELED),
				nativeMouseRecord(5, 3, notchDown, 0, xwindows.MOUSE_HWHEELED),
			},
			want: []string{"wheelright", "wheelleft"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			events := decodeConsoleEvents(t, encodeMouse(c.records...))
			var got []string
			for _, e := range events {
				got = append(got, mouseKind(e))
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("event %d was %q, want %q (all: %v)", i, got[i], c.want[i], got)
				}
			}
		})
	}
}

// mouseKind names a parsed mouse event the way the application sees it.
func mouseKind(e uv.Event) string {
	switch ev := e.(type) {
	case uv.MouseWheelEvent:
		return ev.String()
	case uv.MouseMotionEvent:
		return "motion"
	case uv.MouseReleaseEvent:
		return "release"
	case uv.MouseClickEvent:
		return "click"
	}
	return fmt.Sprintf("other(%T)", e)
}
