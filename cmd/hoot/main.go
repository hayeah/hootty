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
	"errors"
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
	case "clone":
		err = cmdClone(args)
	case "kill":
		err = cmdKill(args)
	case "detach":
		err = cmdDetach(args)
	case "write":
		err = cmdWrite(args)
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
		var ee *exitError
		if errors.As(err, &ee) {
			if ee.err != nil {
				fmt.Fprintf(os.Stderr, "hoot %s: %v\n", cmd, ee.err)
			}
			os.Exit(ee.code)
		}
		fmt.Fprintf(os.Stderr, "hoot %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("exit %d", e.code)
}

func usage() {
	fmt.Fprint(os.Stderr, `hoot — reference CLI for the session library

Usage:
  hoot run     [--remote <url>] [--state-dir <d>] [--key <k>] [--attach]
                    [--no-reconnect] [--prefix-key <key>] -- <cmd> [args...]
  hoot list    [--remote <url>] [--state-dir <d>]
  hoot resolve [--remote <url>] [--state-dir <d>] <id-or-prefix>
  hoot clone   [--remote <url>] [--state-dir <d>] [--key <new-key>]
                    [--env NAME[=VALUE]] [--env-file <path>]
                    [--attach|--detach] [--strict] [<id-or-pattern>]
  hoot attach  [--remote <url>] [--state-dir <d>] [--no-reconnect]
                    [--prefix-key <key>] [--strict] [<id-or-pattern>]
  hoot kill    [--remote <url>] [--state-dir <d>] [-s SIG] [--strict] [<id-or-pattern>]
  hoot detach  [--remote <url>] [--state-dir <d>] [--strict]
                    [<session-prefix>[/<attachment-prefix>]]
  hoot write   [--remote <url>] [--state-dir <d>] [--paste] [--input-file FILE]
                    <id-or-prefix> [DATA...]
  hoot serve   [--state-dir <d>] --bind <host:port|unix:/path.sock> [--prefix /api]

The session state directory is <state-dir>/<key>/. The session
serves rpc.sock and writes pty.cast inside it. --state-dir defaults
to ~/.hoot. If --key is omitted, a random short id (3-8 chars
from 0-9a-z minus l/o) is generated.

Anywhere a session key is accepted, you can pass either the full id
or any unique prefix (minimum 3 characters). Ambiguous prefixes
return an error listing the matches.

--remote accepts host, host:port, user@host, user@host:port (all
default to ssh://), or an explicit http(s)://host:port or ssh://host
URL. For ssh:// remotes, the local CLI shells out to OpenSSH and
requires hoot in the remote PATH; no persistent hoot serve is required.

In an attach (mosh-style): <prefix>. detaches; <prefix>^ sends a
literal prefix byte; <prefix>Ctrl-Z suspends hoot attach (resume
with fg); <prefix>? prints help. Default prefix is C-^ (Ctrl-^, 0x1e).
`)
}
