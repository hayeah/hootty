package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
		{name: "bare hostport", raw: "m4mini:20000", kind: remoteKindHTTP, display: "http://m4mini:20000"},
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

	r, err := parseRemoteFlag(server.Listener.Addr().String(), t.TempDir())
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
