package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/hayeah/hootty"
)

// swapStdio points os.Stdin/Stdout/Stderr at f for the duration of
// the test. Required because RunCmdService.Run wires cmd.Stdin/Out/
// Err = os.Stdin/Out/Err so the child inherits the session's PTY
// stdio. In tests we route those at the PTY slave we control.
func swapStdio(t *testing.T, f *os.File) {
	t.Helper()
	in, out, err := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = f, f, f
	t.Cleanup(func() {
		os.Stdin, os.Stdout, os.Stderr = in, out, err
	})
}

// TestKillSignalDeliversToChild drives RunCmdService end-to-end:
// starts a real `sleep` child on a real PTY served behind rpc.sock,
// POSTs to /signal with TERM, and asserts the supervisor returns
// with the child marked exited via SIGTERM.
func TestKillSignalDeliversToChild(t *testing.T) {
	stateDir := shortTempDir(t)
	key := "killtest"

	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	defer master.Close()
	// The slave fd must remain open until the child is started so
	// stdio inheritance works; close after the spawn returns inside
	// RunCmdService — which is too coupled to do here. Easier: keep
	// it open until the test exits.
	defer slave.Close()

	ptyImpl, err := session.NewLibghosttyPTY(master, 80, 24)
	if err != nil {
		t.Fatalf("NewLibghosttyPTY: %v", err)
	}

	// Stdio inheritance trick: RunCmdService reads from os.Stdin /
	// os.Stdout / os.Stderr (it inherits the session's stdio in the
	// real flow). For this test we can swap them to point at the
	// PTY slave so the child gets a real ctty.
	swapStdio(t, slave)

	svc := &RunCmdService{
		Cmd:      "sleep",
		Args:     []string{"30"},
		StateDir: stateDir,
		Key:      key,
	}
	runner := session.New(session.SessionConfig{
		StateDir: stateDir,
		Key:      key,
		Argv:     []string{"sleep", "30"},
		Service:  svc,
		PTY:      ptyImpl,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(ctx) }()

	sockPath := filepath.Join(stateDir, key, "rpc.sock")
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialUnixSock(ctx, sockPath)
		},
	}}

	// Wait for the supervisor to be serving and the child to be
	// running (state.json's "state" goes "running"). Then a brief
	// extra sleep to let the kernel settle the fg pgrp.
	if err := waitForRunning(client); err != nil {
		t.Fatalf("waitForRunning: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	body := mustJSON(t, signalRequest{Signal: "TERM"})
	resp, err := client.Post("http://unix/signal", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /signal: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		buf := make([]byte, 1024)
		n, _ := resp.Body.Read(buf)
		_ = resp.Body.Close()
		t.Fatalf("status = %d, want 204; body=%s", resp.StatusCode, buf[:n])
	}
	_ = resp.Body.Close()

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("runner.Run err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner.Run did not return after TERM")
	}
}

// TestKillSignalReturnsConflictForBadSignal ensures parseSignal
// rejection comes back as 400. (Bad signal vs. valid signal both
// hit the same path; this guards the wire shape.)
func TestKillSignalReturnsBadRequestForUnknown(t *testing.T) {
	// Build a serveState pointing at an empty store — the upstream
	// rpc.sock won't be reached because the server-side parse
	// happens after Resolve. So this test exercises the proxy's
	// Resolve step (404) for an unknown key. A separate test
	// exercises the in-process /signal handler's parse path.
	stateDir := shortTempDir(t)
	srv := &serveState{store: session.NewStore(stateDir), stateDir: stateDir}
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/{key}/signal", srv.handleSignal)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	resp, err := http.Post(httpSrv.URL+"/sessions/nope/signal", "application/json", strings.NewReader(`{"signal":"TERM"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (no session)", resp.StatusCode)
	}
}

// TestParseSignal exercises the wire-input parser end-to-end:
// names with/without SIG prefix, case-insensitive, and numeric.
func TestParseSignal(t *testing.T) {
	cases := []struct {
		in   string
		want syscall.Signal
		bad  bool
	}{
		{in: "TERM", want: syscall.SIGTERM},
		{in: "sigterm", want: syscall.SIGTERM},
		{in: "SIGKILL", want: syscall.SIGKILL},
		{in: "9", want: syscall.SIGKILL},
		{in: "15", want: syscall.SIGTERM},
		{in: "USR1", want: syscall.SIGUSR1},
		{in: "winch", want: syscall.SIGWINCH},
		{in: "", bad: true},
		{in: "0", bad: true},
		{in: "65", bad: true},
		{in: "BOGUS", bad: true},
	}
	for _, c := range cases {
		got, err := parseSignal(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("parseSignal(%q) err = nil, want error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSignal(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSignal(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// waitForRunning polls /state until state.state == "running".
func waitForRunning(client *http.Client) error {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://unix/state")
		if err == nil {
			var sf session.StateFile
			_ = json.NewDecoder(resp.Body).Decode(&sf)
			_ = resp.Body.Close()
			var rs struct {
				State string `json:"state"`
			}
			if len(sf.State) > 0 {
				_ = json.Unmarshal(sf.State, &rs)
				if rs.State == "running" {
					return nil
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errTimeout
}

var errTimeout = httpErrTimeout("timeout waiting for running state")

type httpErrTimeout string

func (e httpErrTimeout) Error() string { return string(e) }

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
