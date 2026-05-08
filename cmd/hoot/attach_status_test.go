package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hayeah/hootty/internal/attachwire"
)

// TestEmitAttachStatusByteSequence locks down the exact byte sequence
// emitAttachStatus writes. The shape is load-bearing: the position →
// clear-line → HUD → scroll-up → home ordering is what makes the HUD
// line land as the bottom-most scrollback row in modern terminals.
// Drift here would silently regress the "scroll up one line to see it"
// invariant from the section spec.
func TestEmitAttachStatusByteSequence(t *testing.T) {
	var buf bytes.Buffer
	hud := newHUDState("🦉 abc /tmp bash")
	if err := emitAttachStatus(&buf, hud, attachLabel{Session: "abc", Host: "local"}, 24); err != nil {
		t.Fatalf("emitAttachStatus: %v", err)
	}
	want := "\x1b[24;1H\x1b[2K\x1b[2m🦉 abc /tmp bash\x1b[0m\x1b[24S\x1b[H"
	if got := buf.String(); got != want {
		t.Fatalf("emitAttachStatus bytes mismatch:\n got %q\nwant %q", got, want)
	}
}

// TestEmitAttachStatusFallback verifies the "[connected. K @ H]"
// fallback fires when hud.Line() is empty (state.json miss / nil
// hud). Otherwise an attach with a missing HUD would appear silent
// in scrollback — no marker at all.
func TestEmitAttachStatusFallback(t *testing.T) {
	cases := []struct {
		name string
		hud  *hudState
	}{
		{"empty line", newHUDState("")},
		{"nil hud", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := emitAttachStatus(&buf, c.hud, attachLabel{Session: "abc", Host: "remote"}, 12); err != nil {
				t.Fatalf("emitAttachStatus: %v", err)
			}
			if !strings.Contains(buf.String(), "[connected. abc @ remote]") {
				t.Fatalf("missing fallback banner: %q", buf.String())
			}
			if !strings.Contains(buf.String(), "\x1b[12S") {
				t.Fatalf("fallback path skipped CSI <rows> S scroll: %q", buf.String())
			}
		})
	}
}

// TestEmitAttachStatusZeroRowsDefaults24 ensures a zero localRows
// (e.g. attaching from a non-tty pipe where term.GetSize returned 0,0)
// does not emit `CSI 0 S` (which is a no-op on most terminals and
// would skip the scrollback push). 24 is the same fallback the rest
// of the code uses for non-tty stdin.
func TestEmitAttachStatusZeroRowsDefaults24(t *testing.T) {
	var buf bytes.Buffer
	if err := emitAttachStatus(&buf, newHUDState("hud"), attachLabel{Session: "k", Host: "h"}, 0); err != nil {
		t.Fatalf("emitAttachStatus: %v", err)
	}
	if !strings.Contains(buf.String(), "\x1b[24;1H") || !strings.Contains(buf.String(), "\x1b[24S") {
		t.Fatalf("zero rows did not default to 24: %q", buf.String())
	}
}

// TestRunServerLoopSnapshotByteOrdering drives the snapshot phase end-
// to-end in process: a fake server sends MsgSnapshotScrollback then
// MsgSnapshotScreen, and we assert the bytes runServerLoop writes to
// stdout match the spec-mandated sequence:
//
//   - scrollback payload bytes flow through unchanged (no surrounding
//     control bytes — they are pre-formatted by the libghostty
//     formatter)
//   - then the HUD scrollback line + scroll-up + cursor home
//   - then the screen snapshot payload
//
// Order matters for "scroll up one line to see HUD"; the test fails
// if any phase moves out of sequence or if the scroll-up CSI is
// dropped.
func TestRunServerLoopSnapshotByteOrdering(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	scrollback := []byte("history-line-1\r\nhistory-line-2\r\n")
	screen := []byte("snapshot-payload-bytes")
	go func() {
		defer serverConn.Close()
		if err := attachwire.WriteFrame(serverConn, attachwire.MsgSnapshotScrollback, scrollback); err != nil {
			t.Errorf("write scrollback: %v", err)
			return
		}
		if err := attachwire.WriteFrame(serverConn, attachwire.MsgSnapshotScreen, screen); err != nil {
			t.Errorf("write screen: %v", err)
			return
		}
		// Idle so runServerLoop reads to EOF after the snapshot pair.
	}()

	var stdout bytes.Buffer
	writers := attachWriters{stdout: &stdout, stderr: io.Discard}
	var attached atomic.Bool
	r := bufio.NewReader(clientConn)
	hud := newHUDState("🦉 testid /tmp sh")
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		done <- runServerLoop(ctx, r, attachLabel{Session: "testid", Host: "local"}, &attached, writers, nil, hud, 8, nil)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("runServerLoop did not return after server EOF")
	}

	got := stdout.String()
	idxScrollback := strings.Index(got, "history-line-1\r\nhistory-line-2\r\n")
	idxHUD := strings.Index(got, "\x1b[2m🦉 testid /tmp sh\x1b[0m")
	idxScroll := strings.Index(got, "\x1b[8S\x1b[H")
	idxScreen := strings.Index(got, "snapshot-payload-bytes")

	if idxScrollback < 0 {
		t.Fatalf("stdout missing scrollback payload: %q", got)
	}
	if idxHUD < 0 {
		t.Fatalf("stdout missing dim-wrapped HUD line: %q", got)
	}
	if idxScroll < 0 {
		t.Fatalf("stdout missing CSI 8 S scroll-up + CSI H: %q", got)
	}
	if idxScreen < 0 {
		t.Fatalf("stdout missing screen snapshot payload: %q", got)
	}
	// Spec ordering: scrollback → status line → screen snapshot.
	if !(idxScrollback < idxHUD && idxHUD < idxScroll && idxScroll < idxScreen) {
		t.Fatalf("phase order broken: scrollback=%d hud=%d scroll=%d screen=%d in %q",
			idxScrollback, idxHUD, idxScroll, idxScreen, got)
	}
	if !attached.Load() {
		t.Fatalf("attached flag not set after snapshot phase")
	}
}
