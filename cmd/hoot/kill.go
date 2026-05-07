package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"

	"github.com/hayeah/hootty"
)

// cmdKill is the `hoot kill` subcommand. It POSTs to /signal on the
// session's rpc.sock (or the equivalent route on a remote hoot
// serve), where the supervisor delivers the signal to the PTY's
// foreground process group — the same target the kernel uses for
// keyboard-generated signals like Ctrl-C.
//
// Flags:
//
//	-s <signal>   signal to send (default: TERM). Accepts TERM,
//	              SIGTERM, sigterm, 15, etc.
//	--state-dir <d>   session state directory (default: ~/.hoot)
//	--remote <url>    remote hoot serve URL (host, user@host, host:port
//	              — defaults to ssh://; or http(s)://host:port, ssh://host)
//
// Exit codes:
//
//	0   signal delivered
//	1   I/O / network / kernel error
//	2   usage error (bad flag, unknown signal, missing key)
//	3   no such session / ambiguous prefix
//	5   session exists but child not running
func cmdKill(args []string) error {
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: hoot kill [flags] <id-or-prefix>

Send a signal to the foreground process group of a hoot session's PTY
(the same target Ctrl-C reaches). Defaults to TERM.

Flags:
  -s <signal>           signal name or number (default TERM)
  --state-dir <d>       session state directory (default ~/.hoot)
  --remote <url>        remote hoot serve URL
`)
	}
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	remoteFlag := fs.String("remote", "", "remote hoot serve URL (host, user@host, host:port — defaults to ssh://; or http(s)://host:port, ssh://host)")
	sigSpec := fs.String("s", "TERM", "signal name or number")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2, err: err}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return &exitError{code: 2, err: fmt.Errorf("expected exactly one <id-or-prefix> argument")}
	}
	if _, err := parseSignal(*sigSpec); err != nil {
		// Validate locally before we go over the wire. The remote
		// will re-validate, but failing fast here gives a better
		// error message and exit code.
		return &exitError{code: 2, err: err}
	}

	body, err := json.Marshal(signalRequest{Signal: *sigSpec})
	if err != nil {
		return err
	}

	remote, err := parseRemoteFlag(*remoteFlag, *stateDir)
	if err != nil {
		return &exitError{code: 2, err: err}
	}
	if remote != nil {
		defer remote.Close()
		return killRemote(remote, rest[0], body)
	}
	return killLocal(*stateDir, rest[0], body)
}

func killLocal(stateDir, query string, body []byte) error {
	store := session.NewStore(stateDir)
	state, err := store.Resolve(query)
	if err != nil {
		return &exitError{code: 3, err: err}
	}
	sockPath := filepath.Join(stateDir, state.Session.Key, "rpc.sock")
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialUnixSock(ctx, sockPath)
		},
	}}
	req, err := http.NewRequest(http.MethodPost, "http://unix/signal", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return mapKillStatus(resp)
}

func killRemote(remote *Remote, query string, body []byte) error {
	url := "http://hoot/sessions/" + url.PathEscape(query) + "/signal"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient(remote).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return mapKillStatus(resp)
}

// mapKillStatus translates an HTTP response from /signal into a
// CLI exit. 204 is success; 4xx maps to documented exit codes;
// 5xx and unexpected statuses become exit 1.
func mapKillStatus(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		// Either "no such session" (remote shape) or "no /signal
		// route" on an old supervisor. Both → exit 3 with the body
		// as the message.
		return &exitError{code: 3, err: readMsg(resp)}
	case http.StatusConflict:
		return &exitError{code: 5, err: readMsg(resp)}
	case http.StatusBadRequest:
		return &exitError{code: 2, err: readMsg(resp)}
	default:
		return &exitError{code: 1, err: fmt.Errorf("kill: %s: %s", resp.Status, readMsg(resp))}
	}
}

func readMsg(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := string(bytes.TrimSpace(b))
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("%s", msg)
}
