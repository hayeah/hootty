// Command hoot is a small reference consumer of the session
// library: it spawns a single process on a libghostty-backed PTY,
// records output to <dir>/<key>/pty.hootty.log, and serves the
// session mux on <dir>/<key>/rpc.sock.
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
	"strings"
)

// knownSubcommands is the closed set of dispatchable verbs. The
// top-level dispatch in main consults this to decide whether
// `hoot foo ...` is a real verb or should fall through to cmdShell
// as `hoot @foo ...`.
var knownSubcommands = map[string]bool{
	"run":       true,
	"list":      true,
	"ls":        true,
	"log":       true,
	"resolve":   true,
	"attach":    true,
	"clone":     true,
	"kill":      true,
	"detach":    true,
	"write":          true,
	"serve":          true,
	"version":        true,
	"install-remote": true,
	"__session":      true,
	"-h":        true,
	"--help":    true,
	"help":      true,
}

func main() {
	args := os.Args[1:]

	cmd, shellArgs, ok := dispatchTopLevel(args)
	if !ok {
		// dispatchTopLevel signaled an unknown-flag-only usage error.
		usage()
		os.Exit(2)
	}

	var err error
	switch cmd {
	case "shell":
		err = cmdShell(shellArgs)
	case "run":
		err = cmdRun(shellArgs)
	case "list", "ls":
		err = cmdList(shellArgs)
	case "log":
		err = cmdLog(shellArgs)
	case "resolve":
		err = cmdResolve(shellArgs)
	case "attach":
		os.Exit(cmdAttach(shellArgs))
	case "clone":
		err = cmdClone(shellArgs)
	case "kill":
		err = cmdKill(shellArgs)
	case "detach":
		err = cmdDetach(shellArgs)
	case "write":
		err = cmdWrite(shellArgs)
	case "serve":
		err = cmdServe(shellArgs)
	case "version":
		err = cmdVersion(shellArgs)
	case "install-remote":
		err = cmdInstallRemote(shellArgs)
	case "__session":
		err = cmdSession(shellArgs)
	case "help":
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

// dispatchTopLevel translates raw argv (without argv[0]) into a
// (verb, rest, ok) triple. The new sugar lives here:
//
//   - bare `hoot`               → ("shell", nil)
//   - `hoot @host …`            → ("shell", "--remote", "ssh://host", …)
//   - `hoot host …` (host not   → ("shell", "--remote", "ssh://host", …)
//     a known subcommand and not a flag)
//   - `hoot --flag …`           → ("shell", …)
//   - `hoot -h` / `hoot --help` → ("help", nil)
//   - `hoot run …`, etc.        → (verb, rest)
//
// ok=false signals a flag-only invocation that doesn't match anything
// (e.g. a future `hoot -X` that isn't a shell flag) and main should
// print usage and exit 2. Today this only fires when there is no
// recognized cmd at all, which can't happen in practice — kept as a
// guard rail for future edits.
func dispatchTopLevel(args []string) (string, []string, bool) {
	if len(args) == 0 {
		return "shell", nil, true
	}
	first := args[0]
	rest := args[1:]
	if first == "-h" || first == "--help" {
		return "help", nil, true
	}
	if strings.HasPrefix(first, "@") && len(first) > 1 {
		host := first[1:]
		return "shell", append([]string{"--remote", "ssh://" + host}, rest...), true
	}
	if knownSubcommands[first] {
		// Normalize empty slice to nil so dispatch tests can compare
		// against nil literals.
		if len(rest) == 0 {
			rest = nil
		}
		return first, rest, true
	}
	if strings.HasPrefix(first, "-") {
		// Flag-only invocation: route through shell so users can pass
		// e.g. `hoot --remote ssh://m4mini` without an explicit verb.
		return "shell", args, true
	}
	// Subcommand fallthrough: treat the first positional as a remote
	// host shorthand, mirroring `ssh m4mini`.
	return "shell", append([]string{"--remote", "ssh://" + first}, rest...), true
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

Quick shell:
  hoot                            # spawn local default shell, attach
  hoot @<host>                    # spawn shell on remote (ssh://<host>), attach
  hoot <host>                     # same as @<host> (subcommand fallthrough)

Usage:
  hoot run     [--remote <url>] [--state-dir <d>] [--key <k>] [--attach]
                    [--no-reconnect] [--prefix-key <key>]
                    [--cwd <path>] [--env NAME[=VALUE]] [--env-file <path>]
                    -- <cmd> [args...]
  hoot list    [--remote <url>] [--state-dir <d>]
  hoot log     [--state-dir <d>] [--strict] [--output <file>]
                    [--format vt|plain|asciinema] [<id-or-prefix>]
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
  hoot install-remote [--version vX.Y.Z] [--upload | --from-file <tarball>]
                      [--install-dir <dir>] <host>
  hoot version

The session state directory is <state-dir>/<key>/. The session
serves rpc.sock and writes pty.hootty.log inside it. --state-dir
defaults to ~/.hoot. If --key is omitted, a random short id (3-8
chars from 0-9a-z minus l/o) is generated.

`+"`hoot log`"+` views a recording: the default format is `+"`vt`"+` on a
tty (replays the recording into a fresh libghostty terminal and
emits its visible state) and `+"`plain`"+` on a pipe (greppable text).
The `+"`asciinema`"+` format emits asciicast v2 JSONL — pipe to
`+"`asciinema play -`"+` to replay at recorded timing. Use
`+"`hoot log --output FILE`"+` to write a recording to a file instead
of stdout; this is required when viewing the current session from
inside itself, otherwise stdout would append the rendered log back
into the same PTY recording.

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

Every spawned hoot session publishes $HOOT_SESSION=<key> in the
shell's environment. Attach paths (`+"`hoot`, `hoot @<host>`, `hoot attach`, `hoot run --attach`"+`)
refuse to nest when $HOOT_SESSION is set, mirroring tmux. Read-only
verbs (`+"`list`, `resolve`, `kill`, `detach`, `write`"+`) still work.
`+"`hoot log`"+` also works, except it refuses to write the current
session's own log to stdout; use `+"`hoot log --output FILE $HOOT_SESSION`"+`
for that case.
Use `+"`unset HOOT_SESSION`"+` to override.
`)
}
