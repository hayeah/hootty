package main

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hayeah/hootty/internal/attachetest"
)

// TestHootRestorer_E2EClearsCatalogueModes drives a full attach-detach cycle
// through libghostty on both ends. The supervisor side has a bunch of modes
// from the cleanup catalogue SET via raw escape bytes (alt-screen, mouse,
// focus events, bracketed paste, cursor-hidden, modifyOtherKeys, kitty
// keyboard push, OSC 9;4 progress). After detach + cleanup, we ask the
// user-side libghostty Terminal directly: are the modes back to defaults?
//
// This is the inverse of the unit tests, which check what bytes Cleanup
// emits. Here we check what those bytes actually DO when applied to a real
// terminal. If we ever change a constant from `\x1b[?1004l` to something
// typo'd or wrong, the unit tests still pass (they check the constant)
// but THIS test fails (the user terminal still has focus events on).
func TestHootRestorer_E2EClearsCatalogueModes(t *testing.T) {
	remote := attachetest.NewRemote(t, 80, 24)

	// Set a comprehensive set of modes on the supervisor PTY. The trailing
	// "marker" is what we WaitText for, to ensure the snapshot has fully
	// landed on the local side before we proceed.
	setBytes := "" +
		"\x1b[?1049h" + // alt screen + cursor save
		"\x1b[?1004h" + // focus events
		"\x1b[?1000h" + // mouse normal
		"\x1b[?1002h" + // mouse button event
		"\x1b[?1006h" + // mouse SGR
		"\x1b[?2004h" + // bracketed paste
		"\x1b[?2026h" + // synchronized output
		"\x1b[?25l" + // cursor hidden
		"\x1b[>4;2m" + // modifyOtherKeys mode 2
		"\x1b[>u" + // kitty keyboard push
		"\x1b]9;4;1;50\x07" + // OSC 9;4 set progress to 50%
		"marker"
	remote.WriteAndWait(t, []byte(setBytes), "marker")

	local := attachetest.NewLocalTerminal(t, 80, 24)

	conn := remote.DialAttach(t)
	if err := upgradeAttachConn(conn, remote.AttachURL()); err != nil {
		t.Fatalf("upgrade attach: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	shared := &sharedSession{wake: make(chan struct{}, 1)}
	var attached atomic.Bool
	restorer := &hootRestorer{}

	// Mirror runAttachLoop's lifecycle: Attach BEFORE the session starts,
	// so the kitty kbd push frame is owned by hoot before any snapshot
	// bytes paint. (The real runAttachLoop does this between term.MakeRaw
	// and runConnectLoop; this test bypasses runAttachLoop and goes
	// straight to runSession, so we replicate the call here.)
	if err := restorer.Attach(local); err != nil {
		t.Fatalf("restorer.Attach: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- runSession(ctx, conn, 80, 24, shared, attachLabel{Session: "e2e", Host: "local"}, &attached, attachWriters{
			stdout: local,
			stderr: io.Discard,
		}, restorer)
	}()

	// Wait for attach + snapshot to land.
	deadline := time.Now().Add(2 * time.Second)
	for !attached.Load() {
		if time.Now().After(deadline) {
			t.Fatalf("attach session did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	local.WaitText(t, "marker")

	// Mid-attach sanity: verify the snapshot actually carried the modes
	// across to the user-side libghostty (otherwise the post-cleanup
	// asserts would be vacuously true).
	midSnap := local.Snapshot(t)
	if !strings.Contains(midSnap, "active=alternate") {
		t.Errorf("mid-attach: expected active=alternate screen, snapshot:\n%s", midSnap)
	}
	if !strings.Contains(midSnap, "visible=false") {
		t.Errorf("mid-attach: expected visible=false (cursor hidden), snapshot:\n%s", midSnap)
	}
	// Mid-attach should also include the mode-set sequences in the formatVT
	// output. If they aren't there, the post-cleanup absence assertions
	// further down would be vacuously true.
	for _, want := range []string{"[?1004h", "[?1000h", "[?1049h"} {
		if !strings.Contains(midSnap, want) {
			t.Errorf("mid-attach: expected formatVT to include %q (mode set on supervisor side); snapshot:\n%s", want, midSnap)
		}
	}

	// End the session and run cleanup.
	cancel()
	_ = conn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("session did not end")
	}
	emitDetach(local, attachLabel{Session: "e2e", Host: "local"}, restorer)

	// Post-cleanup: verify the user-side libghostty Terminal is back to
	// defaults. These assertions go through libghostty's native state
	// queries, not by re-parsing the bytes we emitted.
	postSnap := local.Snapshot(t)

	// Alt-screen should be left (?1049 cleanup).
	if !strings.Contains(postSnap, "active=primary") {
		t.Errorf("post-cleanup: expected active=primary, snapshot:\n%s", postSnap)
	}

	// Cursor should be visible (?25h cleanup).
	if !strings.Contains(postSnap, "visible=true") {
		t.Errorf("post-cleanup: expected visible=true, snapshot:\n%s", postSnap)
	}

	// The libghostty formatter with WithFormatterExtraModes(true) emits the
	// current mode state as VT mode-set sequences in its serialization. If
	// any of these mode-set bytes appear in the post-cleanup snapshot, the
	// corresponding mode is still set on the local terminal.
	stuckModes := []struct {
		name    string
		setBody string // body of the VT mode-set sequence we'd see if still set
	}{
		{"focus-events", "[?1004h"},
		{"mouse-normal", "[?1000h"},
		{"mouse-button", "[?1002h"},
		{"mouse-sgr", "[?1006h"},
		{"bracketed-paste", "[?2004h"},
		{"synchronized-output", "[?2026h"},
		{"alt-screen", "[?1049h"},
	}
	for _, m := range stuckModes {
		if strings.Contains(postSnap, m.setBody) {
			t.Errorf("post-cleanup: mode %s still set (found %q in snapshot):\n%s",
				m.name, m.setBody, postSnap)
		}
	}
}
