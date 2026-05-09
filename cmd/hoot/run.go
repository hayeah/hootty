package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

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
	remoteFlag := fs.String("remote", "", "remote hoot serve URL (host, user@host, host:port — defaults to ssh://; or http(s)://host:port, ssh://host)")
	attach := fs.Bool("attach", false, "attach to the new session after it starts")
	noReconnect := fs.Bool("no-reconnect", false, "exit on first drop instead of auto-reconnecting during post-spawn attach (--remote only)")
	prefixSpec := fs.String("prefix-key", "C-^", "command prefix byte for post-spawn attach (e.g. C-^, ^a, 0x1c)")
	cwdFlag := fs.String("cwd", "", "working directory for the spawned command (default: current directory)")
	noHistory := fs.Bool("no-history", false, "session default: skip scrollback replay on attach (only restore the visible screen)")
	var envSpecs stringListFlag
	var envFiles stringListFlag
	fs.Var(&envSpecs, "env", "env override NAME or NAME=VALUE (repeatable)")
	fs.Var(&envFiles, "env-file", "dotenv-style env override file (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("run: missing command after --")
	}
	if *attach {
		if err := errIfNestedHootSession("run --attach"); err != nil {
			return err
		}
	}
	attachOpts, err := attachOptionsFromFlags(*prefixSpec, *noReconnect, "hoot")
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}

	envOverrides, err := parseEnvOverrides(envFiles, envSpecs, os.LookupEnv)
	if err != nil {
		return err
	}
	cwd, err := resolveRunCWD(*cwdFlag)
	if err != nil {
		return err
	}

	remote, err := parseRemoteFlag(*remoteFlag, *stateDir)
	if err != nil {
		return err
	}
	if remote != nil {
		defer remote.Close()
		payload, err := json.Marshal(createSessionReq{
			Argv:      rest,
			Key:       *key,
			CWD:       *cwdFlag,
			Env:       envOverrides,
			NoHistory: *noHistory,
		})
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

	if _, err := spawnSessionFromSpec(*stateDir, *key, spawnSpec{
		Argv:         rest,
		CWD:          cwd,
		EnvOverrides: envOverrides,
		Cols:         cols,
		Rows:         rows,
		NoHistory:    *noHistory,
	}); err != nil {
		return fmt.Errorf("run: session did not start: %w", err)
	}

	fmt.Fprintf(os.Stderr, "hoot: session %q started (state-dir=%s)\n", *key, *stateDir)
	if !*attach {
		fmt.Println(*key)
		return nil
	}
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

// resolveRunCWD returns an absolute working directory for the local
// spawn path: empty falls back to os.Getwd; non-empty is absolutized so
// state.json records the resolved path even when the caller passed a
// relative --cwd.
func resolveRunCWD(flag string) (string, error) {
	if flag == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getcwd: %w", err)
		}
		return cwd, nil
	}
	abs, err := filepath.Abs(flag)
	if err != nil {
		return "", fmt.Errorf("--cwd %q: %w", flag, err)
	}
	return abs, nil
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
