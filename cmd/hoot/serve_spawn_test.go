package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hayeah/hootty"
)

// TestCreateSessionDefaultShell asserts that POST /sessions with an
// empty body falls back to the server's default shell — the path
// `hoot @host` takes when it sends an empty createSessionReq. The
// resolved shell ends up as argv[0] in the spawned session, which the
// fake hoot executable copies into state.json so we can read it back.
func TestCreateSessionDefaultShell(t *testing.T) {
	stateDir := shortTempDir(t)
	t.Setenv("SHELL", "/bin/serversideshell")
	withFakeHootExecutable(t)

	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions", srv.createSession)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	body := bytes.NewBufferString(`{"key":"defaultsh"}`)
	resp, err := http.Post(httpSrv.URL+"/sessions", "application/json", body)
	if err != nil {
		t.Fatalf("POST /sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	raw, err := os.ReadFile(filepath.Join(stateDir, "defaultsh", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var st session.StateFile
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(st.Session.Argv) == 0 || st.Session.Argv[0] != "/bin/serversideshell" {
		t.Fatalf("argv[0] = %v, want /bin/serversideshell (server's $SHELL)", st.Session.Argv)
	}
}

// TestCreateSessionExplicitCmdStillParses guards against the
// regression-friendly path: a non-empty Cmd field still goes through
// splitCmd, not the default-shell branch.
func TestCreateSessionExplicitCmdStillParses(t *testing.T) {
	stateDir := shortTempDir(t)
	t.Setenv("SHELL", "/bin/should-not-be-used")
	withFakeHootExecutable(t)

	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions", srv.createSession)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	body := bytes.NewBufferString(`{"key":"explicitcmd","cmd":"/usr/bin/echo hi"}`)
	resp, err := http.Post(httpSrv.URL+"/sessions", "application/json", body)
	if err != nil {
		t.Fatalf("POST /sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	raw, err := os.ReadFile(filepath.Join(stateDir, "explicitcmd", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var st session.StateFile
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(st.Session.Argv) == 0 || st.Session.Argv[0] != "/usr/bin/echo" {
		t.Fatalf("argv[0] = %v, want /usr/bin/echo (Cmd splitting)", st.Session.Argv)
	}
}
