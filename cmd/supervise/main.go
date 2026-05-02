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
	case "list", "ls":
		err = cmdList(args)
	case "resolve":
		err = cmdResolve(args)
	case "attach":
		os.Exit(cmdAttach(args))
	case "serve":
		err = cmdServe(args)
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
  supervise run     [--state-dir <d>] [--key <k>] -- <cmd> [args...]
  supervise list    [--state-dir <d>]
  supervise resolve [--state-dir <d>] <id-or-prefix>
  supervise attach  [--state-dir <d>] [--no-full-replay]
                    [--prefix-key <key>] <id-or-prefix>
  supervise serve   [--state-dir <d>] --bind <host:port> [--prefix /api]

The session state directory is <state-dir>/<key>/. The supervisor
serves rpc.sock and writes pty.log inside it. --state-dir defaults
to ~/.supervise. If --key is omitted, a random short id (3-8 chars
from 0-9a-z minus l/o) is generated.

Anywhere a session key is accepted, you can pass either the full id
or any unique prefix (minimum 3 characters). Ambiguous prefixes
return an error listing the matches.

In an attach: <prefix>d detaches; <prefix><prefix> sends a literal
prefix byte; <prefix>? prints help. Default prefix is C-b.
`)
}
