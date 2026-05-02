package supervisor

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/hayeah/supervisor/internal/attachwire"
)

// attachHandler is the http.Handler that lives on /attach. It
// expects an HTTP/1.1 Upgrade-style handshake; the client sends
//
//	GET /attach HTTP/1.1
//	Upgrade: supervise-attach/1
//	Connection: Upgrade
//
// the server replies 101 Switching Protocols and then both sides
// speak the framed binary protocol from internal/attachwire.
//
// One handler instance is shared by all attaches; per-attach state
// lives in the goroutine that runs serveOne.
type attachHandler struct {
	pty *LibghosttyPTY
	set *AttachSet
}

// newAttachHandler constructs a handler bound to the given PTY. The
// AttachSet's sizeApplier resizes the PTY's master + emulator (which
// is the entire point of the multi-attach min-wins negotiation).
func newAttachHandler(pty *LibghosttyPTY) *attachHandler {
	h := &attachHandler{pty: pty}
	h.set = NewAttachSet(func(cols, rows uint16) {
		// Don't propagate errors — Resize logs internally. A failed
		// resize doesn't invalidate the attach.
		_ = pty.Resize(cols, rows)
	})
	return h
}

// ServeHTTP performs the HTTP→binary upgrade and then delegates to
// serveOne. Errors before the upgrade are returned via HTTP status
// codes + plain-text body (the spec calls for status code + body so
// the client prints it to stderr and exits non-zero).
func (h *attachHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Upgrade") == "" {
		http.Error(w, "attach: Upgrade header required", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "attach: hijacking unsupported", http.StatusInternalServerError)
		return
	}
	conn, bufrw, err := hj.Hijack()
	if err != nil {
		http.Error(w, "attach: hijack: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer conn.Close()

	// Send the 101 ourselves now that we own the conn. We can't use
	// w.WriteHeader(101) because Hijack already swallowed the
	// response writer's chance to emit headers cleanly.
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: supervise-attach/1\r\n" +
		"Connection: Upgrade\r\n\r\n"
	if _, err := bufrw.WriteString(resp); err != nil {
		return
	}
	if err := bufrw.Flush(); err != nil {
		return
	}

	if err := h.serveOne(conn, bufrw); err != nil && !errors.Is(err, io.EOF) {
		// Best-effort: try to push a final Output frame describing
		// the error. If the conn is already gone this is a no-op.
		msg := fmt.Sprintf("\r\n\x1b[31m[attach detached: %v]\x1b[0m\r\n", err)
		_ = attachwire.WriteFrame(conn, attachwire.MsgOutput, []byte(msg))
	}
}

// serveOne is the per-attach run loop. It:
//   - reads Hello
//   - registers in AttachSet (which may resize the PTY + broadcast)
//   - sends Size{effective} to the new attach
//   - subscribes to live PTY chunks atomically with reading the
//     recorder offset
//   - replays pty.log (full mode) or a libghostty snapshot
//     (snapshot mode), then drains the buffered post-offset chunks,
//     then enters live mode
//   - in parallel, reads framed messages from the client (Input,
//     Size) and forwards/handles them
//
// Returns nil on a clean client/server disconnect, error otherwise.
func (h *attachHandler) serveOne(conn io.ReadWriteCloser, bufrw *bufio.ReadWriter) error {
	// Read Hello.
	typ, payload, err := attachwire.ReadFrame(bufrw)
	if err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	if typ != attachwire.MsgHello {
		return fmt.Errorf("expected Hello (0x00), got 0x%02x", typ)
	}
	var hello attachwire.Hello
	if err := json.Unmarshal(payload, &hello); err != nil {
		return fmt.Errorf("decode hello: %w", err)
	}
	if hello.Cols == 0 || hello.Rows == 0 {
		return errors.New("hello: cols and rows must be positive")
	}

	// Serialize all writes onto the connection through one channel +
	// one goroutine. Output frames, Size frames, and the replay
	// dump all go through this — no interleaved partial frames.
	type outMsg struct {
		typ     byte
		payload []byte
	}
	outCh := make(chan outMsg, 256)
	writerDone := make(chan error, 1)
	go func() {
		var werr error
		for m := range outCh {
			if err := attachwire.WriteFrame(conn, m.typ, m.payload); err != nil {
				werr = err
				break
			}
		}
		writerDone <- werr
	}()
	// outClosed guards against double-close of outCh.
	var outOnce sync.Once
	closeOut := func() {
		outOnce.Do(func() { close(outCh) })
	}

	send := func(typ byte, payload []byte) {
		// Deliberately blocking: we want backpressure into the source
		// goroutine (the live drain), not silent drops at the protocol
		// layer.
		outCh <- outMsg{typ, payload}
	}

	// Build the attachConn and register with the set. sendSize is
	// called from AttachSet (under its lock); it must not block
	// indefinitely. The 256-deep outCh buffer is enough headroom for
	// the trivial bursts the AttachSet ever produces.
	ac := &attachConn{
		sendSize: func(ws attachWinsize) {
			payload, _ := json.Marshal(attachwire.Size{Cols: ws.Cols, Rows: ws.Rows})
			send(attachwire.MsgSize, payload)
		},
	}
	declared := attachWinsize{Cols: hello.Cols, Rows: hello.Rows}
	eff := h.set.Add(ac, declared)
	defer h.set.Remove(ac)

	// Send the initial Size{effective} immediately (spec: server
	// sends this to each attach right after Hello, before the first
	// Output frame).
	{
		payload, _ := json.Marshal(attachwire.Size{Cols: eff.Cols, Rows: eff.Rows})
		send(attachwire.MsgSize, payload)
	}

	// Subscribe to live atomically with snapshotting the screen.
	// Both happen on the dispatcher goroutine, so the snapshot we
	// send and the live chunks the subscriber sees are causally
	// ordered with no gap or duplication: the snapshot reflects the
	// emulator's state up to (but not including) any live chunk that
	// arrives on the channel.
	liveCh, _, cancelSub := h.pty.SubscribeAtRecord()
	defer cancelSub()

	// Initial replay: a libghostty snapshot of the active screen
	// (includes scrollback up to max_scrollback). Snapshot is the
	// only replay mode — full pty.log replay was removed because it
	// re-issues every terminal query the child ever sent, which the
	// real terminal would dutifully answer back into the child's
	// stdin. See spec.md / docs/tasks/<slug>/spec.md.
	snap, err := h.pty.Snapshot()
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	if len(snap) > 0 {
		send(attachwire.MsgOutput, snap)
	}

	// Now spool any chunks the live subscription has buffered (these
	// are post-recOffset bytes) and continue forwarding live. Run in
	// a goroutine so we can read client frames concurrently.
	liveDone := make(chan struct{})
	go func() {
		defer close(liveDone)
		for chunk := range liveCh {
			send(attachwire.MsgOutput, chunk)
		}
	}()

	// Client reader loop. Runs on this goroutine. Handles Input
	// (raw bytes to master), Size (client SIGWINCH), and any spurious
	// Hello (rejected).
	readErr := h.clientReadLoop(bufrw, ac)

	// Tear down: cancel subscription so the live goroutine exits;
	// close outCh so the writer goroutine exits.
	cancelSub()
	<-liveDone
	closeOut()
	<-writerDone

	if errors.Is(readErr, io.EOF) {
		return nil
	}
	return readErr
}

// clientReadLoop reads framed messages from the client until EOF or
// a protocol error.
func (h *attachHandler) clientReadLoop(bufrw *bufio.ReadWriter, ac *attachConn) error {
	for {
		typ, payload, err := attachwire.ReadFrame(bufrw)
		if err != nil {
			return err
		}
		switch typ {
		case attachwire.MsgInput:
			if err := h.pty.Write(payload); err != nil {
				return fmt.Errorf("master write: %w", err)
			}
		case attachwire.MsgSize:
			var ws attachwire.Size
			if err := json.Unmarshal(payload, &ws); err != nil {
				return fmt.Errorf("decode size: %w", err)
			}
			if ws.Cols == 0 || ws.Rows == 0 {
				continue
			}
			h.set.Update(ac, attachWinsize{Cols: ws.Cols, Rows: ws.Rows})
		case attachwire.MsgHello:
			return errors.New("unexpected Hello after handshake")
		case attachwire.MsgOutput:
			return errors.New("unexpected Output from client")
		default:
			return fmt.Errorf("unknown frame type 0x%02x", typ)
		}
	}
}

