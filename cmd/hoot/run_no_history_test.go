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

// TestCmdRunNoHistoryRecordsState exercises the local cmdRun path with
// --no-history and asserts the spawned __session was forked with the
// flag — the fake hoot writes session.no_history=true into state.json
// when it observes --no-history on its argv. This pins the spawn-path
// plumbing that hands the flag from `hoot run` to `hoot __session` to
// SessionConfig.NoHistory.
func TestCmdRunNoHistoryRecordsState(t *testing.T) {
	stateDir := shortTempDir(t)

	withFakeHootExecutable(t)
	withNonTTYStdin(t)

	if err := cmdRun([]string{
		"--state-dir", stateDir,
		"--key", "nh1",
		"--no-history",
		"--", "fakecmd", "arg",
	}); err != nil {
		t.Fatalf("cmdRun: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(stateDir, "nh1", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var st session.StateFile
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !st.Session.NoHistory {
		t.Fatalf("session.no_history = false, want true (state=%s)", string(raw))
	}
}

// TestCmdRunNoHistoryDefaultFalse confirms that without --no-history the
// fake hoot does not see the flag, so SessionState.NoHistory stays
// false. The omitempty json tag means the key may be absent entirely;
// either way it must decode to false.
func TestCmdRunNoHistoryDefaultFalse(t *testing.T) {
	stateDir := shortTempDir(t)

	withFakeHootExecutable(t)
	withNonTTYStdin(t)

	if err := cmdRun([]string{
		"--state-dir", stateDir,
		"--key", "nh2",
		"--", "fakecmd", "arg",
	}); err != nil {
		t.Fatalf("cmdRun: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(stateDir, "nh2", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	var st session.StateFile
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if st.Session.NoHistory {
		t.Fatalf("session.no_history = true, want false")
	}
}

// TestCmdRunRemoteForwardsNoHistory pins the createSessionReq body so the
// remote `hoot run --remote --no-history` path serializes the flag as
// `no_history: true` over the wire — without that, a remote `hoot
// serve` would silently drop the user's intent.
func TestCmdRunRemoteForwardsNoHistory(t *testing.T) {
	var gotBody createSessionReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session": map[string]any{"key": "rmt-nh"},
			"alive":   true,
		})
	}))
	defer server.Close()

	_ = captureStdout(t, func() {
		if err := cmdRun([]string{
			"--remote", "http://" + server.Listener.Addr().String(),
			"--key", "rmt-nh",
			"--no-history",
			"--", "bash", "-lc", "true",
		}); err != nil {
			t.Fatalf("cmdRun: %v", err)
		}
	})

	if !gotBody.NoHistory {
		t.Fatalf("createSessionReq.no_history = false, want true (body=%+v)", gotBody)
	}
}
