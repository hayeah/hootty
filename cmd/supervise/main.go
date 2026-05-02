// Command supervise is a small reference consumer of the supervisor
// library: it spawns a single process on a libghostty-backed PTY,
// tees raw output to <dir>/<key>/pty.log, and serves the supervisor
// mux on <dir>/<key>/rpc.sock.
//
// Subcommands:
//
//	supervise run [flags] -- <cmd> [args...]    spawn a session
//	supervise __supervise [flags] -- <cmd> ...  (internal supervisor)
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "run":
		err = cmdRun(args)
	case "__supervise":
		err = cmdSupervise(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "supervise: unknown subcommand %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "supervise %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `supervise — reference CLI for the supervisor library

Usage:
  supervise run --state-dir <d> [--key <k>] -- <cmd> [args...]

The session state directory is <state-dir>/<key>/. The supervisor
serves rpc.sock and writes pty.log inside it. <key> auto-derives
from the command name + a short timestamp if not given.
`)
}
