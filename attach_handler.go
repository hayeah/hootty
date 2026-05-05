package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/hayeah/hootty/internal/attachwire"
)

const (
	defaultAsciiCinemaPlaybackWindow = 5 * time.Minute
	defaultAsciiCinemaPlaybackSpeed  = 8.0
	maxAsciiCinemaPlaybackDelay      = 250 * time.Millisecond
)

type asciiCinemaPlaybackConfig struct {
	Enabled bool
	Window  time.Duration
	Speed   float64
}

// attachHandler is the http.Handler that lives on /attach. It
// expects an HTTP/1.1 Upgrade-style handshake; the client sends
//
//	GET /attach HTTP/1.1
//	Upgrade: hoot-attach/1
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
		"Upgrade: hoot-attach/1\r\n" +
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
//   - subscribes to live PTY chunks atomically with reading a
//     libghostty snapshot
//   - sends the snapshot, drains buffered post-snapshot chunks, then
//     enters live mode
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

	// Subscribe to live atomically with snapshotting the screen. Both
	// happen on the dispatcher goroutine, so the snapshot parts we send
	// and the live chunks the subscriber sees are causally ordered with
	// no gap or duplication: the snapshot reflects the emulator's state
	// up to (but not including) any live chunk that arrives on the channel.
	liveCh, scrollback, screen, cancelSub, err := h.pty.SubscribeWithSnapshotParts()
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	defer cancelSub()

	// Initial replay is split into history and visible-screen phases so
	// the client can place history in local scrollback, clear only the
	// viewport, then paint the visible screen on a known blank canvas.
	// Empty frames are intentional phase markers for the client.
	// Optional asciicast playback is streamed after this visible-screen
	// phase and before live output, so attach paints "where we are now"
	// before replaying recent "how we got here" history.
	send(attachwire.MsgSnapshotScrollback, scrollback)
	send(attachwire.MsgSnapshotScreen, screen)

	// Drain live chunks while historical playback runs. During
	// playback they are buffered in process memory; after playback
	// finishes, the goroutine flushes that queue and then forwards
	// live chunks directly. This keeps the live cutover byte-clean
	// without letting the subscriber channel fill and drop chunks.
	playbackDone := make(chan struct{})
	liveDone := make(chan struct{})
	go func() {
		defer close(liveDone)
		var pending [][]byte
		buffering := true
		playbackCh := playbackDone
		for {
			for !buffering && len(pending) > 0 {
				send(attachwire.MsgOutput, pending[0])
				pending[0] = nil
				pending = pending[1:]
			}
			select {
			case chunk, ok := <-liveCh:
				if !ok {
					for _, chunk := range pending {
						send(attachwire.MsgOutput, chunk)
					}
					return
				}
				if buffering {
					pending = append(pending, chunk)
					continue
				}
				send(attachwire.MsgOutput, chunk)
			case <-playbackCh:
				buffering = false
				playbackCh = nil
			}
		}
	}()

	playbackErr := h.sendAsciiCinemaPlayback(hello, send)
	close(playbackDone)
	if playbackErr != nil {
		cancelSub()
		<-liveDone
		closeOut()
		<-writerDone
		return playbackErr
	}

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

func (h *attachHandler) sendAsciiCinemaPlayback(hello attachwire.Hello, send func(byte, []byte)) error {
	rec := h.pty.Recorder()
	if rec == nil {
		return nil
	}
	cfg := asciiCinemaPlaybackConfigFromHello(hello)
	if !cfg.Enabled {
		return nil
	}
	return streamAsciiCinemaPlayback(rec.Path(), cfg, func(chunk []byte) {
		send(attachwire.MsgOutput, chunk)
	})
}

func asciiCinemaPlaybackConfigFromHello(hello attachwire.Hello) asciiCinemaPlaybackConfig {
	cfg := asciiCinemaPlaybackConfig{
		Enabled: true,
		Window:  defaultAsciiCinemaPlaybackWindow,
		Speed:   defaultAsciiCinemaPlaybackSpeed,
	}
	if hello.AsciiCinemaPlayback != nil {
		cfg.Enabled = *hello.AsciiCinemaPlayback
	}
	if hello.AsciiCinemaPlaybackWindowSeconds != nil && *hello.AsciiCinemaPlaybackWindowSeconds >= 0 {
		cfg.Window = time.Duration(*hello.AsciiCinemaPlaybackWindowSeconds * float64(time.Second))
	}
	if hello.AsciiCinemaPlaybackSpeed != nil && *hello.AsciiCinemaPlaybackSpeed > 0 {
		cfg.Speed = *hello.AsciiCinemaPlaybackSpeed
	}
	return cfg
}

func streamAsciiCinemaPlayback(path string, cfg asciiCinemaPlaybackConfig, send func([]byte)) error {
	if !cfg.Enabled {
		return nil
	}
	events, err := ReadAsciicastOutputEvents(path, cfg.Window)
	if err != nil {
		return fmt.Errorf("asciicast playback: %w", err)
	}
	if len(events) == 0 {
		return nil
	}
	speed := cfg.Speed
	if speed <= 0 {
		speed = defaultAsciiCinemaPlaybackSpeed
	}
	var stripper vtQueryStripper
	prev := events[0].At
	for i, ev := range events {
		if i > 0 {
			delay := time.Duration(float64(ev.At-prev) / speed)
			if delay > maxAsciiCinemaPlaybackDelay {
				delay = maxAsciiCinemaPlaybackDelay
			}
			if delay > 0 {
				time.Sleep(delay)
			}
			prev = ev.At
		}
		filtered := stripper.Filter(ev.Data)
		if len(filtered) > 0 {
			send(filtered)
		}
	}
	return nil
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
