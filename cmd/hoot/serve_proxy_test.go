package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hayeah/hootty"
	"github.com/hayeah/hootty/internal/attachwire"
)

// startFakeRPCSock binds a unix socket at <stateDir>/<key>/rpc.sock and
// serves the given mux on it. Returns a cleanup that closes the listener.
// Mirrors what cmd/hoot/__session would do — but with a hand-rolled mux
// so each test can assert what arrived upstream.
func startFakeRPCSock(t *testing.T, stateDir, key string, mux http.Handler) func() {
	t.Helper()
	sessionDir := filepath.Join(stateDir, key)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	sf := session.StateFile{Session: session.SessionState{Key: key}}
	if err := writeJSON(filepath.Join(sessionDir, "state.json"), sf); err != nil {
		t.Fatalf("write state: %v", err)
	}
	sockPath := filepath.Join(sessionDir, "rpc.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen rpc.sock: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = ln.Close()
	}
}

// newProxyMux returns an http.ServeMux with the session catch-all proxy
// registered at /sessions/{key}/{path...}, plus the attach-raw rewrite.
func newProxyMux(srv *serveState) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/{key}/{path...}", srv.handleSessionProxy)
	mux.HandleFunc("/sessions/{key}/attach-raw", srv.handleAttachRawProxy)
	return mux
}

// TestSessionProxyJSONVerb sanity-checks the catch-all over a unix
// socket: a POST to /sessions/{key}/signal arrives upstream as
// POST /signal with the body intact, and the upstream response makes
// it back unmolested.
func TestSessionProxyJSONVerb(t *testing.T) {
	stateDir := shortTempDir(t)
	key := "abc123"

	var (
		gotMethod string
		gotPath   string
		gotBody   []byte
		gotCT     string
	)
	upstream := http.NewServeMux()
	upstream.HandleFunc("/signal", func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNoContent)
	})
	defer startFakeRPCSock(t, stateDir, key, upstream)()

	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	httpSrv := httptest.NewServer(newProxyMux(srv))
	defer httpSrv.Close()

	req, _ := http.NewRequest(http.MethodPost,
		httpSrv.URL+"/sessions/"+key+"/signal",
		strings.NewReader(`{"signal":"TERM"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if gotMethod != http.MethodPost || gotPath != "/signal" {
		t.Fatalf("upstream got %s %s, want POST /signal", gotMethod, gotPath)
	}
	if string(gotBody) != `{"signal":"TERM"}` {
		t.Fatalf("upstream body = %q, want %q", gotBody, `{"signal":"TERM"}`)
	}
	if gotCT != "application/json" {
		t.Fatalf("upstream Content-Type = %q, want application/json", gotCT)
	}
}

// TestSessionProxyPreservesQueryString verifies that a query string on
// the inbound request (e.g. ?paste=on) survives the rewrite — handleInput
// today carries this through, and the proxy must too.
func TestSessionProxyPreservesQueryString(t *testing.T) {
	stateDir := shortTempDir(t)
	key := "abc123"

	var gotQuery string
	upstream := http.NewServeMux()
	upstream.HandleFunc("/pty/input", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusNoContent)
	})
	defer startFakeRPCSock(t, stateDir, key, upstream)()

	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	httpSrv := httptest.NewServer(newProxyMux(srv))
	defer httpSrv.Close()

	resp, err := http.Post(
		httpSrv.URL+"/sessions/"+key+"/pty/input?paste=on",
		"application/octet-stream",
		strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if gotQuery != "paste=on" {
		t.Fatalf("upstream RawQuery = %q, want paste=on", gotQuery)
	}
}

// TestSessionProxySSE verifies FlushInterval: -1 actually flushes the
// SSE stream chunk-by-chunk through the proxy. We write three events
// upstream with explicit Flush()es and read each one back over the
// proxy connection within a tight deadline.
func TestSessionProxySSE(t *testing.T) {
	stateDir := shortTempDir(t)
	key := "abc123"

	upstream := http.NewServeMux()
	upstream.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, line := range []string{"data: one\n\n", "data: two\n\n", "data: three\n\n"} {
			_, _ = io.WriteString(w, line)
			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	})
	defer startFakeRPCSock(t, stateDir, key, upstream)()

	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	httpSrv := httptest.NewServer(newProxyMux(srv))
	defer httpSrv.Close()

	resp, err := http.Get(httpSrv.URL + "/sessions/" + key + "/events")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	r := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(2 * time.Second)
	var events []string
	for len(events) < 3 && time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			break
		}
		if rest, ok := strings.CutPrefix(line, "data: "); ok {
			events = append(events, strings.TrimSpace(rest))
		}
	}
	if len(events) != 3 || events[0] != "one" || events[2] != "three" {
		t.Fatalf("events = %v, want [one two three]", events)
	}
}

// TestSessionProxyResolvesPrefix confirms the catch-all uses store.Resolve
// (so unique prefixes work) and that a too-short prefix returns 409 with
// a JSON matches list.
func TestSessionProxyResolvesPrefix(t *testing.T) {
	stateDir := shortTempDir(t)
	for _, key := range []string{"abc111", "abc222"} {
		dir := filepath.Join(stateDir, key)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		sf := session.StateFile{Session: session.SessionState{Key: key}}
		if err := writeJSON(filepath.Join(dir, "state.json"), sf); err != nil {
			t.Fatalf("write state: %v", err)
		}
	}

	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	httpSrv := httptest.NewServer(newProxyMux(srv))
	defer httpSrv.Close()

	resp, err := http.Post(
		httpSrv.URL+"/sessions/abc/signal",
		"application/json",
		strings.NewReader(`{"signal":"TERM"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("ambiguous prefix status = %d, want 409", resp.StatusCode)
	}
}

// TestSessionProxyUnknownKey checks 404 surfaces from store.Resolve
// before any upstream dial happens.
func TestSessionProxyUnknownKey(t *testing.T) {
	stateDir := shortTempDir(t)
	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	httpSrv := httptest.NewServer(newProxyMux(srv))
	defer httpSrv.Close()

	resp, err := http.Post(
		httpSrv.URL+"/sessions/zzznotreal/signal",
		"application/json",
		strings.NewReader(`{"signal":"TERM"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestSessionProxyUpstreamDialFails verifies that when state.json
// resolves but rpc.sock isn't bound, the proxy surfaces 502 (matches
// today's serve_bridge behavior).
func TestSessionProxyUpstreamDialFails(t *testing.T) {
	stateDir := shortTempDir(t)
	key := "abc123"
	dir := filepath.Join(stateDir, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sf := session.StateFile{Session: session.SessionState{Key: key}}
	if err := writeJSON(filepath.Join(dir, "state.json"), sf); err != nil {
		t.Fatalf("write state: %v", err)
	}
	// no rpc.sock created — dial will fail

	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	httpSrv := httptest.NewServer(newProxyMux(srv))
	defer httpSrv.Close()

	resp, err := http.Post(
		httpSrv.URL+"/sessions/"+key+"/signal",
		"application/json",
		strings.NewReader(`{"signal":"TERM"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

// TestSessionProxyAttachRawUpgrade exercises the HTTP/1.1 Upgrade path
// through the proxy: serve-side /sessions/{key}/attach-raw routes to
// upstream /attach, ReverseProxy carries the 101 + bidirectional byte
// stream. Tests Hello + Input → echoed Output.
func TestSessionProxyAttachRawUpgrade(t *testing.T) {
	stateDir := shortTempDir(t)
	key := "abc123"

	upstream := http.NewServeMux()
	upstream.HandleFunc("/attach", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Upgrade"); got != "hoot-attach/1" {
			http.Error(w, "bad upgrade: "+got, http.StatusBadRequest)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: hoot-attach/1\r\nConnection: Upgrade\r\n\r\n")
		_ = bufrw.Flush()

		r2 := bufio.NewReader(conn)
		for {
			typ, payload, err := attachwire.ReadFrame(r2)
			if err != nil {
				if !errors.Is(err, io.EOF) {
					t.Logf("upstream read frame: %v", err)
				}
				return
			}
			switch typ {
			case attachwire.MsgHello:
				// discard
			case attachwire.MsgInput:
				if werr := attachwire.WriteFrame(conn, attachwire.MsgOutput, payload); werr != nil {
					return
				}
			}
		}
	})
	defer startFakeRPCSock(t, stateDir, key, upstream)()

	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	httpSrv := httptest.NewServer(newProxyMux(srv))
	defer httpSrv.Close()

	hostport := httpSrv.Listener.Addr().String()
	conn, err := net.Dial("tcp", hostport)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	bufrw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	req, _ := http.NewRequest(http.MethodGet, "http://"+hostport+"/sessions/"+key+"/attach-raw", nil)
	req.Header.Set("Upgrade", "hoot-attach/1")
	req.Header.Set("Connection", "Upgrade")
	if err := req.Write(bufrw); err != nil {
		t.Fatalf("write upgrade: %v", err)
	}
	if err := bufrw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	resp, err := http.ReadResponse(bufrw.Reader, req)
	if err != nil {
		t.Fatalf("read upgrade response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("got status %d, want 101", resp.StatusCode)
	}
	resp.Body.Close()

	// Send Hello + Input
	if err := attachwire.WriteFrame(conn, attachwire.MsgHello, []byte("{}")); err != nil {
		t.Fatalf("write Hello: %v", err)
	}
	want := []byte("ping")
	if err := attachwire.WriteFrame(conn, attachwire.MsgInput, want); err != nil {
		t.Fatalf("write Input: %v", err)
	}
	r := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	typ, payload, err := attachwire.ReadFrame(r)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if typ != attachwire.MsgOutput || string(payload) != string(want) {
		t.Fatalf("got typ=0x%02x payload=%q, want Output %q", typ, payload, want)
	}
}
