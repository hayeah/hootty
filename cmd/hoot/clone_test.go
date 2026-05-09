package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hayeah/hootty"
)

func TestCloneSessionPreservesMetadataAndEnvOverride(t *testing.T) {
	stateDir := shortTempDir(t)
	sourceKey := "src123"
	sourceDir := filepath.Join(stateDir, sourceKey)
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	source := session.StateFile{Session: session.SessionState{
		Key:  sourceKey,
		Argv: []string{"fakecmd", "arg1"},
		CWD:  filepath.Join(stateDir, "work"),
	}}
	if err := os.MkdirAll(source.Session.CWD, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	if err := writeJSON(filepath.Join(sourceDir, "state.json"), source); err != nil {
		t.Fatalf("write source state: %v", err)
	}

	withFakeHootExecutable(t)
	resp, err := cloneSession(session.NewStore(stateDir), stateDir, sourceKey, "dst123", map[string]string{
		"CLONE_VAR": "visible",
	})
	if err != nil {
		t.Fatalf("cloneSession: %v", err)
	}
	if resp.Session.Key != "dst123" {
		t.Fatalf("key = %q, want dst123", resp.Session.Key)
	}
	if strings.Join(resp.Session.Argv, " ") != "fakecmd arg1" {
		t.Fatalf("argv = %v, want fakecmd arg1", resp.Session.Argv)
	}
	if resp.Session.CWD != source.Session.CWD {
		t.Fatalf("cwd = %q, want %q", resp.Session.CWD, source.Session.CWD)
	}
	var state map[string]string
	if err := json.Unmarshal(resp.State, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state["clone_var"] != "visible" {
		t.Fatalf("clone_var = %q, want visible", state["clone_var"])
	}
}

func TestCloneSessionRejectsCorruptState(t *testing.T) {
	stateDir := shortTempDir(t)
	key := "src123"
	dir := filepath.Join(stateDir, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := writeJSON(filepath.Join(dir, "state.json"), session.StateFile{
		Session: session.SessionState{Key: key},
	}); err != nil {
		t.Fatalf("write source state: %v", err)
	}

	_, err := cloneSession(session.NewStore(stateDir), stateDir, key, "dst123", nil)
	if err == nil || !strings.Contains(err.Error(), "corrupt argv/cwd") {
		t.Fatalf("err = %v, want corrupt argv/cwd", err)
	}
}

func TestCmdCloneRemote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sessions/src/clone" {
			t.Fatalf("request = %s %s, want POST /sessions/src/clone", r.Method, r.URL.Path)
		}
		var body cloneSessionReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Key != "dst123" || body.Env["CLONE_VAR"] != "visible" {
			t.Fatalf("body = %+v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session": map[string]any{"key": "dst123", "argv": []string{"fakecmd"}, "cwd": "/work"},
			"state":   map[string]any{},
			"alive":   true,
		})
	}))
	defer server.Close()

	out := captureStdout(t, func() {
		if err := cmdClone([]string{"--remote", "http://" + server.Listener.Addr().String(), "--strict", "--key", "dst123", "--env", "CLONE_VAR=visible", "src"}); err != nil {
			t.Fatalf("cmdClone: %v", err)
		}
	})
	if strings.TrimSpace(out) != "dst123" {
		t.Fatalf("output = %q, want dst123", out)
	}
}

func withFakeHootExecutable(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-hoot")
	script := `#!/bin/sh
if [ "$1" != "__session" ]; then
  echo "unexpected command: $1" >&2
  exit 2
fi
shift
no_history=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --state-dir) state_dir="$2"; shift 2 ;;
    --key) key="$2"; shift 2 ;;
    --cwd) cwd="$2"; shift 2 ;;
    --no-history) no_history=true; shift ;;
    --) shift; break ;;
    *) echo "unexpected arg: $1" >&2; exit 2 ;;
  esac
done
mkdir -p "$state_dir/$key"
cat > "$state_dir/$key/state.json" <<EOF
{"session":{"key":"$key","argv":["$1","$2"],"cwd":"$cwd","no_history":$no_history},"state":{"clone_var":"${CLONE_VAR:-}","run_file_var":"${RUN_FILE_VAR:-}"}}
EOF
: > "$state_dir/$key/rpc.sock"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake hoot: %v", err)
	}
	old := hootExecutable
	hootExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { hootExecutable = old })
}
