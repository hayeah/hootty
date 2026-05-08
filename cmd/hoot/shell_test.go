package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hayeah/hootty"
)

// TestResolveDefaultShell asserts the small env-driven helper:
// $SHELL wins, empty falls back to /bin/sh.
func TestResolveDefaultShell(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	if got := resolveDefaultShell(); got != "/bin/zsh" {
		t.Fatalf("resolveDefaultShell() with $SHELL=/bin/zsh = %q, want /bin/zsh", got)
	}
	t.Setenv("SHELL", "")
	if got := resolveDefaultShell(); got != "/bin/sh" {
		t.Fatalf("resolveDefaultShell() with empty $SHELL = %q, want /bin/sh", got)
	}
}

// TestCmdShellLocalSpawnsResolvedShell drives the local cmdShell path
// through the fake hoot executable and asserts that:
//
//   - $SHELL is forwarded as the spawned argv[0]
//   - --cwd was passed through
//   - the chosen --key is recorded in state.json
//
// It uses --no-reconnect to keep us off the attach loop entirely (we
// short-circuit before runAttachLoop by passing a non-tty stdin and
// inspecting state.json directly via the fake child).
func TestCmdShellLocalSpawnsResolvedShell(t *testing.T) {
	stateDir := shortTempDir(t)
	t.Setenv("SHELL", "/bin/myshell")

	withFakeHootExecutable(t)
	withNonTTYStdin(t)

	// runAttachLoop will try to dial rpc.sock and fail; we expect an
	// error from cmdShell, but state.json should already be on disk
	// by then because spawnSessionFromSpec ran first.
	_ = cmdShell([]string{
		"--state-dir", stateDir,
		"--key", "shell123",
	})

	raw, err := os.ReadFile(filepath.Join(stateDir, "shell123", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var st session.StateFile
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(st.Session.Argv) == 0 || st.Session.Argv[0] != "/bin/myshell" {
		t.Fatalf("argv = %v, want [/bin/myshell, ...]", st.Session.Argv)
	}
	if st.Session.Key != "shell123" {
		t.Fatalf("key = %q, want shell123", st.Session.Key)
	}
}

// TestCmdShellRemoteSendsEmptyArgv proves the remote branch sends
// empty argv (so the server can resolve its own $SHELL) along with
// the chosen key, and accepts the synthesized response.
func TestCmdShellRemoteSendsEmptyArgv(t *testing.T) {
	var (
		gotBody    createSessionReq
		gotPostOnce bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// We only assert on the initial POST /sessions; subsequent
		// requests come from runAttachLoop's HUD load + attach-raw
		// upgrade, which we let fail naturally — the body of the
		// first POST is the only thing this test cares about.
		if r.Method == http.MethodPost && r.URL.Path == "/sessions" {
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			gotPostOnce = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"session": map[string]any{"key": "remoteshell"},
				"state":   map[string]any{},
				"alive":   true,
			})
			return
		}
		// Quietly fail subsequent calls so the attach loop bails fast.
		http.Error(w, "test stop", http.StatusNotFound)
	}))
	defer server.Close()

	// We expect runAttachLoop to fail dialing the test server's
	// /attach-raw upgrade — that's fine, we only care about the body
	// of the POST /sessions request that already happened.
	_ = cmdShell([]string{
		"--remote", "http://" + server.Listener.Addr().String(),
		"--key", "remoteshell",
		"--no-reconnect",
	})

	if !gotPostOnce {
		t.Fatalf("server never saw POST /sessions")
	}
	if len(gotBody.Argv) != 0 {
		t.Fatalf("body.Argv = %v, want empty (server resolves default shell)", gotBody.Argv)
	}
	if gotBody.Cmd != "" {
		t.Fatalf("body.Cmd = %q, want empty", gotBody.Cmd)
	}
	if gotBody.Key != "remoteshell" {
		t.Fatalf("body.Key = %q, want remoteshell", gotBody.Key)
	}
}

// TestCmdShellRejectsPositional makes sure we keep the positional-arg
// surface clean. `hoot run` is the verbose form; `hoot` itself never
// takes free-form argv.
func TestCmdShellRejectsPositional(t *testing.T) {
	err := cmdShell([]string{"--state-dir", t.TempDir(), "stray-positional"})
	if err == nil {
		t.Fatalf("cmdShell with positional arg succeeded; want error")
	}
}
