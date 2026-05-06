package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/hayeah/hootty"
	"github.com/hayeah/hootty/internal/shortid"
)

// cmdDetach is the `hoot detach` subcommand. It DELETEs a session's
// attachments on rpc.sock (local) or proxies through `hoot serve`
// (remote). The argument is either a session prefix (close every
// attachment on that session) or a `<sess>/<att>` pair (close one
// attachment).
//
// Mirrors `hoot kill` for CLI shape, exit codes, and dial pattern —
// the two are siblings: kill targets the supervised process, detach
// targets the attach plumbing.
//
// Flags:
//
//	--state-dir <d>   session state directory (default: ~/.hoot)
//	--remote <url>    remote hoot serve URL (http://host:port,
//	                  host:port, or ssh://host)
//
// Exit codes:
//
//	0   detach delivered (any matched id closed; "detach <session>"
//	    with 0 live attachments is also success)
//	1   I/O / network error
//	2   usage error (bad flag, missing positional, malformed
//	    <sess>/<att>, prefix too short)
//	3   no such session / attachment, or ambiguous prefix
//	5   session resolved but not alive (stale state.json)
func cmdDetach(args []string) error {
	fs := flag.NewFlagSet("detach", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `usage: hoot detach [flags] <session-prefix>[/<attachment-prefix>]

Force-close one or all attachments on a hoot session. Without a slash,
detach every attachment on the resolved session; with a slash, detach
the single attachment whose id matches the prefix.

Flags:
  --state-dir <d>       session state directory (default ~/.hoot)
  --remote <url>        remote hoot serve URL
`)
	}
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	remoteFlag := fs.String("remote", "", "remote hoot serve URL (http://host:port, host:port, or ssh://host)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2, err: err}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return &exitError{code: 2, err: fmt.Errorf("expected exactly one <session-prefix>[/<attachment-prefix>] argument")}
	}
	sessPrefix, attPrefix, err := splitDetachArg(rest[0])
	if err != nil {
		return &exitError{code: 2, err: err}
	}

	remote, err := parseRemoteFlag(*remoteFlag, *stateDir)
	if err != nil {
		return &exitError{code: 2, err: err}
	}
	if remote != nil {
		defer remote.Close()
		return detachRemote(remote, sessPrefix, attPrefix)
	}
	return detachLocal(*stateDir, sessPrefix, attPrefix)
}

// splitDetachArg parses the positional `<sess>[/<att>]` argument.
// A bare `<sess>` form yields an empty attachment prefix. Forms
// like `foo/`, `/bar`, `foo/bar/baz` are usage errors.
func splitDetachArg(arg string) (sess, att string, err error) {
	if arg == "" {
		return "", "", errors.New("detach: empty argument")
	}
	parts := strings.Split(arg, "/")
	switch len(parts) {
	case 1:
		return parts[0], "", nil
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", fmt.Errorf("detach: malformed argument %q (expected <sess> or <sess>/<att>)", arg)
		}
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("detach: malformed argument %q (expected <sess> or <sess>/<att>)", arg)
	}
}

func detachLocal(stateDir, sessPrefix, attPrefix string) error {
	store := session.NewStore(stateDir)
	state, err := store.Resolve(sessPrefix)
	if err != nil {
		return &exitError{code: 3, err: err}
	}
	if !store.IsAlive(state.Session.Key) {
		return &exitError{code: 5, err: fmt.Errorf("session %q is not alive (stale state.json)", state.Session.Key)}
	}
	sockPath := filepath.Join(stateDir, state.Session.Key, "rpc.sock")

	// Resolve the attachment prefix locally (against the snapshot
	// returned by Resolve). The supervisor itself only knows
	// full ids — prefix resolution is a CLI affordance.
	upstreamPath := "/attachments"
	if attPrefix != "" {
		ids := make([]string, 0, len(state.Session.Attachments))
		for _, a := range state.Session.Attachments {
			ids = append(ids, a.ID)
		}
		match, err := shortid.Resolve(attPrefix, ids)
		if err != nil {
			return &exitError{code: 3, err: err}
		}
		upstreamPath = "/attachments/" + match
	}

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialUnixSock(ctx, sockPath)
		},
	}}
	req, err := http.NewRequest(http.MethodDelete, "http://unix"+upstreamPath, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer resp.Body.Close()
	return reportDetach(resp, state.Session.Key, attPrefix)
}

func detachRemote(remote *Remote, sessPrefix, attPrefix string) error {
	// For the remote case the CLI cannot walk the local state-dir
	// to resolve attachment ids. Fetch /sessions/{prefix} (returns
	// state.json with attachments[]), resolve locally, then DELETE.
	resolvedKey := sessPrefix
	if attPrefix != "" {
		stateURL := "http://hoot/sessions/" + url.PathEscape(sessPrefix)
		req, err := http.NewRequest(http.MethodGet, stateURL, nil)
		if err != nil {
			return err
		}
		resp, err := httpClient(remote).Do(req)
		if err != nil {
			return &exitError{code: 1, err: err}
		}
		var stateBody session.StateFile
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return mapDetachStatus(resp.StatusCode, body, sessPrefix, attPrefix)
		}
		if err := json.Unmarshal(body, &stateBody); err != nil {
			return &exitError{code: 1, err: fmt.Errorf("decode state: %w", err)}
		}
		resolvedKey = stateBody.Session.Key
		ids := make([]string, 0, len(stateBody.Session.Attachments))
		for _, a := range stateBody.Session.Attachments {
			ids = append(ids, a.ID)
		}
		match, err := shortid.Resolve(attPrefix, ids)
		if err != nil {
			return &exitError{code: 3, err: err}
		}
		attPrefix = match
	}

	target := "http://hoot/sessions/" + url.PathEscape(resolvedKey) + "/attachments"
	if attPrefix != "" {
		target += "/" + url.PathEscape(attPrefix)
	}
	req, err := http.NewRequest(http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient(remote).Do(req)
	if err != nil {
		return &exitError{code: 1, err: err}
	}
	defer resp.Body.Close()
	return reportDetach(resp, resolvedKey, attPrefix)
}

// reportDetach maps the HTTP response into an exit code and prints a
// short success message on stderr. The success path matches `hoot
// kill`'s style: one line, machine-greppable.
func reportDetach(resp *http.Response, sessKey, attID string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusNoContent {
		return mapDetachStatus(resp.StatusCode, body, sessKey, attID)
	}
	if attID == "" {
		closed := -1
		var parsed struct {
			Closed int `json:"closed"`
		}
		if json.Unmarshal(body, &parsed) == nil {
			closed = parsed.Closed
		}
		if closed >= 0 {
			fmt.Fprintf(os.Stderr, "hoot: detached %d attachment(s) from session %q\n", closed, sessKey)
		} else {
			fmt.Fprintf(os.Stderr, "hoot: detached all attachments from session %q\n", sessKey)
		}
	} else {
		fmt.Fprintf(os.Stderr, "hoot: detached attachment %q\n", sessKey+"/"+attID)
	}
	return nil
}

func mapDetachStatus(status int, body []byte, sessKey, attID string) error {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	switch status {
	case http.StatusNotFound:
		return &exitError{code: 3, err: fmt.Errorf("%s", msg)}
	case http.StatusConflict:
		return &exitError{code: 3, err: fmt.Errorf("%s", msg)}
	case http.StatusBadRequest:
		return &exitError{code: 2, err: fmt.Errorf("%s", msg)}
	default:
		_ = sessKey
		_ = attID
		return &exitError{code: 1, err: fmt.Errorf("detach: %d %s", status, msg)}
	}
}
