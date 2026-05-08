package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hayeah/hootty"
)

// twoSessionsHandler responds to GET /sessions with one alive and
// one dead session — used by the cmdList tests below to exercise the
// alive-only default and the --all override.
func twoSessionsHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions" {
			t.Fatalf("path = %q, want /sessions", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sessions": []map[string]any{
				{
					"session": map[string]any{"key": "alive1", "argv": []string{"bash"}, "cwd": "/srv"},
					"state":   map[string]any{"kind": "test"},
					"alive":   true,
				},
				{
					"session": map[string]any{"key": "dead01", "argv": []string{"sh"}, "cwd": "/tmp"},
					"state":   map[string]any{"kind": "test"},
					"alive":   false,
				},
			},
		})
	})
}

func TestCmdListRemoteJSON(t *testing.T) {
	server := httptest.NewServer(twoSessionsHandler(t))
	defer server.Close()

	out := captureStdout(t, func() {
		if err := cmdList([]string{"--remote", "http://" + server.Listener.Addr().String(), "--json"}); err != nil {
			t.Fatalf("cmdList: %v", err)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("--json default should be alive-only, got %d line(s):\n%s", len(lines), out)
	}
	var got session.StateFile
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("decode output %q: %v", lines[0], err)
	}
	if got.Session.Key != "alive1" || !strings.Contains(string(got.State), "test") {
		t.Fatalf("output = %+v state=%s", got, got.State)
	}
	if strings.Contains(out, "\"alive\"") {
		t.Fatalf("output leaked serve-only alive field: %q", out)
	}
}

func TestCmdListRemoteJSONAll(t *testing.T) {
	server := httptest.NewServer(twoSessionsHandler(t))
	defer server.Close()

	out := captureStdout(t, func() {
		if err := cmdList([]string{"--remote", "http://" + server.Listener.Addr().String(), "--json", "--all"}); err != nil {
			t.Fatalf("cmdList: %v", err)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("--json --all should emit both sessions, got %d line(s):\n%s", len(lines), out)
	}
}

func TestCmdListRemotePretty(t *testing.T) {
	server := httptest.NewServer(twoSessionsHandler(t))
	defer server.Close()

	out := captureStdout(t, func() {
		if err := cmdList([]string{"--remote", "http://" + server.Listener.Addr().String()}); err != nil {
			t.Fatalf("cmdList: %v", err)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("default should be alive-only, got %d line(s):\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "[alive1]\t") {
		t.Fatalf("expected line for alive1, got %q", lines[0])
	}
	if strings.Contains(lines[0], "(dead)") {
		t.Fatalf("alive session should not carry (dead) tag: %q", lines[0])
	}
	if strings.Contains(out, "dead01") {
		t.Fatalf("dead session leaked into default output: %q", out)
	}
}

func TestCmdListRemotePrettyAll(t *testing.T) {
	server := httptest.NewServer(twoSessionsHandler(t))
	defer server.Close()

	out := captureStdout(t, func() {
		if err := cmdList([]string{"--remote", "http://" + server.Listener.Addr().String(), "--all"}); err != nil {
			t.Fatalf("cmdList: %v", err)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("--all should emit both sessions, got %d line(s):\n%s", len(lines), out)
	}
	var deadLine string
	for _, l := range lines {
		if strings.HasPrefix(l, "[dead01]\t") {
			deadLine = l
		}
	}
	if deadLine == "" {
		t.Fatalf("dead session missing from --all output:\n%s", out)
	}
	if !strings.HasSuffix(deadLine, "\t(dead)") {
		t.Fatalf("dead session should carry (dead) tag, got %q", deadLine)
	}
}

func TestCmdResolveRemote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/abc/resolve" {
			t.Fatalf("path = %q, want /sessions/abc/resolve", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"key": "abc123"})
	}))
	defer server.Close()

	out := captureStdout(t, func() {
		if err := cmdResolve([]string{"--remote", "http://" + server.Listener.Addr().String(), "abc"}); err != nil {
			t.Fatalf("cmdResolve: %v", err)
		}
	})
	if strings.TrimSpace(out) != "abc123" {
		t.Fatalf("output = %q, want abc123", out)
	}
}

func TestCmdRunRemote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sessions" {
			t.Fatalf("request = %s %s, want POST /sessions", r.Method, r.URL.Path)
		}
		var body createSessionReq
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Key != "abc123" || strings.Join(body.Argv, " ") != "bash -lc echo hi" {
			t.Fatalf("body = %+v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session": map[string]any{"key": "abc123"},
			"state":   map[string]any{},
			"alive":   true,
		})
	}))
	defer server.Close()

	out := captureStdout(t, func() {
		if err := cmdRun([]string{"--remote", "http://" + server.Listener.Addr().String(), "--key", "abc123", "--", "bash", "-lc", "echo hi"}); err != nil {
			t.Fatalf("cmdRun: %v", err)
		}
	})
	if strings.TrimSpace(out) != "abc123" {
		t.Fatalf("output = %q, want abc123", out)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	fn()
	_ = w.Close()
	return <-done
}
