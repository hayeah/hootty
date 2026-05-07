package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hayeah/hootty"
)

// fakeInputServer stands up a unix-socket http server with a single
// /pty/input handler that records the request and returns whatever
// status the test asks for. Reused by both writeLocal (direct unix
// dial) and the serve proxy tests (the upstream rpc.sock).
type fakeInputServer struct {
	t          *testing.T
	sockPath   string
	listener   net.Listener
	server     *http.Server
	mu         sync.Mutex
	lastBody   []byte
	lastQuery  string
	lastMethod string
	gotCount   int32

	// configurable response
	respStatus int
	respBody   string
}

func newFakeInputServer(t *testing.T, dir string) *fakeInputServer {
	t.Helper()
	sock := filepath.Join(dir, "rpc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	f := &fakeInputServer{
		t:          t,
		sockPath:   sock,
		listener:   ln,
		respStatus: http.StatusNoContent,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/pty/input", f.handle)
	f.server = &http.Server{Handler: mux}
	go func() {
		_ = f.server.Serve(ln)
	}()
	t.Cleanup(func() { _ = f.server.Close() })
	return f
}

func (f *fakeInputServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.lastBody = body
	f.lastQuery = r.URL.RawQuery
	f.lastMethod = r.Method
	atomic.AddInt32(&f.gotCount, 1)
	respStatus := f.respStatus
	respBody := f.respBody
	f.mu.Unlock()
	if respBody != "" {
		w.WriteHeader(respStatus)
		_, _ = io.WriteString(w, respBody)
		return
	}
	w.WriteHeader(respStatus)
}

// writeFakeStateFile writes the minimal state.json the store's
// Resolve() call needs to satisfy a key lookup.
func writeFakeStateFile(t *testing.T, stateDir, key string) {
	t.Helper()
	dir := filepath.Join(stateDir, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sf := session.StateFile{Session: session.SessionState{Key: key}}
	if err := writeJSON(filepath.Join(dir, "state.json"), sf); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
}

func TestWriteLocal_HappyPath(t *testing.T) {
	stateDir := shortTempDir(t)
	key := "abc123"
	writeFakeStateFile(t, stateDir, key)
	fake := newFakeInputServer(t, filepath.Join(stateDir, key))
	_ = fake // referenced via lookup below

	if err := writeLocal(stateDir, key, []byte("hello\r"), false); err != nil {
		t.Fatalf("writeLocal: %v", err)
	}
	if got := fake.lastBody; !bytes.Equal(got, []byte("hello\r")) {
		t.Fatalf("body: got %q want %q", got, "hello\r")
	}
	if fake.lastQuery != "" {
		t.Fatalf("query: got %q want empty", fake.lastQuery)
	}
}

func TestWriteLocal_PasteAddsQuery(t *testing.T) {
	stateDir := shortTempDir(t)
	key := "abc456"
	writeFakeStateFile(t, stateDir, key)
	fake := newFakeInputServer(t, filepath.Join(stateDir, key))

	if err := writeLocal(stateDir, key, []byte("body"), true); err != nil {
		t.Fatalf("writeLocal: %v", err)
	}
	if fake.lastQuery != "paste=on" {
		t.Fatalf("query: got %q want paste=on", fake.lastQuery)
	}
}

func TestWriteLocal_409Conflict_MapsToExit2(t *testing.T) {
	stateDir := shortTempDir(t)
	key := "conf789"
	writeFakeStateFile(t, stateDir, key)
	fake := newFakeInputServer(t, filepath.Join(stateDir, key))
	fake.respStatus = http.StatusConflict
	fake.respBody = "bracketed paste not enabled on receiver\n"

	err := writeLocal(stateDir, key, []byte("body"), true)
	if err == nil {
		t.Fatalf("expected error")
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("err type: %T (%v)", err, err)
	}
	if ee.code != 2 {
		t.Fatalf("exit code: got %d want 2", ee.code)
	}
	if !strings.Contains(ee.err.Error(), "bracketed paste not enabled") {
		t.Fatalf("err msg: %q", ee.err)
	}
}

func TestWriteLocal_UnknownSession_MapsToExit3(t *testing.T) {
	stateDir := shortTempDir(t)
	err := writeLocal(stateDir, "nonexistent", []byte("body"), false)
	if err == nil {
		t.Fatalf("expected error")
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("err type: %T (%v)", err, err)
	}
	if ee.code != 3 {
		t.Fatalf("exit code: got %d want 3", ee.code)
	}
}

// TestServeHandleInput_Proxies stands up the serve mux pointing at
// a fake upstream rpc.sock and confirms `POST /sessions/{key}/input`
// forwards body + query verbatim and mirrors the upstream status.
func TestServeHandleInput_Proxies(t *testing.T) {
	stateDir := shortTempDir(t)
	key := "proxy123"
	writeFakeStateFile(t, stateDir, key)
	fake := newFakeInputServer(t, filepath.Join(stateDir, key))

	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/{key}/input", srv.handleInput)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	resp, err := http.Post(
		httpSrv.URL+"/sessions/"+key+"/input?paste=on",
		"application/octet-stream",
		bytes.NewReader([]byte("ping")),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status: got %d want 204", resp.StatusCode)
	}
	if got := fake.lastBody; !bytes.Equal(got, []byte("ping")) {
		t.Fatalf("upstream body: got %q want %q", got, "ping")
	}
	if fake.lastQuery != "paste=on" {
		t.Fatalf("upstream query: got %q want paste=on", fake.lastQuery)
	}
}

// TestServeHandleInput_UnknownSession returns 404 (the resolve error
// path).
func TestServeHandleInput_UnknownSession(t *testing.T) {
	stateDir := shortTempDir(t)
	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/{key}/input", srv.handleInput)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	resp, err := http.Post(
		httpSrv.URL+"/sessions/nope/input",
		"application/octet-stream",
		strings.NewReader("body"),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", resp.StatusCode)
	}
}

// TestServeHandleInput_BodyTooLarge — the proxy enforces the same
// 1 MiB cap before dialing the upstream so a runaway pipe doesn't
// even get connect()ed to the supervisor.
func TestServeHandleInput_BodyTooLarge(t *testing.T) {
	stateDir := shortTempDir(t)
	key := "bigbody"
	writeFakeStateFile(t, stateDir, key)
	_ = newFakeInputServer(t, filepath.Join(stateDir, key))

	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/{key}/input", srv.handleInput)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	big := bytes.Repeat([]byte{'a'}, (1<<20)+10)
	resp, err := http.Post(
		httpSrv.URL+"/sessions/"+key+"/input",
		"application/octet-stream",
		bytes.NewReader(big),
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d want 413", resp.StatusCode)
	}
}

// TestResolveWriteBody covers source precedence + the no-input error.
func TestResolveWriteBody(t *testing.T) {
	dir := shortTempDir(t)
	filePath := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(filePath, []byte("FILECONTENT"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	t.Run("file_wins_over_argv", func(t *testing.T) {
		got, err := resolveWriteBody(filePath, []string{`hi\r`})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if string(got) != "FILECONTENT" {
			t.Fatalf("got %q want FILECONTENT", got)
		}
	})
	t.Run("argv_when_no_file", func(t *testing.T) {
		got, err := resolveWriteBody("", []string{`hi\r`})
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if string(got) != "hi\r" {
			t.Fatalf("got %q want %q", got, "hi\r")
		}
	})
	t.Run("argv_parse_error_propagates", func(t *testing.T) {
		_, err := resolveWriteBody("", []string{`bad\efoo`})
		if err == nil {
			t.Fatalf("expected parse error")
		}
		if !strings.Contains(err.Error(), "\\x1b") {
			t.Fatalf("expected ESC hint, got %q", err)
		}
	})
}
