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

// TestCmdRunHonorsCWDAndEnv drives the local cmdRun path through the
// fake hoot executable and asserts that --cwd, --env, and --env-file
// reach the spawned __session: --cwd is recorded into state.json by
// the fake child, and the env vars surface as state-side fields the
// fake script copies in from its environment.
func TestCmdRunHonorsCWDAndEnv(t *testing.T) {
	stateDir := shortTempDir(t)
	cwdDir := filepath.Join(stateDir, "workdir")
	if err := os.MkdirAll(cwdDir, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	envFile := filepath.Join(t.TempDir(), "run.env")
	if err := os.WriteFile(envFile, []byte("RUN_FILE_VAR=from-file\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	withFakeHootExecutable(t)
	withNonTTYStdin(t)

	out := captureStdout(t, func() {
		if err := cmdRun([]string{
			"--state-dir", stateDir,
			"--key", "run123",
			"--cwd", cwdDir,
			"--env", "CLONE_VAR=run-direct",
			"--env-file", envFile,
			"--", "fakecmd", "arg1",
		}); err != nil {
			t.Fatalf("cmdRun: %v", err)
		}
	})
	if strings.TrimSpace(out) != "run123" {
		t.Fatalf("output = %q, want run123", out)
	}

	raw, err := os.ReadFile(filepath.Join(stateDir, "run123", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var st session.StateFile
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	if st.Session.CWD != cwdDir {
		t.Fatalf("session.cwd = %q, want %q", st.Session.CWD, cwdDir)
	}
	var state map[string]string
	if err := json.Unmarshal(st.State, &state); err != nil {
		t.Fatalf("unmarshal state.state: %v", err)
	}
	if state["clone_var"] != "run-direct" {
		t.Fatalf("CLONE_VAR (--env) = %q, want run-direct (full state=%v)", state["clone_var"], state)
	}
	if state["run_file_var"] != "from-file" {
		t.Fatalf("RUN_FILE_VAR (--env-file) = %q, want from-file (full state=%v)", state["run_file_var"], state)
	}
}

// TestCmdRunRelativeCWDIsAbsolutized confirms that a relative --cwd is
// resolved against the caller's process cwd before reaching the child,
// so state.json never holds a relative path that depends on lookup-time
// cwd.
func TestCmdRunRelativeCWDIsAbsolutized(t *testing.T) {
	stateDir := shortTempDir(t)
	rootDir := filepath.Join(stateDir, "rel-root")
	subDir := filepath.Join(rootDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(rootDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	withFakeHootExecutable(t)
	withNonTTYStdin(t)

	_ = captureStdout(t, func() {
		if err := cmdRun([]string{
			"--state-dir", stateDir,
			"--key", "rel123",
			"--cwd", "sub",
			"--", "fakecmd", "arg1",
		}); err != nil {
			t.Fatalf("cmdRun: %v", err)
		}
	})

	raw, err := os.ReadFile(filepath.Join(stateDir, "rel123", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var st session.StateFile
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	wantAbs, err := filepath.EvalSymlinks(subDir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	gotAbs, err := filepath.EvalSymlinks(st.Session.CWD)
	if err != nil {
		t.Fatalf("eval got cwd: %v", err)
	}
	if gotAbs != wantAbs {
		t.Fatalf("session.cwd = %q (resolved %q), want %q", st.Session.CWD, gotAbs, wantAbs)
	}
}

// TestCmdRunRemoteForwardsCWDAndEnv asserts the remote path serializes
// --cwd / --env / --env-file into createSessionReq so the server-side
// spawn can apply them. We don't drive the spawn; we only inspect the
// JSON body.
func TestCmdRunRemoteForwardsCWDAndEnv(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "remote.env")
	if err := os.WriteFile(envFile, []byte("FROM_FILE=ok\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	var gotBody createSessionReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sessions" {
			t.Fatalf("request = %s %s, want POST /sessions", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session": map[string]any{"key": "remote123"},
			"state":   map[string]any{},
			"alive":   true,
		})
	}))
	defer server.Close()

	out := captureStdout(t, func() {
		if err := cmdRun([]string{
			"--remote", "http://" + server.Listener.Addr().String(),
			"--key", "remote123",
			"--cwd", "/srv/work",
			"--env", "DIRECT=yes",
			"--env-file", envFile,
			"--", "bash", "-lc", "echo hi",
		}); err != nil {
			t.Fatalf("cmdRun: %v", err)
		}
	})
	if strings.TrimSpace(out) != "remote123" {
		t.Fatalf("output = %q, want remote123", out)
	}
	if gotBody.Key != "remote123" {
		t.Fatalf("body.Key = %q, want remote123", gotBody.Key)
	}
	if strings.Join(gotBody.Argv, " ") != "bash -lc echo hi" {
		t.Fatalf("body.Argv = %v", gotBody.Argv)
	}
	if gotBody.CWD != "/srv/work" {
		t.Fatalf("body.CWD = %q, want /srv/work (raw flag passed through)", gotBody.CWD)
	}
	if gotBody.Env["DIRECT"] != "yes" {
		t.Fatalf("body.Env[DIRECT] = %q, want yes (full env=%v)", gotBody.Env["DIRECT"], gotBody.Env)
	}
	if gotBody.Env["FROM_FILE"] != "ok" {
		t.Fatalf("body.Env[FROM_FILE] = %q, want ok (full env=%v)", gotBody.Env["FROM_FILE"], gotBody.Env)
	}
}

// TestResolveRunCWD covers the small helper directly: empty falls back
// to os.Getwd, non-empty is absolutized.
func TestResolveRunCWD(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	got, err := resolveRunCWD("")
	if err != nil {
		t.Fatalf("resolveRunCWD(\"\"): %v", err)
	}
	if got != wd {
		t.Fatalf("empty cwd = %q, want %q", got, wd)
	}
	got, err = resolveRunCWD("/tmp")
	if err != nil {
		t.Fatalf("resolveRunCWD(/tmp): %v", err)
	}
	if got != "/tmp" {
		t.Fatalf("abs cwd = %q, want /tmp", got)
	}
	got, err = resolveRunCWD("relative/path")
	if err != nil {
		t.Fatalf("resolveRunCWD(relative): %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("relative cwd not absolutized: %q", got)
	}
}

// withNonTTYStdin swaps os.Stdin for a pipe so cmdRun's term.GetSize
// branch is skipped during tests.
func withNonTTYStdin(t *testing.T) {
	t.Helper()
	old := os.Stdin
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdin = pr
	t.Cleanup(func() {
		os.Stdin = old
		_ = pr.Close()
		_ = pw.Close()
	})
}
