package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hayeah/hootty/internal/attachwire"
)

// TestAttachHandlerNoHistorySendsEmptyScrollback drives serveOne against a
// real LibghosttyPTY whose scrollback ring has been pre-populated with
// content. With noHistory=false, the MsgSnapshotScrollback frame carries
// non-empty libghostty-rendered scrollback bytes; with noHistory=true,
// the frame is sent but with an empty payload (the phase marker is kept
// so the client's snapshot FSM still progresses through both phases).
//
// The visible-screen snapshot frame stays non-empty in both cases — the
// no-history flag only suppresses scrollback replay, never the live
// viewport repaint that lets the user see the current state.
func TestAttachHandlerNoHistorySendsEmptyScrollback(t *testing.T) {
	cases := []struct {
		name      string
		noHistory bool
	}{
		{"history-included", false},
		{"history-skipped", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, slave, cleanup := newTestLibghosttyPTY(t, 20, 4)
			defer cleanup()

			// Push enough lines through the emulator to overflow the
			// 4-row viewport into scrollback. The marker line lands on
			// the visible screen.
			var fixture bytes.Buffer
			for i := 1; i <= 12; i++ {
				fmt.Fprintf(&fixture, "line-%02d\r\n", i)
			}
			fixture.WriteString("marker")
			writePTYAndWait(t, p, slave, fixture.Bytes(), "marker")

			h := &attachHandler{pty: p, noHistory: c.noHistory}
			h.set = NewAttachSet(func(uint16, uint16) {})

			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			defer serverConn.Close()

			done := make(chan error, 1)
			go func() {
				bufrw := bufio.NewReadWriter(
					bufio.NewReader(serverConn),
					bufio.NewWriter(serverConn),
				)
				done <- h.serveOne(serverConn, bufrw)
			}()

			// Client: send Hello, then read frames until we've seen
			// both snapshot phases. We then close the conn to unwind
			// serveOne's read loop.
			helloPayload, _ := json.Marshal(attachwire.Hello{
				Size: attachwire.PTYSize{Cols: 20, Rows: 4},
			})
			if err := attachwire.WriteFrame(clientConn, attachwire.MsgHello, helloPayload); err != nil {
				t.Fatalf("write hello: %v", err)
			}

			r := bufio.NewReader(clientConn)
			var gotScrollback, gotScreen []byte
			haveScrollback, haveScreen := false, false
			deadline := time.Now().Add(3 * time.Second)
			_ = clientConn.SetReadDeadline(deadline)
			for !haveScrollback || !haveScreen {
				typ, payload, err := attachwire.ReadFrame(r)
				if err != nil {
					t.Fatalf("read frame: %v (have scrollback=%v screen=%v)", err, haveScrollback, haveScreen)
				}
				switch typ {
				case attachwire.MsgSnapshotScrollback:
					gotScrollback = payload
					haveScrollback = true
				case attachwire.MsgSnapshotScreen:
					gotScreen = payload
					haveScreen = true
				case attachwire.MsgSize, attachwire.MsgOutput, attachwire.MsgPong:
					// expected ancillary frames before/around the snapshot
					// pair — ignore.
				default:
					t.Fatalf("unexpected frame type 0x%02x", typ)
				}
			}

			// Trigger serveOne unwind by closing the client end.
			_ = clientConn.Close()
			select {
			case err := <-done:
				// EOF / closed conn is fine; anything else is suspect.
				if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
					t.Logf("serveOne returned: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("serveOne did not unwind within 2s")
			}

			if c.noHistory {
				if len(gotScrollback) != 0 {
					t.Fatalf("noHistory=true: scrollback payload = %d bytes, want 0\nbytes=%q", len(gotScrollback), gotScrollback)
				}
			} else {
				if len(gotScrollback) == 0 {
					t.Fatalf("noHistory=false: scrollback payload was empty; expected non-empty libghostty replay")
				}
				if !bytes.Contains(gotScrollback, []byte("line-")) {
					t.Fatalf("noHistory=false: scrollback payload missing line-* content: %q", gotScrollback)
				}
			}
			if len(gotScreen) == 0 {
				t.Fatalf("screen snapshot payload was empty; expected non-empty viewport repaint regardless of noHistory")
			}
			if !bytes.Contains(gotScreen, []byte("marker")) {
				t.Fatalf("screen snapshot missing marker: %q", gotScreen)
			}
		})
	}
}
