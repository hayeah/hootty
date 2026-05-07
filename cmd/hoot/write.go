package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/hayeah/hootty"
)

// cmdWrite implements `hoot write`. It POSTs bytes to the supervised
// PTY's master FD via /pty/input on rpc.sock (or the equivalent route
// on a remote hoot serve).
//
// Input source:
//
//	--input-file FILE   bytes from FILE, verbatim
//	DATA...             argv joined with no separator, then run
//	                    through Go's string-literal interpreter
//	                    (`strconv.Unquote`-style escapes)
//	(else stdin pipe)   bytes from stdin, verbatim
//
// Flags:
//
//	--paste              wrap body in DEC bracketed-paste markers
//	                     (\e[200~ … \e[201~). Errors with exit 2 if
//	                     the receiver does not have DECSET 2004
//	                     enabled.
//	--input-file FILE    read body from FILE (overrides argv/stdin)
//	--state-dir <d>      session state directory (default ~/.hoot)
//	--remote <url>       remote hoot serve URL
//
// Exit codes:
//
//	0   write delivered (HTTP 204)
//	1   I/O / network / unexpected server error
//	2   usage / parse error / unknown session / paste-not-enabled / payload contains \e[201~
//	3   no such session (server-side resolve)
func cmdWrite(args []string) error {
	fs := flag.NewFlagSet("write", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: hoot write [flags] <id-or-prefix> [DATA...]

Send bytes to the supervised PTY of a hoot session.

Input source (in priority order):
  --input-file FILE   read FILE, send bytes verbatim
  DATA...             argv joined with no separator and interpreted as
                      a Go double-quoted string literal (\xHH, \uXXXX,
                      \UXXXXXXXX, \NNN, \r \n \t \a \b \f \v, \\ \" \')
  (otherwise)         if stdin is a pipe, read it verbatim

Cheat sheet (most common bytes):
  Enter      \r            Tab        \t
  Backspace  \x7f          Escape     \x1b
  Ctrl-A..Z  \x01..\x1a
  Up         \x1b[A        Down       \x1b[B
  Right      \x1b[C        Left       \x1b[D
  F1..F4     \x1bOP \x1bOQ \x1bOR \x1bOS

Note: Go's grammar has no \e — write \x1b instead.

Flags:
  --paste               wrap body in bracketed-paste markers; errors
                        if the receiver has DECSET 2004 disabled
  --input-file FILE     read body from FILE (verbatim, no escape parse)
  --state-dir <d>       session state directory (default ~/.hoot)
  --remote <url>        remote hoot serve URL
`)
	}
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	remoteFlag := fs.String("remote", "", "remote hoot serve URL")
	paste := fs.Bool("paste", false, "wrap body in bracketed-paste markers")
	inputFile := fs.String("input-file", "", "read body from FILE (verbatim)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2, err: err}
	}
	rest := fs.Args()
	if len(rest) < 1 {
		fs.Usage()
		return &exitError{code: 2, err: fmt.Errorf("missing <id-or-prefix>")}
	}
	target := rest[0]
	dataArgs := rest[1:]

	body, err := resolveWriteBody(*inputFile, dataArgs)
	if err != nil {
		return &exitError{code: 2, err: err}
	}

	remote, err := parseRemoteFlag(*remoteFlag, *stateDir)
	if err != nil {
		return &exitError{code: 2, err: err}
	}
	if remote != nil {
		defer remote.Close()
		return writeRemote(remote, target, body, *paste)
	}
	return writeLocal(*stateDir, target, body, *paste)
}

// resolveWriteBody picks the body source per spec §6 precedence:
// --input-file > argv > stdin (if not a TTY). Returns a usage error
// if none of the three apply.
func resolveWriteBody(inputFile string, argv []string) ([]byte, error) {
	if inputFile != "" {
		f, err := os.Open(inputFile)
		if err != nil {
			return nil, fmt.Errorf("--input-file: %w", err)
		}
		defer f.Close()
		return io.ReadAll(f)
	}
	if len(argv) > 0 {
		return decodeArgvEscapes(argv)
	}
	if !stdinIsTTY() {
		return io.ReadAll(os.Stdin)
	}
	return nil, fmt.Errorf("no input: pass DATA arguments, --input-file, or pipe stdin")
}

func stdinIsTTY() bool {
	st, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (st.Mode() & os.ModeCharDevice) != 0
}

func writeLocal(stateDir, target string, body []byte, paste bool) error {
	store := session.NewStore(stateDir)
	state, err := store.Resolve(target)
	if err != nil {
		return &exitError{code: 3, err: err}
	}
	sockPath := filepath.Join(stateDir, state.Session.Key, "rpc.sock")
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialUnixSock(ctx, sockPath)
		},
	}}
	urlStr := "http://unix/pty/input"
	if paste {
		urlStr += "?paste=on"
	}
	req, err := http.NewRequest(http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return mapWriteStatus(resp)
}

func writeRemote(remote *Remote, target string, body []byte, paste bool) error {
	urlStr := "http://hoot/sessions/" + url.PathEscape(target) + "/input"
	if paste {
		urlStr += "?paste=on"
	}
	req, err := http.NewRequest(http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := httpClient(remote).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return mapWriteStatus(resp)
}

// mapWriteStatus translates the HTTP response from /pty/input (or
// the serve proxy) into a CLI exit code.
func mapWriteStatus(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return &exitError{code: 3, err: readMsg(resp)}
	case http.StatusConflict:
		// 409 from `?paste=on` when DECSET 2004 is off on the
		// receiver. Surfaced to the user as a clear "can't paste"
		// signal — exit 2 (usage) so scripts can branch on it.
		return &exitError{code: 2, err: readMsg(resp)}
	case http.StatusBadRequest:
		return &exitError{code: 2, err: readMsg(resp)}
	case http.StatusRequestEntityTooLarge:
		return &exitError{code: 2, err: readMsg(resp)}
	default:
		return &exitError{code: 1, err: fmt.Errorf("write: %s: %s", resp.Status, readMsg(resp))}
	}
}
