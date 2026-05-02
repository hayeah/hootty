package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/coder/websocket"

	"github.com/hayeah/supervisor/internal/attachwire"
	"github.com/hayeah/supervisor/internal/shortid"
)

// handleEvents is a passthrough proxy of the upstream /events SSE
// stream from rpc.sock to the HTTP client. The browser can't dial
// a unix socket itself, so we copy bytes through the response
// writer with periodic flushes.
func (s *serveState) handleEvents(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "missing session key", http.StatusBadRequest)
		return
	}
	sockPath := filepath.Join(s.stateDir, key, "rpc.sock")

	// HTTP client over a Unix-socket transport that ignores the URL
	// host. We use it for /events; /attach is hijacked instead.
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) {
				return dialSock(ctx, sockPath)
			},
		},
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://unix/events", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream status "+resp.Status, http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	flusher.Flush()

	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flusher.Flush()
		}
		if rerr != nil {
			return
		}
	}
}

// attachMessage is the WS text-frame envelope. Today only
// {type:"resize", cols, rows} is recognized. Unknown types are
// silently ignored — forward-compatible for future {type:"ping"}
// or similar.
type attachMessage struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

// handleAttach is the browser-facing WebSocket bridge. It
// terminates a WS on the client side and bridges it to the
// supervisor's per-session /attach (HTTP/1.1 Upgrade →
// attachwire framed protocol) on rpc.sock.
//
// Wire mapping (browser ↔ bridge ↔ supervisor):
//
//	browser binary  ←  attachwire MsgOutput   (PTY bytes server→client)
//	browser binary  →  attachwire MsgInput    (raw input client→server)
//	browser text {type:"resize",cols,rows}  →  attachwire MsgSize
//	attachwire MsgSize (server→client)       →  ignored (browser
//	                                             doesn't render
//	                                             remote-size mirror)
//
// Initial attachwire Hello uses 80x24; the browser's xterm-style
// addon will fire a resize on mount which reaches the supervisor
// as the first MsgSize.
func (s *serveState) handleAttach(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "missing session key", http.StatusBadRequest)
		return
	}
	sockPath := filepath.Join(s.stateDir, key, "rpc.sock")

	// Step 1: open the WS to the browser. websocket.Accept writes
	// the 101 itself.
	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// In dev we sit behind devport's proxy on a different
		// port; in production, same-origin. Either way this
		// process is never directly exposed to untrusted origins.
		InsecureSkipVerify: true,
	})
	if err != nil {
		// Accept already wrote a response on failure.
		return
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "bye")

	// Step 2: dial rpc.sock and perform the supervise-attach/1
	// HTTP Upgrade, then send Hello.
	upstream, err := dialAttach(r.Context(), sockPath)
	if err != nil {
		_ = wsConn.Close(websocket.StatusInternalError, "upstream dial: "+err.Error())
		return
	}
	defer upstream.conn.Close()

	helloPayload, _ := json.Marshal(attachwire.Hello{
		Cols:       80,
		Rows:       24,
		ReplayMode: "full",
	})
	if err := attachwire.WriteFrame(upstream.conn, attachwire.MsgHello, helloPayload); err != nil {
		_ = wsConn.Close(websocket.StatusInternalError, "hello: "+err.Error())
		return
	}

	// Single-writer mutex on the upstream so Input frames from the
	// WS-reader pump and Size frames from the same pump never
	// interleave. (Today both are sent from the same goroutine, so
	// the mutex is belt-and-suspenders, but cheap.)
	var upstreamMu sync.Mutex
	writeUpstream := func(typ byte, payload []byte) error {
		upstreamMu.Lock()
		defer upstreamMu.Unlock()
		return attachwire.WriteFrame(upstream.conn, typ, payload)
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Pump A: upstream attachwire frames → WS binary frames.
	upToWS := make(chan error, 1)
	go func() {
		upToWS <- pumpUpstreamToWS(ctx, wsConn, upstream.reader)
	}()

	// Pump B: WS frames → upstream attachwire frames.
	wsToUp := make(chan error, 1)
	go func() {
		wsToUp <- pumpWSToUpstream(ctx, wsConn, writeUpstream)
	}()

	// Either side finishing tears the rest down via cancel/Close.
	select {
	case <-r.Context().Done():
	case <-upToWS:
	case <-wsToUp:
	}
}

// upstreamConn pairs a hijacked rpc.sock conn with its bufio.Reader,
// since the Upgrade handshake needs a buffered reader for
// http.ReadResponse.
type upstreamConn struct {
	conn   net.Conn
	reader *bufio.Reader
}

// dialAttach dials rpc.sock and performs the HTTP Upgrade dance for
// the supervise-attach/1 protocol. Returns a connection ready for
// attachwire frames in both directions.
func dialAttach(ctx context.Context, sockPath string) (*upstreamConn, error) {
	conn, err := dialSock(ctx, sockPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", sockPath, err)
	}
	bufrw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

	req, err := http.NewRequest(http.MethodGet, "http://supervise/attach", nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Upgrade", "supervise-attach/1")
	req.Header.Set("Connection", "Upgrade")
	if err := req.Write(bufrw); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write upgrade: %w", err)
	}
	if err := bufrw.Flush(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("flush upgrade: %w", err)
	}
	resp, err := http.ReadResponse(bufrw.Reader, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read upgrade response: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		conn.Close()
		return nil, fmt.Errorf("supervisor refused upgrade: %s\n%s", resp.Status, string(body))
	}
	resp.Body.Close()
	return &upstreamConn{conn: conn, reader: bufrw.Reader}, nil
}

// chdirMu serializes the chdir dance below — multiple in-flight
// dialers in the same process otherwise stomp on each other's cwd.
var chdirMu sync.Mutex

// dialSock dials a Unix socket, with a long-path-relative chdir
// fallback that supervise attach uses. Some state-dirs are deeply
// nested and exceed the OS sun_path length; chdir-ing into the
// session dir lets us bind a relative path instead.
func dialSock(ctx context.Context, sockPath string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sockPath)
	if err == nil {
		return conn, nil
	}
	chdirMu.Lock()
	defer chdirMu.Unlock()
	cwd, gerr := os.Getwd()
	if gerr != nil {
		return nil, err
	}
	if cerr := os.Chdir(filepath.Dir(sockPath)); cerr != nil {
		return nil, err
	}
	defer os.Chdir(cwd)
	return d.DialContext(ctx, "unix", filepath.Base(sockPath))
}

// pumpUpstreamToWS reads attachwire frames from the supervisor and
// forwards MsgOutput as binary WS frames. MsgSize is dropped (the
// browser doesn't need to mirror remote size). Anything else is a
// protocol error.
func pumpUpstreamToWS(ctx context.Context, ws *websocket.Conn, r *bufio.Reader) error {
	for {
		typ, payload, err := attachwire.ReadFrame(r)
		if err != nil {
			return err
		}
		switch typ {
		case attachwire.MsgOutput:
			if werr := ws.Write(ctx, websocket.MessageBinary, payload); werr != nil {
				return werr
			}
		case attachwire.MsgSize:
			// ignore — browser sets its own size on resize
		case attachwire.MsgHello, attachwire.MsgInput:
			return fmt.Errorf("supervisor sent unexpected frame 0x%02x", typ)
		default:
			return fmt.Errorf("supervisor sent unknown frame 0x%02x", typ)
		}
	}
}

// pumpWSToUpstream reads WS frames and forwards them as attachwire
// frames. Binary → MsgInput; text {type:"resize",cols,rows} →
// MsgSize. Unknown text types are ignored (forward-compat).
func pumpWSToUpstream(ctx context.Context, ws *websocket.Conn, write func(byte, []byte) error) error {
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		switch typ {
		case websocket.MessageBinary:
			if len(data) == 0 {
				continue
			}
			if werr := write(attachwire.MsgInput, data); werr != nil {
				return werr
			}
		case websocket.MessageText:
			var msg attachMessage
			if jerr := json.Unmarshal(data, &msg); jerr != nil {
				continue
			}
			switch msg.Type {
			case "resize":
				if msg.Cols == 0 || msg.Rows == 0 {
					continue
				}
				payload, _ := json.Marshal(attachwire.Size{Cols: msg.Cols, Rows: msg.Rows})
				if werr := write(attachwire.MsgSize, payload); werr != nil {
					return werr
				}
			default:
				// silently ignore
			}
		}
	}
}

// handleAttachRaw is the CLI-facing HTTP/1.1 Upgrade pass-through to a
// session's rpc.sock /attach. Unlike handleAttach (which terminates a
// WebSocket and translates xterm.js JSON resize messages), this route
// is a pure TCP relay: client speaks attachwire end-to-end, the
// bridge just shovels bytes.
//
// Wire shape:
//
//	GET /sessions/{key-or-prefix}/attach-raw
//	  Upgrade: supervise-attach/1
//	  Connection: Upgrade
//
// {key-or-prefix} is resolved server-side via store.Resolve (full key
// or unique prefix).
//
// Responses:
//
//	101  Switching Protocols + Upgrade: supervise-attach/1
//	     → byte-stream of attachwire frames in both directions
//	404  no session matched
//	409  ambiguous prefix; body is a JSON {matches: [...]}
//	502  upstream rpc.sock dial / upgrade failed
//
// The bridge does not parse attachwire; it does an io.Copy in each
// direction. Either side closing tears the other down.
func (s *serveState) handleAttachRaw(w http.ResponseWriter, r *http.Request) {
	query := r.PathValue("key")
	if query == "" {
		http.Error(w, "missing session key", http.StatusBadRequest)
		return
	}

	state, err := s.store.Resolve(query)
	if err != nil {
		var ambig *shortid.AmbiguousIDError
		var notFound *shortid.IDNotFoundError
		var tooShort *shortid.IDTooShortError
		switch {
		case errors.As(err, &ambig):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   "ambiguous prefix",
				"query":   ambig.Query,
				"matches": ambig.Matches,
			})
		case errors.As(err, &notFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.As(err, &tooShort):
			http.Error(w, err.Error(), http.StatusBadRequest)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	sockPath := filepath.Join(s.stateDir, state.Supervisor.Key, "rpc.sock")

	// Step 1: dial rpc.sock and complete the upstream Upgrade. We
	// reuse dialAttach which already performs the HTTP/1.1 Upgrade
	// against rpc.sock and returns a conn ready for attachwire
	// frames. If this fails, we haven't hijacked yet — return a
	// regular 502.
	upstream, err := dialAttach(r.Context(), sockPath)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Step 2: hijack the client conn and send our own 101.
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.conn.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, clientBufrw, err := hj.Hijack()
	if err != nil {
		upstream.conn.Close()
		// can't write a normal response after hijack failure either
		return
	}
	defer clientConn.Close()
	defer upstream.conn.Close()

	if _, err := clientBufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: supervise-attach/1\r\n" +
		"Connection: Upgrade\r\n\r\n"); err != nil {
		return
	}
	if err := clientBufrw.Flush(); err != nil {
		return
	}

	// Step 3: io.Copy in both directions. The upstream-side bufio
	// reader (upstream.reader) may already hold buffered bytes that
	// arrived between the upstream 101 and now (typically none, but
	// must be drained first). Same on the client side: clientBufrw's
	// reader may hold pipelined bytes from the request.
	relay(clientConn, clientBufrw.Reader, upstream.conn, upstream.reader)
}

// relay shovels bytes between two conns whose buffered readers may
// already hold prefix bytes from the protocol-switch handshake. It
// returns when either copy direction errors or closes.
func relay(a net.Conn, ar *bufio.Reader, b net.Conn, br *bufio.Reader) {
	done := make(chan struct{}, 2)
	halfClose := func(c net.Conn) {
		if cw, ok := c.(closeWriteConn); ok {
			_ = cw.CloseWrite()
		}
	}
	go func() {
		_, _ = io.Copy(b, ar) // a → b (drain ar buffered, then keep reading from a)
		halfClose(b)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(a, br) // b → a
		halfClose(a)
		done <- struct{}{}
	}()
	<-done
	// Closing one side typically tears down the other; we still
	// wait for both copy goroutines for cleanliness.
	<-done
}

// closeWriteConn is the half-close subset of net.TCPConn / *net.UnixConn.
// We use CloseWrite to send EOF after one direction's copy finishes
// without yanking the read side out from under the other goroutine.
// Hijacked HTTP connections from net/http are *net.TCPConn under the
// hood; the rpc.sock conn is *net.UnixConn. Both implement this.
type closeWriteConn interface {
	CloseWrite() error
}

