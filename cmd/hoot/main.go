// Command hoot is a small reference consumer of the session
// library: it spawns a single process on a libghostty-backed PTY,
// records output to <dir>/<key>/pty.cast, and serves the session
// mux on <dir>/<key>/rpc.sock.
//
// Subcommands:
//
//	hoot run [flags] -- <cmd> [args...]    spawn a session
//	hoot __session [flags] -- <cmd> ...  (internal session)
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
	case "__session":
		err = cmdSession(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "hoot: unknown subcommand %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "hoot %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `hoot — reference CLI for the session library

Usage:
  hoot run     [--state-dir <d>] [--key <k>] -- <cmd> [args...]
  hoot list    [--state-dir <d>]
  hoot resolve [--state-dir <d>] <id-or-prefix>
  hoot attach  [--host <addr>] [--state-dir <d>] [--no-reconnect]
                    [--no-ascii-cinema-playback]
                    [--prefix-key <key>] <id-or-prefix>
  hoot serve   [--state-dir <d>] --bind <host:port> [--prefix /api]

The session state directory is <state-dir>/<key>/. The session
serves rpc.sock and writes pty.cast inside it. --state-dir defaults
to ~/.hoot. If --key is omitted, a random short id (3-8 chars
from 0-9a-z minus l/o) is generated.

Anywhere a session key is accepted, you can pass either the full id
or any unique prefix (minimum 3 characters). Ambiguous prefixes
return an error listing the matches.

In an attach (mosh-style): <prefix>. detaches; <prefix>^ sends a
literal prefix byte; <prefix>Ctrl-Z suspends hoot attach (resume
with fg); <prefix>? prints help. Default prefix is C-^ (Ctrl-^, 0x1e).
`)
}
