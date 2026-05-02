package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/hayeah/supervisor"
)

// cmdRun opens a PTY pair, forks `supervise __supervise` with the
// slave as stdio and the master on fd 3, then exits — the worker
// process keeps running in its own session and serves rpc.sock.
//
// `supervise run` is intentionally fire-and-forget: the worker holds
// the flock and the unix socket; clients (a future `attach` CLI,
// curl, an embedding HTTP server) talk to the worker through
// rpc.sock.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	key := fs.String("key", "", "session key (default: auto from cmd name + timestamp)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("run: missing command after --")
	}
	if *key == "" {
		*key = autoKey(rest[0])
	}

	store := supervisor.NewStore(*stateDir)
	if store.IsAlive(*key) {
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
	superviseArgs := []string{
		"__supervise",
		"--state-dir", *stateDir,
		"--key", *key,
		"--",
	}
	superviseArgs = append(superviseArgs, rest...)

	cmd := exec.Command(self, superviseArgs...)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.ExtraFiles = []*os.File{master}
	// Worker runs in its own session (Setsid). The Service inside
	// will Setctty to claim the slave; keeping the worker
	// session-less for this tty is what lets master-side
	// TIOCSWINSZ keep working after the child takes the fg pgrp.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		return fmt.Errorf("fork worker: %w", err)
	}

	// Parent is done with both ends — child has its own dups.
	_ = slave.Close()
	_ = master.Close()

	// Wait for the worker to open its socket so we can promise the
	// caller a usable session before returning.
	sockPath := filepath.Join(*stateDir, *key, "rpc.sock")
	if err := waitForSocket(sockPath, 3*time.Second); err != nil {
		return fmt.Errorf("run: worker did not open socket: %w", err)
	}

	fmt.Fprintf(os.Stderr, "supervise: session %q started (state-dir=%s)\n", *key, *stateDir)
	fmt.Println(*key)
	_ = cmd.Process.Release()
	return nil
}

// autoKey builds a key from the command basename + a short
// timestamp suffix. Lowercase, letters / digits / dashes only.
func autoKey(cmdName string) string {
	base := filepath.Base(cmdName)
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return '-'
		}
	}, base)
	if safe == "" {
		safe = "sess"
	}
	suffix := strconv.FormatInt(time.Now().Unix()%100000, 10)
	return safe + "-" + suffix
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
