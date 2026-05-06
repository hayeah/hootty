package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/hayeah/hootty/internal/attachwire"
)

// TestPumpUpstreamToWSForwardsSnapshotFrames verifies that the
// attach→browser bridge forwards Output AND both snapshot frame
// types to the browser as binary WS frames. Before the fix, the
// switch's default arm errored on MsgSnapshotScrollback (0x04) and
// MsgSnapshotScreen (0x05), so xterm.js never saw the initial
// repaint on attach.
func TestPumpUpstreamToWSForwardsSnapshotFrames(t *testing.T) {
	// Build a fake upstream byte stream containing one of each
	// frame type the browser is expected to render.
	var upstream bytes.Buffer
	frames := []struct {
		typ     byte
		payload []byte
	}{
		{attachwire.MsgSnapshotScrollback, []byte("scroll-history\r\n")},
		{attachwire.MsgSnapshotScreen, []byte("\x1b[H\x1b[2Jvisible-screen")},
		{attachwire.MsgOutput, []byte("live-bytes")},
	}
	for _, f := range frames {
		if err := attachwire.WriteFrame(&upstream, f.typ, f.payload); err != nil {
			t.Fatalf("encode upstream frame: %v", err)
		}
	}

	// Stand up a real websocket server that runs pumpUpstreamToWS
	// against the fake upstream reader. The test client connects via
	// websocket.Dial and reads back the binary frames.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		err = pumpUpstreamToWS(ctx, ws, bufio.NewReader(&upstream))
		// EOF is the expected end state once we drain all frames.
		if err != nil && !errors.Is(err, context.Canceled) {
			// io.EOF surfaces from ReadFrame; that's fine.
			if !strings.Contains(err.Error(), "EOF") {
				t.Errorf("pumpUpstreamToWS: %v", err)
			}
		}
		_ = ws.Close(websocket.StatusNormalClosure, "done")
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "done")

	for _, want := range frames {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("read ws: %v", err)
		}
		if typ != websocket.MessageBinary {
			t.Fatalf("ws frame type = %v, want binary", typ)
		}
		if !bytes.Equal(data, want.payload) {
			t.Fatalf("ws payload for upstream typ 0x%02x = %q, want %q",
				want.typ, data, want.payload)
		}
	}
}
