package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hayeah/hootty"
	"github.com/hayeah/hootty/internal/attachwire"
)

// TestHandleAttachRawRoundTrip stands up an in-process serve mux
// pointing at a state-dir with a single fake session whose rpc.sock
// runs a tiny attach handler. A test client dials over TCP, does
// the hoot-attach/1 Upgrade, sends Hello + Input, reads back
// expected Output frames, and detaches by closing.
func TestHandleAttachRawRoundTrip(t *testing.T) {
	stateDir := shortTempDir(t)
	key := "abc123"
	sessionDir := filepath.Join(stateDir, key)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session: %v", err)
	}
	// Resolve() also reads state.json indirectly via Load(). Write a
	// minimal one.
	stateFile := filepath.Join(sessionDir, "state.json")
	sf := session.StateFile{Session: session.SessionState{Key: key}}
	if err := writeJSON(stateFile, sf); err != nil {
		t.Fatalf("write state: %v", err)
	}

	// Fake rpc.sock: accepts a single conn, performs the
	// hoot-attach/1 101, then echoes attachwire frames back to
	// the client (any Input → Output with the same payload). Also
	// reads and discards the Hello.
	sockPath := filepath.Join(sessionDir, "rpc.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen rpc.sock: %v", err)
	}
	defer ln.Close()
	go runFakeSessionAttach(t, ln)

	// serveState wired up like cmd/serve.go does.
	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/{key}/attach-raw", srv.handleAttachRaw)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	// Client: dial TCP, write the hoot-attach/1 upgrade, expect 101.
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

	// Send Hello + Input ("ping").
	helloPayload, _ := json.Marshal(attachwire.Hello{Cols: 80, Rows: 24})
	if err := attachwire.WriteFrame(conn, attachwire.MsgHello, helloPayload); err != nil {
		t.Fatalf("write Hello: %v", err)
	}
	want := []byte("ping")
	if err := attachwire.WriteFrame(conn, attachwire.MsgInput, want); err != nil {
		t.Fatalf("write Input: %v", err)
	}

	// Read one Output frame; the fake session echoes our Input.
	r := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	typ, payload, err := attachwire.ReadFrame(r)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if typ != attachwire.MsgOutput {
		t.Fatalf("got frame typ 0x%02x, want Output 0x02", typ)
	}
	if string(payload) != string(want) {
		t.Fatalf("got payload %q, want %q", payload, want)
	}
}

// TestHandleAttachRawNotFound checks that an unknown short-id returns
// a 404 rather than upgrading.
func TestHandleAttachRawNotFound(t *testing.T) {
	stateDir := shortTempDir(t)
	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/{key}/attach-raw", srv.handleAttachRaw)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	req, _ := http.NewRequest(http.MethodGet, httpSrv.URL+"/sessions/zzznotreal/attach-raw", nil)
	req.Header.Set("Upgrade", "hoot-attach/1")
	req.Header.Set("Connection", "Upgrade")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("got %d (%s), want 404", resp.StatusCode, string(body))
	}
}

// TestHandleAttachRawAmbiguous checks that a too-short prefix that
// matches multiple sessions returns 409 with a list of matches.
func TestHandleAttachRawAmbiguous(t *testing.T) {
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
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/{key}/attach-raw", srv.handleAttachRaw)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	req, _ := http.NewRequest(http.MethodGet, httpSrv.URL+"/sessions/abc/attach-raw", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("got %d, want 409", resp.StatusCode)
	}
	var body struct {
		Error   string   `json:"error"`
		Query   string   `json:"query"`
		Matches []string `json:"matches"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Matches) != 2 {
		t.Fatalf("got matches=%v, want 2 entries", body.Matches)
	}
}

func TestListenServeBindUnixRoundTrip(t *testing.T) {
	sockPath := filepath.Join(shortTempDir(t), "serve.sock")
	ln, cleanup, isUnix, err := listenServeBind("unix:" + sockPath)
	if err != nil {
		t.Fatalf("listenServeBind: %v", err)
	}
	if !isUnix {
		t.Fatalf("listenServeBind isUnix = false, want true")
	}
	defer cleanup()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	server := &http.Server{Handler: mux}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(ln)
	}()

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
	}}
	resp, err := client.Get("http://unix/healthz")
	if err != nil {
		t.Fatalf("GET /healthz over unix socket: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("response = %d %q, want 200 ok", resp.StatusCode, body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-serveDone:
		if err != http.ErrServerClosed {
			t.Fatalf("Serve err = %v, want ErrServerClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("server did not shut down")
	}
	cleanup()
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Fatalf("socket still exists after cleanup: %v", err)
	}
}

// runFakeSessionAttach pretends to be the session's
// /attach handler on a unix socket. It accepts one connection,
// performs the hoot-attach/1 upgrade, reads the Hello
// (discards it), and then echoes any Input frame back as an Output
// frame until EOF.
func runFakeSessionAttach(t *testing.T, ln net.Listener) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	bufrw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	req, err := http.ReadRequest(bufrw.Reader)
	if err != nil {
		t.Logf("fake session: read req: %v", err)
		return
	}
	if req.Header.Get("Upgrade") != "hoot-attach/1" {
		t.Errorf("fake session: got Upgrade=%q, want hoot-attach/1", req.Header.Get("Upgrade"))
		return
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: hoot-attach/1\r\n" +
		"Connection: Upgrade\r\n\r\n"
	if _, err := bufrw.WriteString(resp); err != nil {
		return
	}
	if err := bufrw.Flush(); err != nil {
		return
	}

	// Read frames forever; echo Input as Output.
	r := bufio.NewReader(conn)
	for {
		typ, payload, err := attachwire.ReadFrame(r)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Logf("fake session: read frame: %v", err)
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
}

// writeJSON writes any JSON-encodable value to a file.
func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(v)
}

// shortTempDir returns a temp dir under /tmp short enough to bind a
// unix socket inside (macOS sun_path is 104 bytes; t.TempDir() paths
// often exceed that).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sv-srvtest")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// keep these refs so unused-import linters don't bite when we narrow
// the test set
var (
	_ = sync.Mutex{}
	_ = context.Background
)
