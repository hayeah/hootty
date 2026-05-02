package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/creack/pty"

	"github.com/hayeah/supervisor"
)

// cmdSupervise is the internal supervisor process that holds the
// flock, owns the PTY emulator + recorder, and serves rpc.sock. It
// is not meant to be invoked by hand; `supervise run` forks this
// with:
//
//   - stdin/stdout/stderr pointing at the PTY slave
//   - fd 3 = the PTY master (ExtraFiles[0])
//   - setsid so it's the controlling process of its own session
func cmdSupervise(args []string) error {
	fs := flag.NewFlagSet("__supervise", flag.ContinueOnError)
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	key := fs.String("key", "", "session key (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *key == "" {
		return errors.New("--key is required")
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("__supervise: missing command after --")
	}

	// Supervisor stderr is the PTY slave; the library's slog
	// default would leak supervisor-internal log lines into the
	// captured stream. Send slog to a file inside the state dir
	// instead.
	logPath := filepath.Join(*stateDir, *key, "supervisor.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		// Fall back silently; not fatal.
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	} else {
		defer logFile.Close()
		slog.SetDefault(slog.New(slog.NewTextHandler(logFile, nil)))
	}

	master := os.NewFile(3, "pty-master")
	if master == nil {
		return errors.New("__supervise: PTY master not provided on fd 3 (invoked outside `supervise run`?)")
	}

	cols, rows := uint16(80), uint16(24)
	if size, err := pty.GetsizeFull(master); err == nil && size.Cols > 0 && size.Rows > 0 {
		cols, rows = size.Cols, size.Rows
	}

	// The state dir is created by the supervisor library when it
	// opens the writer; we just need it to exist before we open
	// the recorder file inside it.
	dir := filepath.Join(*stateDir, *key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	rec, err := supervisor.NewRecorder(filepath.Join(dir, "pty.log"))
	if err != nil {
		return fmt.Errorf("recorder: %w", err)
	}

	ptyImpl, err := supervisor.NewLibghosttyPTY(master, cols, rows,
		supervisor.WithRecorder(rec),
	)
	if err != nil {
		return fmt.Errorf("new libghostty pty: %w", err)
	}
	defer ptyImpl.Close()

	svc := &RunCmdService{
		Cmd:  rest[0],
		Args: rest[1:],
	}

	run := supervisor.New(supervisor.SupervisorConfig{
		StateDir: *stateDir,
		Key:      *key,
		Service:  svc,
		PTY:      ptyImpl,
	})

	return run.Run(context.Background())
}
