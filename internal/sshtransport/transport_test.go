package sshtransport

import (
	"path/filepath"
	"strings"
	"testing"
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
		"-L\n" + plan.LocalSocket + ":" + plan.RemoteSocket,
		"me@devbox",
		"exec hoot serve --bind 'unix:/home/me/.hoot/.tunnels/012345.sock'",
	}
	for _, want := range checks {
		if !strings.Contains(joined, want) {
			t.Fatalf("TunnelArgs missing %q in:\n%s", want, joined)
		}
	}
	if plan.RemoteSocket != "/home/me/.hoot/.tunnels/012345.sock" {
		t.Fatalf("RemoteSocket = %q", plan.RemoteSocket)
	}
}
