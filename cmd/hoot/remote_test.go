package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestParseRemoteFlag(t *testing.T) {
	stateDir := t.TempDir()
	tests := []struct {
		name    string
		raw     string
		kind    remoteKind
		display string
		user    string
		host    string
		port    string
		wantErr bool
	}{
		{name: "empty", raw: "", display: ""},
		{name: "bare host", raw: "m4mini", kind: remoteKindSSH, display: "ssh://m4mini", host: "m4mini"},
		{name: "bare hostport", raw: "m4mini:20000", kind: remoteKindSSH, display: "ssh://m4mini:20000", host: "m4mini", port: "20000"},
		{name: "bare user host", raw: "me@m4mini", kind: remoteKindSSH, display: "ssh://me@m4mini", user: "me", host: "m4mini"},
		{name: "bare user host port", raw: "me@m4mini:2222", kind: remoteKindSSH, display: "ssh://me@m4mini:2222", user: "me", host: "m4mini", port: "2222"},
		{name: "http", raw: "http://m4mini:20000/path?q=1", kind: remoteKindHTTP, display: "http://m4mini:20000"},
		{name: "https", raw: "https://m4mini", kind: remoteKindHTTP, display: "https://m4mini"},
		{name: "ssh host", raw: "ssh://devbox", kind: remoteKindSSH, display: "ssh://devbox", host: "devbox"},
		{name: "ssh user host port", raw: "ssh://me@devbox:2222/path", kind: remoteKindSSH, display: "ssh://me@devbox:2222", user: "me", host: "devbox", port: "2222"},
		{name: "unsupported", raw: "ftp://devbox", wantErr: true},
		{name: "missing host", raw: "ssh://", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := parseRemoteFlag(tt.raw, stateDir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRemoteFlag err = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRemoteFlag: %v", err)
			}
			if tt.raw == "" {
				if r != nil {
					t.Fatalf("remote = %#v, want nil", r)
				}
				return
			}
			if r.kind != tt.kind || r.display != tt.display {
				t.Fatalf("remote = (%s, %q), want (%s, %q)", r.kind, r.display, tt.kind, tt.display)
			}
			if tt.kind == remoteKindSSH {
				if r.ssh.User != tt.user || r.ssh.Host != tt.host || r.ssh.Port != tt.port {
					t.Fatalf("ssh cfg = %+v, want user=%q host=%q port=%q", r.ssh, tt.user, tt.host, tt.port)
				}
			}
		})
	}
}

func TestHTTPClientDialsRemoteAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer server.Close()

	r, err := parseRemoteFlag("http://"+server.Listener.Addr().String(), t.TempDir())
	if err != nil {
		t.Fatalf("parseRemoteFlag: %v", err)
	}
	resp, err := httpClient(r).Get("http://hoot/healthz")
	if err != nil {
		t.Fatalf("GET via remote client: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestDialAttachRawUsesRemotePath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sessions/abc/attach-raw" {
			t.Fatalf("path = %q, want /sessions/abc/attach-raw", r.URL.Path)
		}
		if r.Header.Get("Upgrade") != "hoot-attach/1" {
			t.Fatalf("Upgrade = %q, want hoot-attach/1", r.Header.Get("Upgrade"))
		}
		w.Header().Set("Upgrade", "hoot-attach/1")
		w.Header().Set("Connection", "Upgrade")
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	defer server.Close()

	r, err := parseRemoteFlag("http://"+server.Listener.Addr().String(), t.TempDir())
	if err != nil {
		t.Fatalf("parseRemoteFlag: %v", err)
	}
	conn, err := dialAttachRaw(r, "abc")(context.Background())
	if err != nil {
		t.Fatalf("dialAttachRaw: %v", err)
	}
	_ = conn.Close()
}

func TestSSHRemoteLocalhostHealthz(t *testing.T) {
	if err := exec.Command("ssh", "-o", "BatchMode=yes", "localhost", "true").Run(); err != nil {
		t.Skipf("passwordless ssh localhost unavailable: %v", err)
	}
	if err := exec.Command("ssh", "-o", "BatchMode=yes", "localhost", "command -v hoot").Run(); err != nil {
		t.Skipf("hoot is not in localhost ssh PATH: %v", err)
	}

	stateDir, err := os.MkdirTemp("/tmp", "hoot-sshremote")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	r, err := parseRemoteFlag("ssh://localhost", stateDir)
	if err != nil {
		t.Fatalf("parseRemoteFlag: %v", err)
	}
	defer r.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://hoot/healthz", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := httpClient(r).Do(req)
	if err != nil {
		t.Fatalf("GET /healthz via ssh remote: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.OK {
		t.Fatalf("health body = %+v, want ok", body)
	}
}
