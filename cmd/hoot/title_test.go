package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHudTitleSetBytes(t *testing.T) {
	got := hudTitleSetBytes("🦉 abc /x sh")
	want := "\x1b]2;🦉 abc /x sh\x07"
	if got != want {
		t.Errorf("hudTitleSetBytes = %q, want %q", got, want)
	}
}

func TestHudTitleSetBytesSanitizesControl(t *testing.T) {
	// Embedded BEL would terminate the OSC string early; embedded ESC
	// would splice in a fresh control sequence. Both must be stripped.
	got := hudTitleSetBytes("hi\x07evil\x1b[2J")
	want := "\x1b]2;hievil[2J\x07"
	if got != want {
		t.Errorf("hudTitleSetBytes = %q, want %q", got, want)
	}
}

func TestHudTitleSetBytesPreservesUTF8(t *testing.T) {
	// The owl emoji is multi-byte UTF-8; sanitizeTitle must walk runes
	// or it'll treat continuation bytes as control bytes and drop them.
	got := hudTitleSetBytes("🦉 ok")
	want := "\x1b]2;🦉 ok\x07"
	if got != want {
		t.Errorf("hudTitleSetBytes = %q, want %q", got, want)
	}
}

func TestHudTitlePushPopConstants(t *testing.T) {
	// xterm window-manipulation: 22;2 push, 23;2 pop. Asserting the
	// exact bytes guards against a future "make it nicer" tweak that
	// silently drops modern-terminal compatibility.
	if hudTitlePushBytes != "\x1b[22;2t" {
		t.Errorf("hudTitlePushBytes = %q, want \\x1b[22;2t", hudTitlePushBytes)
	}
	if hudTitlePopBytes != "\x1b[23;2t" {
		t.Errorf("hudTitlePopBytes = %q, want \\x1b[23;2t", hudTitlePopBytes)
	}
}

func TestSanitizeTitleHotPath(t *testing.T) {
	// needsSanitize returns false on the common path, and the function
	// returns its input unchanged.
	in := "🦉 abcdef ~/proj bash --login"
	if got := sanitizeTitle(in); got != in {
		t.Errorf("sanitizeTitle changed clean input: %q -> %q", in, got)
	}
}

// withTTY swaps stdoutIsTTY for the duration of a test. We can't make
// a real os.Stdout look like a tty under `go test`, so the helpers
// take the function var route.
func withTTY(t *testing.T, isTTY bool) {
	t.Helper()
	prev := stdoutIsTTY
	stdoutIsTTY = func() bool { return isTTY }
	t.Cleanup(func() { stdoutIsTTY = prev })
}

func TestHUDStateOnAttachEmitsPushAndSet(t *testing.T) {
	withTTY(t, true)
	hud := newHUDState("🦉 abcde /x sh")
	var buf bytes.Buffer
	hud.onAttach(&buf)
	got := buf.String()
	want := hudTitlePushBytes + "\x1b]2;🦉 abcde /x sh\x07"
	if got != want {
		t.Errorf("onAttach emitted %q, want %q", got, want)
	}
}

func TestHUDStateOnAttachIdempotentPush(t *testing.T) {
	// A second onAttach should re-set the title (e.g. after a
	// reconnect re-renders, in some future caller) but must NOT push
	// again — that would deepen the stack and leak entries on detach.
	withTTY(t, true)
	hud := newHUDState("🦉 abc /x sh")
	var buf bytes.Buffer
	hud.onAttach(&buf)
	first := buf.Len()
	hud.onAttach(&buf)
	got := buf.String()[first:]
	if strings.Contains(got, hudTitlePushBytes) {
		t.Errorf("second onAttach pushed again: %q", got)
	}
	if !strings.Contains(got, "\x1b]2;") {
		t.Errorf("second onAttach should still set: %q", got)
	}
}

func TestHUDStateOnDetachOnlyPopsIfPushed(t *testing.T) {
	withTTY(t, true)
	hud := newHUDState("🦉 abc /x sh")

	var noPush bytes.Buffer
	hud.onDetach(&noPush)
	if noPush.Len() != 0 {
		t.Errorf("onDetach without prior push wrote bytes: %q", noPush.String())
	}

	var withPush bytes.Buffer
	hud.onAttach(&withPush)
	withPush.Reset()
	hud.onDetach(&withPush)
	if withPush.String() != hudTitlePopBytes {
		t.Errorf("onDetach after push = %q, want %q", withPush.String(), hudTitlePopBytes)
	}
}

func TestHUDStateNonTTYNoEmit(t *testing.T) {
	withTTY(t, false)
	hud := newHUDState("🦉 abc /x sh")
	var buf bytes.Buffer
	hud.onAttach(&buf)
	hud.onDetach(&buf)
	if buf.Len() != 0 {
		t.Errorf("non-tty emitted control bytes: %q", buf.String())
	}
}

func TestHUDStateEmptyLineNoEmit(t *testing.T) {
	// loadHUDLine returned "" (state.json missing or unreadable);
	// onAttach must skip emitting so the user sees their existing
	// terminal title untouched.
	withTTY(t, true)
	hud := newHUDState("")
	var buf bytes.Buffer
	hud.onAttach(&buf)
	if buf.Len() != 0 {
		t.Errorf("empty HUD emitted bytes: %q", buf.String())
	}
}

func TestHUDStatePrintChord(t *testing.T) {
	hud := newHUDState("🦉 abcde /x sh")
	var buf bytes.Buffer
	hud.printChord(&buf)
	got := buf.String()

	if !strings.Contains(got, "🦉 abcde /x sh") {
		t.Errorf("printChord missing HUD line: %q", got)
	}
	if !strings.HasPrefix(got, "\r\n") {
		t.Errorf("printChord did not lead with CRLF: %q", got)
	}
	if !strings.HasSuffix(got, "\r\n") {
		t.Errorf("printChord did not trail with CRLF: %q", got)
	}
	// Chord help should still be visible — losing it would be a
	// regression for users who hit `?` expecting docs.
	if !strings.Contains(got, "detach") || !strings.Contains(got, "clone") {
		t.Errorf("printChord missing chord-help text: %q", got)
	}
}

func TestHUDStatePrintChordEmpty(t *testing.T) {
	hud := newHUDState("")
	var buf bytes.Buffer
	hud.printChord(&buf)
	got := buf.String()
	if !strings.Contains(got, "session info unavailable") {
		t.Errorf("printChord with empty line missing fallback: %q", got)
	}
}
