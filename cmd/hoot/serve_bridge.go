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
	"net/http/httputil"
	"os"
	"path/filepath"
	"sync"

	"github.com/coder/websocket"

	"github.com/hayeah/hootty/internal/attachwire"
	"github.com/hayeah/hootty/internal/shortid"
)

// handleSessionProxy is the catch-all reverse proxy mounted at
// /sessions/{key}/{path...}. It resolves the key prefix locally,
// dials <state-dir>/<full-key>/rpc.sock, and forwards the request to
// the session library's mux at the upstream path captured by the
// {path...} wildcard.
//
// Why one handler covers everything:
//
//   - JSON verbs (/signal, /clone, /pty/input, /attachments,
//     /attachments/{id}, /state) — plain request/response, ReverseProxy
//     copies headers + body in both directions.
//   - SSE (/events) — FlushInterval: -1 forces ReverseProxy to flush
//     each Write through to the client (no buffering).
//   - HTTP/1.1 Upgrade (CLI /attach) — since Go 1.20, ReverseProxy
//     transparently handles the 101 + bidirectional byte stream when
//     Upgrade headers are present; no hijack/relay required here.
//
// Specific routes (handleSession, handleResolve, handleAttach for the
// browser WS bridge) win in http.ServeMux, so this catch-all only
// services the verbs that have an upstream rpc.sock equivalent.
func (s *serveState) handleSessionProxy(w http.ResponseWriter, r *http.Request) {
	s.proxySession(w, r, r.PathValue("path"))
}

// handleAttachRawProxy is the CLI-facing entry point that survives the
// rename. It routes /sessions/{key}/attach-raw at the serve-side to
// /attach on the session-side rpc.sock — keeping the CLI URL stable
// while letting the catch-all handle every other verb on its own path.
//
// Why a separate registration: the browser's /sessions/{key}/attach
// goes through handleAttach (real WS↔attachwire translation) and the
// CLI's hoot-attach/1 Upgrade can't share that URL — websocket.Accept
// will reject the request. So serve-side keeps two distinct paths but
// the proxy unifies them upstream.
func (s *serveState) handleAttachRawProxy(w http.ResponseWriter, r *http.Request) {
	s.proxySession(w, r, "attach")
}

func (s *serveState) proxySession(w http.ResponseWriter, r *http.Request, upstreamPath string) {
	query := r.PathValue("key")
	if query == "" {
		http.Error(w, "missing session key", http.StatusBadRequest)
		return
	}
	state, err := s.store.Resolve(query)
	if err != nil {
		writeResolveError(w, err)
		return
	}
	sockPath := filepath.Join(s.stateDir, state.Session.Key, "rpc.sock")

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "unix"
			pr.Out.URL.Path = "/" + upstreamPath
			// pr.Out.URL.RawQuery is already copied from pr.In by
			// httputil; leave it alone so ?paste=on etc. survive.
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialSock(ctx, sockPath)
			},
		},
		// SSE + any other streaming response: flush each Write so
		// browser clients see events in real time. Required for the
		// /events route folded into this proxy.
		FlushInterval: -1,
	}
	rp.ServeHTTP(w, r)
}

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

func (s *serveState) handleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := r.PathValue("key")
	if query == "" {
		http.Error(w, "missing session key", http.StatusBadRequest)
		return
	}
	state, err := s.store.Resolve(query)
	if err != nil {
		writeResolveError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"key": state.Session.Key})
}

func writeResolveError(w http.ResponseWriter, err error) {
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
// session's per-session /attach (HTTP/1.1 Upgrade →
// attachwire framed protocol) on rpc.sock.
//
// Wire mapping (browser ↔ bridge ↔ session):
//
//	browser binary  ←  attachwire MsgOutput   (PTY bytes server→client)
//	browser binary  →  attachwire MsgInput    (raw input client→server)
//	browser text {type:"resize",cols,rows}  →  attachwire MsgSize
//	attachwire MsgSize (server→client)       →  ignored (browser
//	                                             doesn't render
//	                                             remote-size mirror)
//
// Initial attachwire Hello uses 80x24; the browser's xterm-style
// addon will fire a resize on mount which reaches the session
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

	// Step 2: dial rpc.sock and perform the hoot-attach/1
	// HTTP Upgrade, then send Hello.
	upstream, err := dialAttach(r.Context(), sockPath)
	if err != nil {
		_ = wsConn.Close(websocket.StatusInternalError, "upstream dial: "+err.Error())
		return
	}
	defer upstream.conn.Close()

	helloPayload, _ := json.Marshal(attachwire.Hello{
		Size: attachwire.PTYSize{Cols: 80, Rows: 24},
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
// the hoot-attach/1 protocol. Returns a connection ready for
// attachwire frames in both directions.
func dialAttach(ctx context.Context, sockPath string) (*upstreamConn, error) {
	conn, err := dialSock(ctx, sockPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", sockPath, err)
	}
	bufrw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

	req, err := http.NewRequest(http.MethodGet, "http://hoot/attach", nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Upgrade", "hoot-attach/1")
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
		return nil, fmt.Errorf("session refused upgrade: %s\n%s", resp.Status, string(body))
	}
	resp.Body.Close()
	return &upstreamConn{conn: conn, reader: bufrw.Reader}, nil
}

// chdirMu serializes the chdir dance below — multiple in-flight
// dialers in the same process otherwise stomp on each other's cwd.
var chdirMu sync.Mutex

// dialSock dials a Unix socket, with a long-path-relative chdir
// fallback that hoot attach uses. Some state-dirs are deeply
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

// pumpUpstreamToWS reads attachwire frames from the session and
// forwards VT-byte payloads (Output + the two snapshot frames) to
// the browser as binary WS frames. MsgSize is dropped (the browser
// sets its own size on resize). Ping/Pong are heartbeat frames the
// CLI client uses; the browser's WS layer has its own keepalive, so
// we drop them. Anything else is a protocol error.
func pumpUpstreamToWS(ctx context.Context, ws *websocket.Conn, r *bufio.Reader) error {
	for {
		typ, payload, err := attachwire.ReadFrame(r)
		if err != nil {
			return err
		}
		switch typ {
		case attachwire.MsgOutput,
			attachwire.MsgSnapshotScrollback,
			attachwire.MsgSnapshotScreen:
			// All three carry VT bytes that xterm.js renders the same
			// way. The CLI client clears the viewport before
			// MsgSnapshotScreen; xterm.js handles the formatter's
			// embedded cursor-home + erase-display sequences directly,
			// so no special-casing is needed here.
			if werr := ws.Write(ctx, websocket.MessageBinary, payload); werr != nil {
				return werr
			}
		case attachwire.MsgSize:
			// ignore — browser sets its own size on resize
		case attachwire.MsgPing, attachwire.MsgPong:
			// ignore — WebSocket has its own keepalive
		case attachwire.MsgHello, attachwire.MsgInput:
			return fmt.Errorf("session sent unexpected frame 0x%02x", typ)
		default:
			return fmt.Errorf("session sent unknown frame 0x%02x", typ)
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

