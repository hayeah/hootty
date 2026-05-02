package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/creack/pty"
)

// shortTempDir returns a temp dir path short enough for unix-socket
// listeners (macOS sun_path is 104 bytes; t.TempDir() paths often
// exceed that).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "sv")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// newTestPTY opens a real PTY pair and wraps the master in a
// LibghosttyPTY for tests that need a non-nil PTY but don't
// otherwise exercise terminal I/O. The slave is closed when the
// test ends.
func newTestPTY(t *testing.T) *LibghosttyPTY {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	p, err := NewLibghosttyPTY(master, 80, 24)
	if err != nil {
		_ = master.Close()
		_ = slave.Close()
		t.Fatalf("NewLibghosttyPTY: %v", err)
	}
	t.Cleanup(func() {
		_ = p.Close()
		_ = master.Close()
		_ = slave.Close()
	})
	return p
}

// servicefunc lets tests write inline Service implementations.
type serviceFunc func(ctx context.Context, super Supervisor) error

func (f serviceFunc) Run(ctx context.Context, super Supervisor) error { return f(ctx, super) }

// TestRunnerRunsServiceAndSignalsCancellation exercises the whole
// Runner.Run happy path: Service.Run receives a Supervisor,
// publishes initial state, and exits when ctx is cancelled. Also
// verifies that UpdateState writes state.json and that /state over
// rpc.sock returns the update.
func TestRunnerRunsServiceAndSignalsCancellation(t *testing.T) {
	dir := shortTempDir(t)
	ptyImpl := newTestPTY(t)

	svcStarted := make(chan struct{})
	svcExited := make(chan error, 1)

	svc := serviceFunc(func(ctx context.Context, super Supervisor) error {
		// Publish initial state.
		if err := super.UpdateState(map[string]string{"state": "starting"}); err != nil {
			return fmt.Errorf("initial UpdateState: %w", err)
		}
		close(svcStarted)
		<-ctx.Done()
		return ctx.Err()
	})

	runner := New(SupervisorConfig{
		StateDir: dir,
		Key:      "demo",
		Service:  svc,
		PTY:      ptyImpl,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { svcExited <- runner.Run(ctx) }()

	select {
	case <-svcStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("Service.Run did not start within 2s")
	}

	// rpc.sock should exist; /state should return the initial state.
	sockPath := filepath.Join(dir, "demo", "rpc.sock")
	client := httpOverUnix(sockPath)
	resp, err := client.Get("http://unix/state")
	if err != nil {
		t.Fatalf("GET /state: %v", err)
	}
	defer resp.Body.Close()
	var sf StateFile
	if err := json.NewDecoder(resp.Body).Decode(&sf); err != nil {
		t.Fatalf("decode /state: %v", err)
	}
	if sf.Supervisor.Key != "demo" {
		t.Errorf("got supervisor.key=%q, want demo", sf.Supervisor.Key)
	}
	if sf.Supervisor.PID == 0 {
		t.Errorf("expected supervisor.pid to be set, got 0")
	}
	var stateBlob map[string]string
	if err := json.Unmarshal(sf.State, &stateBlob); err != nil {
		t.Fatalf("decode state blob: %v", err)
	}
	if stateBlob["state"] != "starting" {
		t.Errorf("got state.state=%q, want starting", stateBlob["state"])
	}

	cancel()

	select {
	case err := <-svcExited:
		// Service returned ctx.Err() (context.Canceled); Runner.Run
		// returns it unchanged.
		if err == nil {
			t.Error("expected non-nil return from Runner.Run after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Runner.Run did not return within 2s of cancel")
	}
}

// TestRunnerRejectsIncompleteConfig checks the validation guards on
// Run. StateDir, Key, Service, PTY all required.
func TestRunnerRejectsIncompleteConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  SupervisorConfig
	}{
		{"no service", SupervisorConfig{StateDir: shortTempDir(t), Key: "k", PTY: newTestPTY(t)}},
		{"no pty", SupervisorConfig{StateDir: shortTempDir(t), Key: "k", Service: serviceFunc(func(context.Context, Supervisor) error { return nil })}},
		{"no key", SupervisorConfig{StateDir: shortTempDir(t), Service: serviceFunc(func(context.Context, Supervisor) error { return nil }), PTY: newTestPTY(t)}},
		{"no state dir", SupervisorConfig{Key: "k", Service: serviceFunc(func(context.Context, Supervisor) error { return nil }), PTY: newTestPTY(t)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := New(tt.cfg)
			err := runner.Run(context.Background())
			if err == nil {
				t.Fatal("expected error from incomplete config, got nil")
			}
		})
	}
}

// httpOverUnix returns an *http.Client that dials a unix socket.
func httpOverUnix(sockPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
		Timeout: 5 * time.Second,
	}
}
