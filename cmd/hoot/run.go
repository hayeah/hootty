package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/hayeah/hootty"
	"github.com/hayeah/hootty/internal/shortid"
)

// cmdRun opens a PTY pair, forks `hoot __session` with the
// slave as stdio and the master on fd 3, then exits — the session
// process keeps running in its own session and serves rpc.sock.
//
// `hoot run` is intentionally fire-and-forget: the session
// holds the flock and the unix socket; clients (a future `attach`
// CLI, curl, an embedding HTTP server) talk to the session
// through rpc.sock.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	key := fs.String("key", "", "session key (default: random short id)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("run: missing command after --")
	}

	store := session.NewStore(*stateDir)
	if *key == "" {
		generated, err := generateKey(store)
		if err != nil {
			return fmt.Errorf("run: generate key: %w", err)
		}
		*key = generated
	} else if store.IsAlive(*key) {
		return fmt.Errorf("run: session %q is already alive — pick a different --key or kill it first", *key)
	}

	// Size the PTY to the caller's terminal if possible.
	cols, rows := uint16(80), uint16(24)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		c, r, err := term.GetSize(int(os.Stdin.Fd()))
		if err == nil {
			cols, rows = uint16(c), uint16(r)
		}
	}

	// Open PTY pair; configure slave size so the child's TIOCGWINSZ
	// returns sane values.
	master, slave, err := pty.Open()
	if err != nil {
		return fmt.Errorf("pty.Open: %w", err)
	}
	if err := pty.Setsize(slave, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
		master.Close()
		slave.Close()
		return fmt.Errorf("setsize: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		master.Close()
		slave.Close()
		return fmt.Errorf("os.Executable: %w", err)
	}
	hootArgs := []string{
		"__session",
		"--state-dir", *stateDir,
		"--key", *key,
		"--",
	}
	hootArgs = append(hootArgs, rest...)

	cmd := exec.Command(self, hootArgs...)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.ExtraFiles = []*os.File{master}
	// Session runs in its own session (Setsid). The Service
	// inside will Setctty to claim the slave; keeping the
	// session session-less for this tty is what lets master-side
	// TIOCSWINSZ keep working after the child takes the fg pgrp.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		return fmt.Errorf("fork session: %w", err)
	}

	// Parent is done with both ends — child has its own dups.
	_ = slave.Close()
	_ = master.Close()

	// Wait for the session to open its socket so we can promise
	// the caller a usable session before returning.
	sockPath := filepath.Join(*stateDir, *key, "rpc.sock")
	if err := waitForSocket(sockPath, 3*time.Second); err != nil {
		return fmt.Errorf("run: session did not open socket: %w", err)
	}

	fmt.Fprintf(os.Stderr, "hoot: session %q started (state-dir=%s)\n", *key, *stateDir)
	fmt.Println(*key)
	_ = cmd.Process.Release()
	return nil
}

// generateKey returns a random short id that doesn't collide with
// any existing session directory under the store's state dir.
func generateKey(store *session.Store) (string, error) {
	existing := map[string]bool{}
	states, err := store.List()
	if err != nil {
		return "", err
	}
	for _, st := range states {
		existing[st.Session.Key] = true
	}
	return shortid.Generate(existing)
}

// waitForSocket polls for the unix socket file to appear.
func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", path)
}
