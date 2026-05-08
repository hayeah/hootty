package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"

	"golang.org/x/term"

	"github.com/hayeah/hootty"
)

// cmdShell is the entry point for the bare `hoot` and `hoot @host`
// (or fallthrough `hoot host`) forms — spawn the user's default shell
// and attach to it. Local: argv is the resolved local default shell.
// Remote: argv is empty and the remote serve resolves its own default
// shell; that way `hoot @m4mini` opens whatever the remote user's
// $SHELL is, not whatever the local user runs.
func cmdShell(args []string) error {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	key := fs.String("key", "", "session key (default: random short id)")
	remoteFlag := fs.String("remote", "", "remote hoot serve URL (host, user@host, host:port — defaults to ssh://; or http(s)://host:port, ssh://host)")
	noReconnect := fs.Bool("no-reconnect", false, "exit on first drop instead of auto-reconnecting (--remote only)")
	prefixSpec := fs.String("prefix-key", "C-^", "command prefix byte (e.g. C-^, ^a, 0x1c)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("shell: unexpected positional argument %q (did you mean `hoot run -- %s`?)", rest[0], rest[0])
	}

	if err := errIfNestedHootSession("shell"); err != nil {
		return err
	}

	attachOpts, err := attachOptionsFromFlags(*prefixSpec, *noReconnect, "hoot")
	if err != nil {
		return fmt.Errorf("shell: %w", err)
	}

	remote, err := parseRemoteFlag(*remoteFlag, *stateDir)
	if err != nil {
		return err
	}
	if remote != nil {
		defer remote.Close()
		// Empty argv asks the remote to resolve its own default shell.
		payload, err := json.Marshal(createSessionReq{Key: *key})
		if err != nil {
			return err
		}
		resp, err := httpClient(remote).Post("http://hoot/sessions", "application/json", bytes.NewReader(payload))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			return remoteResponseError(resp)
		}
		var body struct {
			*session.StateFile
			Alive      bool   `json:"alive"`
			SocketPath string `json:"socket_path"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return err
		}
		if body.StateFile == nil || body.Session.Key == "" {
			return errors.New("remote shell: response missing session key")
		}
		sessionKey := body.Session.Key
		fmt.Fprintf(os.Stderr, "hoot: session %q started (remote=%s)\n", sessionKey, remote.display)
		hud := newHUDState(loadHUDLine(remote, *stateDir, sessionKey))
		exitCode, err := runAttachLoop(
			remoteAttachTarget(remote, sessionKey),
			attachOpts.PrefixByte,
			!attachOpts.NoReconnect,
			hud,
			attachOpts.Restorer,
		)
		if exitCode != 0 || err != nil {
			return &exitError{code: exitCode, err: err}
		}
		return nil
	}

	store := session.NewStore(*stateDir)
	if *key == "" {
		generated, err := generateKey(store)
		if err != nil {
			return fmt.Errorf("shell: generate key: %w", err)
		}
		*key = generated
	} else if store.IsAlive(*key) {
		return fmt.Errorf("shell: session %q is already alive — pick a different --key or kill it first", *key)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("shell: getcwd: %w", err)
	}

	cols, rows := uint16(80), uint16(24)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		c, r, err := term.GetSize(int(os.Stdin.Fd()))
		if err == nil {
			cols, rows = uint16(c), uint16(r)
		}
	}

	if _, err := spawnSessionFromSpec(*stateDir, *key, spawnSpec{
		Argv: []string{resolveDefaultShell()},
		CWD:  cwd,
		Cols: cols,
		Rows: rows,
	}); err != nil {
		return fmt.Errorf("shell: session did not start: %w", err)
	}

	fmt.Fprintf(os.Stderr, "hoot: session %q started (state-dir=%s)\n", *key, *stateDir)
	hud := newHUDState(loadHUDLine(nil, *stateDir, *key))
	exitCode, err := runAttachLoop(
		localAttachTarget(*stateDir, *key),
		attachOpts.PrefixByte,
		false,
		hud,
		attachOpts.Restorer,
	)
	if exitCode != 0 || err != nil {
		return &exitError{code: exitCode, err: err}
	}
	return nil
}

// resolveDefaultShell picks the shell for a bare local `hoot`. $SHELL
// is what every interactive shell sets for its children; it's the same
// signal `xterm`, `tmux new-window`, and `nvim :terminal` consult.
// Falling back to /bin/sh keeps the no-$SHELL case working — POSIX
// guarantees /bin/sh exists.
func resolveDefaultShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}
