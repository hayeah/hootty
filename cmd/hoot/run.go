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
	"github.com/hayeah/hootty/internal/shortid"
)

// cmdRun opens a PTY pair, forks `hoot __session` with the
// slave as stdio and the master on fd 3, then exits — the session
// process keeps running in its own session and serves rpc.sock.
//
// `hoot run` is intentionally fire-and-forget: the session
// holds the flock and the unix socket; clients (a future `attach`
// CLI, curl, an embedding HTTP server) talk to the session
// through rpc.sock.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	stateDir := fs.String("state-dir", defaultStateDir(), "session state directory")
	key := fs.String("key", "", "session key (default: random short id)")
	remoteFlag := fs.String("remote", "", "remote hoot serve URL (http://host:port, host:port, or ssh://host)")
	attach := fs.Bool("attach", false, "attach to the new session after it starts")
	noReconnect := fs.Bool("no-reconnect", false, "exit on first drop instead of auto-reconnecting during post-spawn attach (--remote only)")
	prefixSpec := fs.String("prefix-key", "C-^", "command prefix byte for post-spawn attach (e.g. C-^, ^a, 0x1c)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("run: missing command after --")
	}
	attachOpts, err := attachOptionsFromFlags(
		*prefixSpec,
		*noReconnect,
	)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	remote, err := parseRemoteFlag(*remoteFlag, *stateDir)
	if err != nil {
		return err
	}
	if remote != nil {
		defer remote.Close()
		payload, err := json.Marshal(createSessionReq{Argv: rest, Key: *key})
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
			return errors.New("remote run: response missing session key")
		}
		sessionKey := body.Session.Key
		fmt.Fprintf(os.Stderr, "hoot: session %q started (remote=%s)\n", body.Session.Key, remote.display)
		if !*attach {
			fmt.Println(sessionKey)
			return nil
		}
		exitCode, err := runAttachLoop(
			remoteAttachTarget(remote, sessionKey),
			attachOpts.PrefixByte,
			!attachOpts.NoReconnect,
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
			return fmt.Errorf("run: generate key: %w", err)
		}
		*key = generated
	} else if store.IsAlive(*key) {
		return fmt.Errorf("run: session %q is already alive — pick a different --key or kill it first", *key)
	}

	// Size the PTY to the caller's terminal if possible.
	cols, rows := uint16(80), uint16(24)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		c, r, err := term.GetSize(int(os.Stdin.Fd()))
		if err == nil {
			cols, rows = uint16(c), uint16(r)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getcwd: %w", err)
	}
	if _, err := spawnSessionFromSpec(*stateDir, *key, spawnSpec{
		Argv: rest,
		CWD:  cwd,
		Cols: cols,
		Rows: rows,
	}); err != nil {
		return fmt.Errorf("run: session did not start: %w", err)
	}

	fmt.Fprintf(os.Stderr, "hoot: session %q started (state-dir=%s)\n", *key, *stateDir)
	if !*attach {
		fmt.Println(*key)
		return nil
	}
	exitCode, err := runAttachLoop(
		localAttachTarget(*stateDir, *key),
		attachOpts.PrefixByte,
		false,
	)
	if exitCode != 0 || err != nil {
		return &exitError{code: exitCode, err: err}
	}
	return nil
}

// generateKey returns a random short id that doesn't collide with
// any existing session directory under the store's state dir.
func generateKey(store *session.Store) (string, error) {
	existing := map[string]bool{}
	states, err := store.List()
	if err != nil {
		return "", err
	}
	for _, st := range states {
		existing[st.Session.Key] = true
	}
	return shortid.Generate(existing)
}
