//go:build windows

package tui

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	xwindows "github.com/charmbracelet/x/windows"
	"golang.org/x/sys/windows"
)

func TestConsoleInputHonorsPassedPipe(t *testing.T) {
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer output.Close()
	options, cleanup, err := consoleInput(t.Context(), input, output)
	if err != nil || len(options) != 0 || cleanup == nil {
		t.Fatalf("pipe should use Bubble Tea's normal reader: options=%d cleanup=%v error=%v", len(options), cleanup != nil, err)
	}
	cleanup()
}

func nativeKeyRecord(vk, char uint16, modifiers uint32) xwindows.InputRecord {
	r := xwindows.InputRecord{EventType: xwindows.KEY_EVENT}
	binary.LittleEndian.PutUint32(r.Event[0:4], 1)
	binary.LittleEndian.PutUint16(r.Event[4:6], 1)
	binary.LittleEndian.PutUint16(r.Event[6:8], vk)
	binary.LittleEndian.PutUint16(r.Event[10:12], char)
	binary.LittleEndian.PutUint32(r.Event[12:16], modifiers)
	return r
}

func TestNativeConsolePreservesWindowFocus(t *testing.T) {
	r := &consoleReader{}
	for _, focused := range []byte{0, 1} {
		record := xwindows.InputRecord{EventType: xwindows.FOCUS_EVENT}
		record.Event[0] = focused
		r.encode(record)
	}
	events := decodeConsoleEvents(t, r.buffer.String())
	if len(events) != 2 {
		t.Fatalf("focus reports lost: %#v", events)
	}
	if _, ok := events[0].(uv.BlurEvent); !ok {
		t.Fatalf("focus loss became %T", events[0])
	}
	if _, ok := events[1].(uv.FocusEvent); !ok {
		t.Fatalf("focus recovery became %T", events[1])
	}
}

func decodeConsoleEvents(t *testing.T, input string) []uv.Event {
	t.Helper()
	return readConsoleEvents(t, strings.NewReader(input))
}

func readConsoleEvents(t *testing.T, input io.Reader) []uv.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	events := make(chan uv.Event, 64)
	reader := uv.NewTerminalReader(input, "")
	done := make(chan error, 1)
	go func() {
		done <- reader.StreamEvents(ctx, events)
		close(events)
	}()
	var result []uv.Event
	for event := range events {
		result = append(result, event)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return result
}

func TestNativeConsoleDecodesPastedSurrogatePair(t *testing.T) {
	r := &consoleReader{}
	r.encode(nativeKeyRecord(0, 0xd83d, 0))
	r.encode(nativeKeyRecord(0, 0xde00, 0))
	events := decodeConsoleEvents(t, r.buffer.String())
	if len(events) != 1 {
		t.Fatalf("surrogate pair split into events: %#v", events)
	}
	key, ok := events[0].(uv.KeyPressEvent)
	if !ok || key.Text != "😀" {
		t.Fatalf("emoji corrupted: %#v", events)
	}
}

func TestNativeNonTextAndModifiedEditingKeysKeepModifiers(t *testing.T) {
	r := &consoleReader{}
	for _, record := range []xwindows.InputRecord{
		nativeKeyRecord(xwindows.VK_F1, 0, 0),
		nativeKeyRecord(xwindows.VK_UP, 0, xwindows.LEFT_ALT_PRESSED),
		nativeKeyRecord(xwindows.VK_TAB, '\t', xwindows.SHIFT_PRESSED),
		nativeKeyRecord(xwindows.VK_RETURN, '\r', xwindows.LEFT_CTRL_PRESSED),
	} {
		r.encode(record)
	}
	events := decodeConsoleEvents(t, r.buffer.String())
	if len(events) != 4 {
		t.Fatalf("unexpected native keys: %#v", events)
	}
	for i, want := range []string{"f1", "alt+up", "shift+tab", "ctrl+enter"} {
		key, ok := events[i].(uv.KeyPressEvent)
		if !ok || key.String() != want {
			t.Fatalf("key %d: got %#v, want %s", i, events[i], want)
		}
	}
}

func TestNativeConsoleModeKeepsPasteAndNativeEvents(t *testing.T) {
	for _, original := range []uint32{0, windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_QUICK_EDIT_MODE, ^uint32(0)} {
		mode := nativeConsoleMode(original)
		required := uint32(windows.ENABLE_VIRTUAL_TERMINAL_INPUT | windows.ENABLE_EXTENDED_FLAGS | windows.ENABLE_MOUSE_INPUT | windows.ENABLE_WINDOW_INPUT)
		forbidden := uint32(windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_QUICK_EDIT_MODE)
		if mode&required != required || mode&forbidden != 0 {
			t.Fatalf("unsafe console mode: %#x -> %#x", original, mode)
		}
	}
}

func TestConsoleResizeUsesVisibleWindowAndIgnoresBufferChurn(t *testing.T) {
	r := &consoleReader{}
	info := windows.ConsoleScreenBufferInfo{
		Size:   windows.Coord{X: 200, Y: 9000},
		Window: windows.SmallRect{Left: 10, Top: 500, Right: 109, Bottom: 529},
	}
	r.encodeResize(info)
	// Buffer growth and a shifted viewport are not changes to layout size.
	info.Size.Y = 16000
	info.Window.Top, info.Window.Bottom = 600, 629
	r.encodeResize(info)
	info.Window.Right = 129
	r.encodeResize(info)
	events := decodeConsoleEvents(t, r.buffer.String())
	if len(events) != 2 {
		t.Fatalf("buffer churn generated resize events: %#v", events)
	}
	for i, width := range []int{100, 120} {
		e, ok := events[i].(uv.WindowSizeEvent)
		if !ok || e.Width != width || e.Height != 30 {
			t.Fatalf("resize used buffer dimensions: %#v", events[i])
		}
	}
}

func TestConsoleReaderCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &consoleReader{ctx: ctx}
	_, err := r.Read(make([]byte, 8))
	if err != io.EOF {
		t.Fatalf("canceled read returned %v", err)
	}
}
