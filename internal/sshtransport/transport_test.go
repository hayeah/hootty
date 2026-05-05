package sshtransport

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlPathIsShortAndStable(t *testing.T) {
	stateDir := filepath.Join("/Users/me", ".hoot", ".tunnels")
	a := ControlPath(stateDir, "me", "devbox.example.internal", "2222")
	b := ControlPath(stateDir, "me", "devbox.example.internal", "2222")
	c := ControlPath(stateDir, "me", "devbox.example.internal", "")
	if a != b {
		t.Fatalf("ControlPath unstable: %q != %q", a, b)
	}
	if a == c {
		t.Fatalf("ControlPath did not include port: %q", a)
	}
	if len(a) > 80 {
		t.Fatalf("ControlPath length = %d, want <= 80: %s", len(a), a)
	}
	if !strings.Contains(filepath.Base(a), "cm-") {
		t.Fatalf("ControlPath base = %q, want cm-*", filepath.Base(a))
	}
}

func TestTunnelArgsShape(t *testing.T) {
	cfg := Config{
		User:     "me",
		Host:     "devbox",
		Port:     "2222",
		StateDir: filepath.Join("/Users/me", ".hoot", ".tunnels"),
	}
	plan := BuildPlan(cfg, "012345", "/home/me")
	args := TunnelArgs(cfg, plan)
	joined := strings.Join(args, "\n")
	checks := []string{
		"ControlMaster=auto",
		"ControlPath=" + plan.ControlPath,
		"ControlPersist=60",
		"ServerAliveInterval=15",
		"ServerAliveCountMax=2",
		"ExitOnForwardFailure=yes",
		"-p\n2222",
		"-L\n" + plan.LocalSocket + ":" + plan.ForwardTarget,
		"me@devbox",
		"sh -lc",
		"hoot serve --bind",
		plan.RemoteBind,
		"/home/me/.hoot/.tunnels/012345.pid",
		"trap",
	}
	for _, want := range checks {
		if !strings.Contains(joined, want) {
			t.Fatalf("TunnelArgs missing %q in:\n%s", want, joined)
		}
	}
	if plan.RemoteSocket != "/home/me/.hoot/.tunnels/012345.sock" {
		t.Fatalf("RemoteSocket = %q", plan.RemoteSocket)
	}
	if plan.RemotePID != "/home/me/.hoot/.tunnels/012345.pid" {
		t.Fatalf("RemotePID = %q", plan.RemotePID)
	}
}

func TestWaitHTTPReadyRequiresHealthz(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "hoot-sshtransport")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)
	sockPath := filepath.Join(dir, "ready.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	server := &http.Server{Handler: mux}
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ln)
	}()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitHTTPReady(ctx, sockPath, make(chan error)); err != nil {
		t.Fatalf("waitHTTPReady: %v", err)
	}
}
