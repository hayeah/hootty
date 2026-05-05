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

func TestCmdListRemote(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions" {
			t.Fatalf("path = %q, want /sessions", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sessions": []map[string]any{
				{
					"session": map[string]any{"key": "abc123"},
					"state":   map[string]any{"kind": "test"},
					"alive":   true,
				},
			},
		})
	}))
	defer server.Close()

	out := captureStdout(t, func() {
		if err := cmdList([]string{"--remote", server.Listener.Addr().String()}); err != nil {
			t.Fatalf("cmdList: %v", err)
		}
	})
	var got session.StateFile
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("decode output %q: %v", out, err)
	}
	if got.Session.Key != "abc123" || !strings.Contains(string(got.State), "test") {
		t.Fatalf("output = %+v state=%s", got, got.State)
	}
	if strings.Contains(out, "alive") {
		t.Fatalf("output leaked serve-only alive field: %q", out)
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
		if err := cmdResolve([]string{"--remote", server.Listener.Addr().String(), "abc"}); err != nil {
			t.Fatalf("cmdResolve: %v", err)
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
